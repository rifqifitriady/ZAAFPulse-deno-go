// Package egress is the gateway's egress endpoint (P5, spec docs/superpowers/specs/2026-11-22-p5-egress-design.md):
// the control plane sends each outbound request to a loopback listener with the target in a header, and Go makes the
// real request with the engine's client settings (proxy environment, trusted roots, 10 s connect), checks it against
// the pushed allow-list, refuses private addresses at connect time for untrusted URLs (public mode) and records every
// connection as an egress.event. Raw TCP goes through CONNECT on the same listener.
//
// Every request carries X-ZP-Egress-Token, DeriveToken(frontToken); without it the connection is closed with no
// reply.
package egress

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// TokenLabel is the HMAC message the egress token is derived with.
const TokenLabel = "zaafpulse egress v1"

// ConnectTimeout is the time a connection has to open (undici's connect timeout, as engine.ConnectTimeout).
const ConnectTimeout = 10 * time.Second

// maxReplay is the most request body kept to send again on a 307/308 redirect.
const maxReplay = 32 << 20

// DeriveToken is the egress token for a front token: hex HMAC-SHA256 keyed with frontToken (its text as bytes) over
// TokenLabel. lib/egress.js deriveToken computes the same.
func DeriveToken(frontToken string) string {
	m := hmac.New(sha256.New, []byte(frontToken))
	m.Write([]byte(TokenLabel))
	return hex.EncodeToString(m.Sum(nil))
}

// Server is the egress endpoint.
type Server struct {
	token  atomic.Pointer[string]
	policy atomic.Pointer[Policy]
	record func(Record)

	plain  *http.Transport // the engine client's settings, gzip on, own dialer
	public *http.Transport // as plain, no proxy, private addresses refused at connect

	// lookup resolves a name to addresses for the dialer (tests replace it: no test may reach real DNS).
	lookup         func(ctx context.Context, host string) ([]net.IP, error)
	connectTimeout time.Duration
	maxReplay      int
	now            func() time.Time

	mu      sync.Mutex
	srv     *http.Server
	tunnels map[net.Conn]struct{}
	closed  bool
}

// New builds the endpoint from the engine's client: its transport's settings (proxy environment, trusted roots, TLS
// handshake limit, pools) are copied, with Go's gzip request and transparent decode turned on and the dialer replaced
// by the endpoint's own (10 s connect; public mode refuses private addresses). record hears every Record; it may be
// called from many goroutines at once. A nil client gives net/http's default transport settings.
func New(client *http.Client, record func(Record)) *Server {
	s := &Server{
		record:         record,
		connectTimeout: ConnectTimeout,
		maxReplay:      maxReplay,
		now:            time.Now,
		tunnels:        map[net.Conn]struct{}{},
		lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, err
		},
	}
	var base *http.Transport
	if client != nil {
		if tr, ok := client.Transport.(*http.Transport); ok {
			base = tr.Clone()
		}
	}
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	base.DisableCompression = false // Accept-Encoding: gzip on every request, decoded for the caller (Decision 8)
	base.DialTLSContext = nil
	if base.TLSHandshakeTimeout == 0 {
		base.TLSHandshakeTimeout = ConnectTimeout
	}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return s.dial(ctx, network, addr, false)
	}
	pub := base.Clone()
	// Public mode connects straight to the target, as lib/net.js safeFetch did: through a proxy the address checked at
	// connect would be the proxy's, not the target's.
	pub.Proxy = nil
	pub.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return s.dial(ctx, network, addr, true)
	}
	s.plain, s.public = base, pub
	s.SetPolicy(Policy{Mode: ModeReport})
	return s
}

// SetToken sets the token every request must carry (DeriveToken(frontToken)); until it is set every request is
// dropped.
func (s *Server) SetToken(t string) { s.token.Store(&t) }

// SetPolicy replaces the allow-list; requests in flight keep the one they started with for the hop they are on.
func (s *Server) SetPolicy(p Policy) {
	p.Allow = append([]Rule(nil), p.Allow...)
	s.policy.Store(&p)
}

// Policy is the allow-list in force (never nil; a caller must not change it).
func (s *Server) Policy() *Policy { return s.policy.Load() }

// Handler serves "/" (forward), "/check" and CONNECT.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// Listen opens the endpoint on 127.0.0.1 on a free port and serves it until Close.
func (s *Server) Listen() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: ConnectTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Close stops the listener: requests in flight get up to grace to finish, then every connection (tunnels too) is
// closed.
func (s *Server) Close(grace time.Duration) {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.closed = true
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), grace)
		if srv.Shutdown(ctx) != nil {
			_ = srv.Close()
		}
		cancel()
	}
	s.mu.Lock()
	for c := range s.tunnels {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.plain.CloseIdleConnections()
	s.public.CloseIdleConnections()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		drop(w)
		return
	}
	switch {
	case r.Method == http.MethodConnect:
		s.tunnel(w, r)
	case r.URL.Path == "/check":
		s.check(w, r)
	default:
		s.forward(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	want := s.token.Load()
	if want == nil || *want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Zp-Egress-Token")), []byte(*want)) == 1
}

// drop closes the connection without a reply.
func drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			_ = c.Close()
			return
		}
	}
	panic(http.ErrAbortHandler) // net/http closes the connection, nothing written
}

func (s *Server) emit(r Record) {
	if s.record != nil {
		s.record(r)
	}
}

// privateError is a connect refused by public mode.
type privateError struct{ addr string }

func (e privateError) Error() string {
	return e.addr + " is a private or local address; it is not reached for a public URL"
}

// dial opens a connection within the connect limit: the name resolved by s.lookup, each address tried in turn; in
// public mode a private address is refused before the connect (the dialer's Control hook), so every address a name
// resolves to, on every redirect hop, is checked at the moment it is used (no DNS rebinding).
func (s *Server) dial(ctx context.Context, network, addr string, public bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.connectTimeout)
	defer cancel()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = s.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	d := net.Dialer{KeepAlive: 30 * time.Second}
	if public {
		// As lib/net.js assertPublic: a name with any private address is refused, not only the address dialled.
		for _, ip := range ips {
			if IsPrivate(ip) {
				return nil, &net.OpError{Op: "dial", Net: network, Err: privateError{addr: ip.String()}}
			}
		}
		d.Control = refusePrivate
	}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

// refusePrivate is the public-mode dialer's Control hook: it runs after the address is chosen and before the connect.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || IsPrivate(net.ParseIP(host)) {
		return privateError{addr: host}
	}
	return nil
}

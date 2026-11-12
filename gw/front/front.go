// Package front is the gateway's front door (P4a, docs/superpowers/specs/2026-11-12-p4a-go-front-design.md): it holds
// the main port (TLS when configured) and forwards every request to the control plane's internal port on 127.0.0.1,
// each one with an X-Zp-Client header that names the client (D3, header design; client.go). In P4a every route class is
// on the js engine, so everything is forwarded.
package front

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Config is the front section of config.push with the token of secrets.handoff.
type Config struct {
	Host     string
	Port     int
	CertFile string // empty: plain HTTP
	KeyFile  string
	Upstream int    // the control plane's internal port on 127.0.0.1
	Token    string // frontToken
	Engines  map[string]string
}

// NoHostHeader flags a request that came without a Host header (HTTP/1.0): Go's transport cannot send an empty Host,
// so the internal listener (lib/front/internal.js) removes the Host Go writes and this header (P4a final review I1).
const NoHostHeader = "X-Zp-No-Host"

// AbsoluteFormError is the answer to an absolute-form target, the same text server.js sends on its own port.
const AbsoluteFormError = "absolute-form request targets are not accepted"

// proxyHeaders are the headers the control plane's proxy checks read (server.js, lib/auth.js), in canonical form.
var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port",
	"Forwarded", "Via", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip"}

// MaxHeaderBytes makes the public port refuse what Node refuses: Node's limit is 16 KiB of request line and headers,
// and net/http reads MaxHeaderBytes plus 4096 bytes before it answers 431 (P4a final review m2, P4b entry condition 6).
const MaxHeaderBytes = 16384 - 4096

// GoClasses are the route classes the go engine serves (P4b: chat, not streaming; the engine hands the rest of a
// class's requests to js itself). A config.push that puts another class on go is refused.
var GoClasses = map[string]bool{"chat": true}

// GoEngine serves a request of a class on the go engine, or hands it to js; Shadow forwards a request of a class on js
// marked for the compare (gw/engine Engine).
type GoEngine interface {
	Serve(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler)
	Shadow(class string, w http.ResponseWriter, r *http.Request, rawPath string, js http.Handler,
		own func(method, path string, body []byte) (int, []byte, error))
}

// The gateway's marks for the control plane (lib/front/internal.js): a request it shadows, and a request of its own.
// A client's copy of either is removed from every request.
const (
	ShadowHeader = "X-Zp-Shadow"
	OwnHeader    = "X-Zp-Own"
)

// ErrAddrInUse: another process holds the main port. The gateway exits 4.
var ErrAddrInUse = errors.New("front: the main port is in use")

// Server is the front: one http.Server on the main port and one pool of upstream connections.
type Server struct {
	cfg      Config
	srv      *http.Server
	ln       net.Listener
	proxy    *httputil.ReverseProxy
	engines  atomic.Pointer[map[string]string]
	inflight atomic.Int64
	goEngine GoEngine
	shadow   atomic.Pointer[map[string]bool]
	upstream *url.URL
}

// SetShadow replaces the route classes whose js requests are compared with the go engine (config.push front.shadow).
func (s *Server) SetShadow(classes []string) {
	m := make(map[string]bool, len(classes))
	for _, c := range classes {
		m[c] = true
	}
	s.shadow.Store(&m)
}

// own calls the control plane's internal port as the gateway itself (the shadow endpoints under /_zp/).
func (s *Server) own(method, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.upstream.String()+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	// The gateway names itself: loopback, at its own main port (the control plane accepts no port 0).
	req.Header.Set(ClientHeader, ClientValue(s.cfg.Token, s.ln.Addr().String(), false))
	req.Header.Set(OwnHeader, "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.proxy.Transport.RoundTrip(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, b, err
}

// SetGoEngine installs the go engine before Serve (without one, a class on go is answered 501).
func (s *Server) SetGoEngine(e GoEngine) { s.goEngine = e }

// Listen loads the TLS files (when set) and opens the main port. ErrAddrInUse when the port is taken.
func Listen(cfg Config, logger *log.Logger) (*Server, error) {
	var tlsCfg *tls.Config
	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("front: TLS: %w", err)
		}
		// HTTP/1.1 only, as the control plane's own https server answers today.
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		if addrInUse(err) {
			return nil, fmt.Errorf("%w: %s:%d", ErrAddrInUse, cfg.Host, cfg.Port)
		}
		return nil, fmt.Errorf("front: listen on %s:%d: %w", cfg.Host, cfg.Port, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	s := &Server{cfg: cfg, ln: ln}
	s.SetEngines(cfg.Engines)
	s.SetShadow(nil)
	quiet := QuietLogger(logger, time.Now)
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Upstream))}
	s.upstream = target
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The request target exactly as the client sent it (P4b review I2): net/http re-escapes a path it decoded
			// ("\" and "|" become %5C and %7C) and drops query pairs it cannot parse (";"), so the control plane would
			// route another text than in js mode. Opaque is written as it is; RawQuery is kept whole.
			// A target that starts with "//" cannot go in Opaque (net/url writes it as "http://..."): its path is set
			// with RawPath, which is written as sent whenever it is a valid escaping of the path (re-review N1).
			if raw := RawPath(pr.In.RequestURI); strings.HasPrefix(raw, "//") {
				pr.Out.URL.Path, pr.Out.URL.RawPath = pr.In.URL.Path, raw
			} else {
				pr.Out.URL.Opaque = raw
			}
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			// The client's Host. Without one (HTTP/1.0) Go's transport would write the internal 127.0.0.1:<port>, so
			// the request is flagged and the internal listener removes that Host: the host check sees none, as in js
			// mode (P4a final review I1). Set and Del replace any copy of the flag the client sent.
			pr.Out.Host = pr.In.Host
			if pr.In.Host == "" {
				pr.Out.Header.Set(NoHostHeader, "1")
			} else {
				pr.Out.Header.Del(NoHostHeader)
			}
			// The control plane reads these as the client sent them (its proxy checks look for their presence), so
			// they go through unchanged and the front adds none. Rewrite drops the X-Forwarded ones and Forwarded
			// from Out before it runs, and every header the client names in Connection; Node keeps them all, so they
			// are put back (P4a final review m1).
			for _, h := range proxyHeaders {
				if v, ok := pr.In.Header[h]; ok {
					pr.Out.Header[h] = append([]string(nil), v...)
				}
			}
			// Set replaces any X-Zp-Client the client sent.
			pr.Out.Header.Set(ClientHeader, ClientValue(cfg.Token, pr.In.RemoteAddr, pr.In.TLS != nil))
		},
		Transport: &http.Transport{
			DisableCompression:  true, // never add Accept-Encoding: the client's headers go as sent
			MaxIdleConnsPerHost: 64,
			// Shorter than the internal listener's keep-alive (130 s): the front always closes an idle connection first.
			IdleConnTimeout: 120 * time.Second,
		},
		FlushInterval: -1, // SSE and chunked bodies pass through at once
		ErrorLog:      quiet,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the client is gone
			}
			quiet.Printf("front: %s %s: %v", r.Method, RawPath(r.RequestURI), err)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"the control plane is not answering"}`))
		},
	}
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          quiet,
	}
	return s, nil
}

// Serve answers on the main port until Shutdown or Close (then it returns nil).
func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Addr is the main port's address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Inflight is the number of requests being answered (health.inflight).
func (s *Server) Inflight() int64 { return s.inflight.Load() }

// SetEngines replaces the engine of each route class (a later config.push).
func (s *Server) SetEngines(m map[string]string) {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	s.engines.Store(&c)
}

func (s *Server) engine(class string) string {
	if class == "" {
		return "js"
	}
	if e, ok := (*s.engines.Load())[class]; ok {
		return e
	}
	return "js"
}

// Shutdown stops accepting, waits for the requests in flight until ctx ends, then closes what is left.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		_ = s.srv.Close()
	}
	return err
}

// Close stops at once: the control plane is gone, there is nothing to forward to.
func (s *Server) Close() error { return s.srv.Close() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	if r.URL.IsAbs() {
		// net/http has dropped this request's Host header (RFC 9112 3.2.2: the target's host wins), and the control
		// plane checks the Host header: neither can be handed on, so it is refused here, as server.js refuses it on its
		// own port (P4a final review I1).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"` + AbsoluteFormError + `"}`))
		return
	}
	if r.Method == http.MethodConnect {
		// Node has no "connect" listener and closes the socket without an answer (P4a re-review r1).
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Header.Del(ShadowHeader)
	r.Header.Del(OwnHeader)
	raw := RawPath(r.RequestURI)
	class := Classify(raw)
	if s.engine(class) == "go" && GoClasses[class] && s.goEngine != nil {
		s.goEngine.Serve(class, w, r, raw, s.proxy)
		return
	}
	if s.engine(class) == "js" && (*s.shadow.Load())[class] && GoClasses[class] && s.goEngine != nil {
		s.goEngine.Shadow(class, w, r, raw, s.proxy, s.own)
		return
	}
	if s.engine(class) != "js" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"the go engine is not available in this gateway"}`))
		return
	}
	s.proxy.ServeHTTP(w, r)
}

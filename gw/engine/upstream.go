package engine

// The engine's provider calls (P4b): what lib/gateway/providers.js request() and lib/net.js fetchSameOrigin do, with Go's
// client. The proxy comes from HTTPS_PROXY, HTTP_PROXY and NO_PROXY (the control plane passes them on an allow-list,
// lib/ipc/env.js); the trusted roots are the system's plus the PEM files named by SSL_CERT_FILE, NODE_EXTRA_CA_CERTS and
// DENO_CERT (Go reads SSL_CERT_FILE and SSL_CERT_DIR itself on Linux, never on macOS or Windows). The call has 120 s to
// get the provider's answer started (TIMEOUT_MS; the body then has no limit, as in JS); a connection has 10 s (undici's
// connect timeout). A redirect is followed (at most 5, same method and body) only to the same origin.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// Timeout is providers.js TIMEOUT_MS: the provider must start its answer within it.
	Timeout = 120 * time.Second
	// ConnectTimeout is undici's default connect timeout.
	ConnectTimeout = 10 * time.Second
	maxRedirects   = 5
)

// Upstream calls providers.
type Upstream struct {
	client  *http.Client
	timeout time.Duration
}

// NewUpstream builds the client from the environment (getenv: os.Getenv).
func NewUpstream(getenv func(string) string) *Upstream {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "DENO_CERT"} {
		if f := getenv(name); f != "" {
			if pem, err := os.ReadFile(f); err == nil {
				roots.AppendCertsFromPEM(pem)
			}
		}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: ConnectTimeout,
		MaxIdleConnsPerHost: 64,
		// The request is written once per call (a large body bypasses the buffer); a small write buffer keeps a held
		// stream's connection light (P4c Decision 11). The read buffer stays at net/http's 4 KiB.
		WriteBufferSize:    1 << 10,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: true, // the body goes to the client as the provider wrote it, as fetch gives it
		ForceAttemptHTTP2:  true,
	}
	return &Upstream{
		client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		timeout: Timeout,
	}
}

// Result of one call: the provider's status and body, or Why the call failed (the text after "<provider id>: ").
type Result struct {
	Status int
	Body   []byte
	Why    string
}

// Start sends the plan's request and returns the provider's answer once its headers are in, its body unread, or Why the
// call failed. Closing the answer's body ends the call. ctx ends when the client goes away. The 120 s limit covers
// the headers only; the body has none, as in JS.
func (u *Upstream) Start(ctx context.Context, p *Plan) (*http.Response, string) {
	ctx, cancel := context.WithCancel(ctx)
	var timedOut atomic.Bool
	timer := time.AfterFunc(u.timeout, func() { timedOut.Store(true); cancel() })
	fail := func(why string) (*http.Response, string) {
		timer.Stop()
		cancel()
		return nil, why
	}
	current := p.URL
	for hop := 0; hop <= maxRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, current, &onceBody{b: p.Body})
		if err != nil {
			return fail("fetch failed")
		}
		req.ContentLength = int64(len(p.Body))
		req.GetBody = func() (io.ReadCloser, error) { return &onceBody{b: p.Body}, nil } // p.Body, read when called
		req.Header = p.Header.Clone()
		resp, err := u.client.Do(req)
		if err != nil {
			if timedOut.Load() {
				return fail("timed out")
			}
			return fail(ErrorCode(err))
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			timer.Stop() // the answer has started: the body has no time limit, as in JS
			resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, ""
		}
		_ = resp.Body.Close()
		from, _ := url.Parse(current)
		next, err := from.Parse(loc)
		if err != nil {
			return fail("fetch failed")
		}
		if origin(next) != origin(from) {
			return fail("refused a redirect from " + from.Host + " to " + next.Host +
				"; credentials are not forwarded to another host")
		}
		current = next.String()
	}
	return fail("too many redirects")
}

// Call sends the plan's request and reads the whole answer.
func (u *Upstream) Call(ctx context.Context, p *Plan) Result {
	resp, why := u.Start(ctx, p)
	if why != "" {
		return Result{Why: why}
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil && ctx.Err() != nil {
		return Result{Status: resp.StatusCode, Body: body, Why: "client gone"}
	}
	return Result{Status: resp.StatusCode, Body: body}
}

// onceBody is a request body that lets go of its bytes once they are read or the request is done with it, so a long
// stream does not keep its prompt alive (P4c Decision 11). Read and Close may run at once (net/http's rule).
type onceBody struct {
	mu sync.Mutex
	b  []byte
}

func (o *onceBody) Read(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.b) == 0 {
		o.b = nil
		return 0, io.EOF
	}
	n := copy(p, o.b)
	o.b = o.b[n:]
	if len(o.b) == 0 {
		o.b = nil
	}
	return n, nil
}

func (o *onceBody) Close() error {
	o.mu.Lock()
	o.b = nil
	o.mu.Unlock()
	return nil
}

// cancelBody ends the call when the answer's body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + u.Hostname() + ":" + port
}

var winsock = map[syscall.Errno]string{10061: "ECONNREFUSED", 10054: "ECONNRESET", 10060: "ETIMEDOUT",
	10065: "EHOSTUNREACH", 10051: "ENETUNREACH", 10053: "ECONNABORTED"}

// ErrorCode is what the JS gateway names a failed fetch by (err.cause.code of Node's fetch, else its message): the
// common system and undici codes, else "fetch failed". Deno names some of these differently; a failed call has no
// provider answer, so the shadow compare never reads it.
func ErrorCode(err error) string {
	// Winsock codes (Windows) by the names Node gives them there (re-review N4); no Unix errno is this large.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name, ok := winsock[errno]; ok {
			return name
		}
	}
	var dns *net.DNSError
	var hn x509.HostnameError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "ENOTFOUND"
		}
		return "EAI_AGAIN"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "UND_ERR_SOCKET"
	case errors.As(err, &hn):
		return "ERR_TLS_CERT_ALTNAME_INVALID"
	case errors.As(err, &ua):
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	case errors.As(err, &ci):
		if ci.Reason == x509.Expired {
			return "CERT_HAS_EXPIRED"
		}
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "UND_ERR_CONNECT_TIMEOUT"
	}
	return "fetch failed"
}

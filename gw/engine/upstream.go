package engine

// The engine's provider calls (P4b): what lib/gateway/providers.js request() and lib/net.js fetchSameOrigin do, with Go's
// client. The proxy comes from HTTPS_PROXY, HTTP_PROXY and NO_PROXY (the control plane passes them on an allow-list,
// lib/ipc/env.js); the trusted roots are the system's plus the PEM files named by SSL_CERT_FILE, NODE_EXTRA_CA_CERTS and
// DENO_CERT (Go reads SSL_CERT_FILE and SSL_CERT_DIR itself on Linux, never on macOS or Windows). The call has 120 s to
// get the provider's answer started (TIMEOUT_MS; the body then has no limit, as in JS); a connection has 10 s (undici's
// connect timeout). A redirect is followed (at most 5, same method and body) only to the same origin.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // the body goes to the client as the provider wrote it, as fetch gives it
		ForceAttemptHTTP2:   true,
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

// Call sends the plan's request. ctx ends when the client goes away.
func (u *Upstream) Call(ctx context.Context, p *Plan) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(u.timeout, func() { timedOut.Store(true); cancel() })
	current := p.URL
	for hop := 0; hop <= maxRedirects; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, current, bytes.NewReader(p.Body))
		if err != nil {
			timer.Stop()
			return Result{Why: "fetch failed"}
		}
		req.Header = p.Header.Clone()
		resp, err := u.client.Do(req)
		if err != nil {
			timer.Stop()
			if timedOut.Load() {
				return Result{Why: "timed out"}
			}
			return Result{Why: ErrorCode(err)}
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			timer.Stop() // the answer has started: the body has no time limit, as in JS
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil && ctx.Err() != nil {
				return Result{Status: resp.StatusCode, Body: body, Why: "client gone"}
			}
			return Result{Status: resp.StatusCode, Body: body}
		}
		_ = resp.Body.Close()
		from, _ := url.Parse(current)
		next, err := from.Parse(loc)
		if err != nil {
			timer.Stop()
			return Result{Why: "fetch failed"}
		}
		if origin(next) != origin(from) {
			timer.Stop()
			return Result{Why: "refused a redirect from " + from.Host + " to " + next.Host +
				"; credentials are not forwarded to another host"}
		}
		current = next.String()
	}
	timer.Stop()
	return Result{Why: "too many redirects"}
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return u.Scheme + "://" + u.Hostname() + ":" + port
}

// ErrorCode is what the JS gateway names a failed fetch by (err.cause.code of Node's fetch, else its message): the
// common system and undici codes, else "fetch failed". Deno names some of these differently; a failed call has no
// provider answer, so the shadow compare never reads it.
func ErrorCode(err error) string {
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

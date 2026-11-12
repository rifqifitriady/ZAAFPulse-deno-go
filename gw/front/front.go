// Package front is the gateway's front door (P4a, docs/superpowers/specs/2026-11-12-p4a-go-front-design.md): it holds
// the main port (TLS when configured) and forwards every request to the control plane's internal port on 127.0.0.1,
// each one with an X-Zp-Client header that names the client (D3, header design; client.go). In P4a every route class is
// on the js engine, so everything is forwarded.
package front

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
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
}

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
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Upstream))}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			// Rewrite drops these four from Out before it runs. The control plane reads them as the client sent them
			// (its proxy checks look for their presence), so they go through unchanged; the front adds none.
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
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
		ErrorLog:      logger,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the client is gone
			}
			logger.Printf("front: %s %s: %v", r.Method, r.URL.Path, err)
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
		ErrorLog:          logger,
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
	if s.engine(Classify(r.URL.Path)) != "js" {
		// Unreachable in P4a: config.push refuses a go engine. P4b answers here.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"the go engine is not available in this gateway"}`))
		return
	}
	s.proxy.ServeHTTP(w, r)
}

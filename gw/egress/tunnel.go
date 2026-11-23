package egress

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// tunnel is CONNECT host:port (raw TCP, E11: Postgres; TLS stays end to end in the caller): the allow-list checked, the
// target dialled within the connect limit (no proxy, as net.connect), "200 Connection Established", bytes copied both
// ways until both sides are done, one Record (scheme tcp, via tunnel) at close with both byte counts.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	purpose := r.Header.Get("X-Zp-Egress-Purpose")
	if purpose == "" {
		purpose = unspecified
	}
	host, ps, err := net.SplitHostPort(r.Host)
	port, perr := strconv.Atoi(ps)
	if err != nil || perr != nil || host == "" || port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "CONNECT takes host:port")
		return
	}
	host = normHost(host)
	start := s.now()
	rec := Record{TS: start.UnixMilli(), Purpose: purpose, Method: http.MethodConnect, Scheme: "tcp", Host: host,
		Port: port, Via: ViaTunnel}
	if IsMetadataHost(host) { // every mode, whatever the allow-list lists (P7 review M1)
		rec.Outcome, rec.Error = OutcomeRefused, CodePrivate
		s.emit(rec)
		writeError(w, http.StatusBadGateway, CodePrivate, metadataError{addr: net.JoinHostPort(host, ps)}.Error())
		return
	}
	if pol := s.policy.Load(); !pol.Allows(host, port) {
		if pol.Mode == ModeEnforce {
			rec.Outcome, rec.Error = OutcomeRefused, CodeRefused
			s.emit(rec)
			writeError(w, http.StatusBadGateway, CodeRefused,
				net.JoinHostPort(host, ps)+" is not on the egress allow-list")
			return
		}
		wr := rec
		wr.Outcome, wr.Error = OutcomeWouldRefuse, CodeRefused
		s.emit(wr)
	}
	conn, err := s.dial(r.Context(), "tcp", net.JoinHostPort(host, ps), false)
	if err != nil {
		code, message := errorCode(err)
		rec.Outcome, rec.Error, rec.MS = OutcomeError, code, ms(start)
		if isMetadataErr(err) {
			rec.Outcome = OutcomeRefused
		}
		s.emit(rec)
		writeError(w, http.StatusBadGateway, code, message)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = conn.Close()
		writeError(w, http.StatusInternalServerError, "EPROTO", "the connection cannot be taken over")
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		_ = conn.Close()
		return
	}
	if !s.track(client, conn) {
		_ = client.Close()
		_ = conn.Close()
		return
	}
	defer s.untrack(client, conn)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = conn.Close()
		return
	}
	var out, in atomic.Int64
	var wg sync.WaitGroup
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = conn.Close() }) }
	wg.Add(2)
	go func() { // caller to target: what the caller had already sent is in rw's buffer
		defer wg.Done()
		n, err := io.Copy(conn, rw.Reader)
		out.Store(n)
		if err != nil || closeWrite(conn) != nil {
			closeBoth()
		}
	}()
	go func() {
		defer wg.Done()
		n, err := io.Copy(client, conn)
		in.Store(n)
		if err != nil || closeWrite(client) != nil {
			closeBoth()
		}
	}()
	wg.Wait()
	closeBoth()
	rec.Outcome, rec.BytesOut, rec.BytesIn, rec.MS = OutcomeOK, out.Load(), in.Load(), ms(start)
	s.emit(rec)
}

// closeWrite half-closes a TCP connection (the other side reads EOF and may still answer).
func closeWrite(c net.Conn) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

// track registers a tunnel's connections so Close can end them; false once the server is closed.
func (s *Server) track(cs ...net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, c := range cs {
		s.tunnels[c] = struct{}{}
	}
	return true
}

func (s *Server) untrack(cs ...net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cs {
		delete(s.tunnels, c)
	}
}

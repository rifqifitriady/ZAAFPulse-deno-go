package egress

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxRedirects is fetch's redirect limit: the 21st redirect fails.
const maxRedirects = 20

// call is one request to the endpoint, from its X-ZP-Egress-* headers.
type call struct {
	url      *url.URL
	purpose  string
	redirect string // follow | manual | error
	public   bool
}

// parseCall reads the X-ZP-Egress-* headers; a non-empty message is a bad request.
func parseCall(r *http.Request) (*call, string) {
	raw := r.Header.Get("X-Zp-Egress-Url")
	if raw == "" {
		return nil, "X-ZP-Egress-URL is missing"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "X-ZP-Egress-URL is not an absolute URL"
	}
	if msg := checkTarget(u, "X-ZP-Egress-URL"); msg != "" {
		return nil, msg
	}
	c := &call{url: u, purpose: r.Header.Get("X-Zp-Egress-Purpose"), redirect: "follow",
		public: r.Header.Get("X-Zp-Egress-Public") == "1"}
	if c.purpose == "" {
		c.purpose = unspecified
	}
	switch m := strings.ToLower(r.Header.Get("X-Zp-Egress-Redirect")); m {
	case "":
	case "follow", "manual", "error":
		c.redirect = m
	default:
		return nil, "X-ZP-Egress-Redirect must be follow, manual or error"
	}
	return c, ""
}

// checkTarget is the one check every URL the endpoint fetches passes, the caller's and every redirect target: absolute,
// http or https, no user name or password, a host and a port 1..65535. It lower-cases the scheme and the host in place,
// as a WHATWG URL holds them (the record's host, the final URL). A non-empty message names what is wrong with it.
func checkTarget(u *url.URL, what string) string {
	if !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return what + " is not an absolute URL"
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "only http:// and https:// URLs can be fetched"
	}
	if u.User != nil {
		return "URLs with a user name or password are not fetched"
	}
	if host, port := target(u); host == "" || port < 1 || port > 65535 {
		return what + " has no valid host and port"
	}
	return ""
}

// writeError answers a failure: status, X-ZP-Egress-Error and a JSON {code, message}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	h := w.Header()
	h.Set("X-Zp-Egress-Error", code)
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"code": code, "message": message})
	_, _ = w.Write(body)
}

// hopHeaders are removed from both directions, with the headers Connection names and every Proxy-* header.
var hopHeaders = []string{"Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(k, "Proxy-") {
			delete(h, k)
		}
	}
}

// outboundHeader is the caller's headers as the target gets them: no X-ZP-Egress-*, no hop-by-hop, no
// Accept-Encoding (Go asks for gzip itself and decodes the answer), and no Go User-Agent when the caller sent none.
func outboundHeader(in http.Header) http.Header {
	h := in.Clone()
	stripHop(h)
	for k := range h {
		if strings.HasPrefix(k, "X-Zp-Egress-") {
			delete(h, k)
		}
	}
	h.Del("Accept-Encoding")
	h.Del("Host")
	if _, ok := h["User-Agent"]; !ok {
		h["User-Agent"] = []string{""} // net/http then sends none
	}
	return h
}

// bodyHeaders go with the body when a redirect turns the request into a GET (fetch's request-body-header names).
var bodyHeaders = []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Language",
	"Content-Location"}

func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	ah, ap := target(a)
	bh, bp := target(b)
	return a.Scheme == b.Scheme && ah == bh && ap == bp
}

// finalURL is the URL the caller's Response.url shows: without the fragment.
func finalURL(u *url.URL) string {
	c := *u
	c.Fragment, c.RawFragment = "", ""
	return c.String()
}

// verdict checks one hop before it is sent: the cloud metadata service is refused first, in every mode; in public mode an address literal that is private is refused (names are
// checked at connect) and the allow-list does not apply (any public address, Decision 13); otherwise the allow-list:
// a miss is refused in enforce mode and recorded "would-refuse" in report mode. A non-empty code refuses the hop.
func (s *Server) verdict(c *call, u *url.URL, rec Record) (code string, outcome string) {
	host, port := target(u)
	if IsMetadataHost(host) {
		return CodePrivate, OutcomeRefused // every mode, whatever the allow-list lists (P7 review M1)
	}
	if c.public {
		if ip := net.ParseIP(host); ip != nil && IsPrivate(ip) {
			return CodePrivate, OutcomeError
		}
		return "", ""
	}
	pol := s.policy.Load()
	if pol.Allows(host, port) {
		return "", ""
	}
	if pol.Mode == ModeEnforce {
		return CodeRefused, OutcomeRefused
	}
	wr := rec
	wr.Outcome, wr.Error = OutcomeWouldRefuse, CodeRefused
	s.emit(wr)
	return "", ""
}

func refusal(code string, u *url.URL) string {
	host, port := target(u)
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	if IsMetadataHost(host) {
		return metadataError{addr: hp}.Error()
	}
	if code == CodePrivate {
		return hp + " is a private or local address; it is not reached for a public URL"
	}
	return hp + " is not on the egress allow-list"
}

func (s *Server) newRecord(purpose, method string, u *url.URL, via string, start time.Time) Record {
	host, port := target(u)
	return Record{TS: start.UnixMilli(), Purpose: purpose, Method: method, Scheme: u.Scheme, Host: host, Port: port,
		Via: via}
}

// forward is "/": the request in X-ZP-Egress-URL made with the caller's method, headers and body (streamed), the
// answer streamed back flushed per read; redirects as fetch follows them, each hop checked and recorded.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	c, msg := parseCall(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	// The request body may still be arriving while the answer streams back (both directions at once).
	_ = http.NewResponseController(w).EnableFullDuplex()
	rt := s.plain
	if c.public {
		rt = s.public
	}
	header := outboundHeader(r.Header)
	method := r.Method
	src := newReplay(r.Body, r.ContentLength, s.replayLimit(c))
	body := src.first()
	cur := c.url
	redirects := 0
	fail := func(rec Record, status int, code, message string) {
		rec.Outcome, rec.Error = OutcomeError, code
		if r.Context().Err() != nil {
			rec.Error = CodeAborted
		}
		s.emit(rec)
		writeError(w, status, code, message)
	}
	for {
		start := s.now()
		rec := s.newRecord(c.purpose, method, cur, ViaProxy, start)
		if code, outcome := s.verdict(c, cur, rec); code != "" {
			rec.Outcome, rec.Error = outcome, code
			s.emit(rec)
			writeError(w, http.StatusBadGateway, code, refusal(code, cur))
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), method, cur.String(), nil)
		if err != nil {
			fail(rec, http.StatusBadRequest, CodeBadRequest, "the request could not be built: "+err.Error())
			return
		}
		req.Header = header.Clone()
		if body != nil {
			req.Body, req.ContentLength = body, body.length
		}
		resp, err := rt.RoundTrip(req)
		if body != nil {
			rec.BytesOut = body.n.Load()
		}
		if err != nil {
			code, message := errorCode(err)
			rec.MS = ms(start)
			if isMetadataErr(err) { // a name that resolved to the metadata service: refused, as a literal is
				rec.Outcome, rec.Error = OutcomeRefused, code
				s.emit(rec)
				writeError(w, http.StatusBadGateway, code, message)
				return
			}
			fail(rec, http.StatusBadGateway, code, message)
			return
		}
		if resp.StatusCode < 100 || resp.StatusCode > 999 {
			// No HTTP status (net/http reads any three digits): no answer to pass on, and none to record.
			_ = resp.Body.Close()
			rec.MS = ms(start)
			fail(rec, http.StatusBadGateway, "EPROTO", "the target answered status "+strconv.Itoa(resp.StatusCode)+
				", which is not an HTTP status")
			return
		}
		rec.Status = resp.StatusCode
		if isRedirect(resp.StatusCode) && c.redirect != "manual" {
			loc := resp.Header.Get("Location")
			if c.redirect == "error" {
				_ = resp.Body.Close()
				rec.MS = ms(start)
				fail(rec, http.StatusBadGateway, CodeRedirect, "the target answered a redirect and redirects are refused")
				return
			}
			if loc != "" {
				_ = resp.Body.Close()
				rec.Outcome, rec.MS = OutcomeOK, ms(start)
				if body != nil {
					rec.BytesOut = body.n.Load()
				}
				s.emit(rec)
				next, err := cur.Parse(loc)
				if err != nil {
					writeError(w, http.StatusBadGateway, CodeRedirect, "the target redirected to an URL that cannot be fetched")
					return
				}
				if msg := checkTarget(next, "the redirect target"); msg != "" {
					writeError(w, http.StatusBadGateway, CodeRedirect, "the target redirected to an URL that cannot be fetched: "+msg)
					return
				}
				if redirects >= maxRedirects {
					writeError(w, http.StatusBadGateway, CodeRedirects, "too many redirects")
					return
				}
				redirects++
				st := resp.StatusCode
				// WHATWG fetch: 301/302 with POST, and 303 with any method but GET and HEAD, become a GET without a body.
				if (st == 303 && method != http.MethodGet && method != http.MethodHead) ||
					((st == 301 || st == 302) && method == http.MethodPost) {
					method = http.MethodGet
					body = nil
					src.drop()
					for _, k := range bodyHeaders {
						header.Del(k)
					}
				} else if body != nil {
					body.close()
					if body, err = src.again(); err != nil {
						writeError(w, http.StatusBadGateway, CodeRedirect, err.Error())
						return
					}
				}
				if !sameOrigin(cur, next) {
					header.Del("Authorization")
				}
				cur = next
				continue
			}
		}
		s.answer(w, r, resp, rec, start, body, cur, redirects > 0)
		return
	}
}

// replayLimit is how much of the caller's body is kept for a 307/308 replay: only a followed redirect sends it again
// (manual and error never do), so nothing is kept otherwise.
func (s *Server) replayLimit(c *call) int {
	if c.redirect != "follow" {
		return 0
	}
	return s.maxReplay
}

// answer streams the target's answer back: its status and headers (hop-by-hop removed) plus X-ZP-Egress-URL and
// X-ZP-Egress-Redirected, the body flushed after every read. A body that fails halfway aborts the caller's
// connection, so the caller sees the failure rather than a short body.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, resp *http.Response, rec Record, start time.Time,
	body *hopBody, u *url.URL, redirected bool) {
	defer resp.Body.Close()
	h := w.Header()
	for k, v := range resp.Header {
		h[k] = v
	}
	stripHop(h)
	// The X-ZP-Egress-* answer headers are the endpoint's own: a target's are dropped, so it cannot fake a failure,
	// the final URL or a redirect.
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "x-zp-egress-") {
			delete(h, k)
		}
	}
	h.Set("X-Zp-Egress-Url", finalURL(u))
	if redirected {
		h.Set("X-Zp-Egress-Redirected", "1")
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	rec.Outcome = OutcomeOK
	buf := make([]byte, 32<<10)
	var readErr error
	written := true
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			rec.BytesIn += int64(n)
			if _, werr := w.Write(buf[:n]); werr != nil {
				written = false
				break // the caller left; r.Context() cancels the target request
			}
			_ = rc.Flush()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	if body != nil {
		rec.BytesOut = body.n.Load()
	}
	if readErr != nil {
		rec.Outcome = OutcomeError
		rec.Error, _ = errorCode(readErr)
	}
	if !written || r.Context().Err() != nil {
		rec.Outcome, rec.Error = OutcomeError, CodeAborted
	}
	rec.MS = ms(start)
	s.emit(rec)
	if readErr != nil && r.Context().Err() == nil {
		panic(http.ErrAbortHandler)
	}
}

// check is "/check": the verdict for the call the headers describe (the allow-list, or the address literal in public
// mode), recorded "checked" or "would-refuse" with via "check" (the control plane's gate counts only these), answered 204 or 403 with X-ZP-Egress-Error; nothing is connected and
// the body is not read. The call's method comes from X-ZP-Egress-Method (default GET).
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeBadRequest, "/check takes POST")
		return
	}
	c, msg := parseCall(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	method := strings.ToUpper(r.Header.Get("X-Zp-Egress-Method"))
	if method == "" {
		method = http.MethodGet
	}
	rec := s.newRecord(c.purpose, method, c.url, ViaCheck, s.now())
	host, port := target(c.url)
	code := ""
	if IsMetadataHost(host) {
		code = CodePrivate // every mode (P7 review M1)
	} else if c.public {
		if ip := net.ParseIP(host); ip != nil && IsPrivate(ip) {
			code = CodePrivate
		}
	} else if !s.policy.Load().Allows(host, port) {
		code = CodeRefused
	}
	if code == "" {
		rec.Outcome = OutcomeChecked
		s.emit(rec)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rec.Outcome, rec.Error = OutcomeWouldRefuse, code
	s.emit(rec)
	writeError(w, http.StatusForbidden, code, refusal(code, c.url))
}

// replay is the caller's request body: streamed to the first hop and kept (up to a limit) to send again when a
// 307/308 redirect asks for the same body.
type replay struct {
	mu     sync.Mutex // held during a read from src
	src    io.Reader
	length int64 // the caller's Content-Length, -1 unknown (chunked)
	buf    []byte
	over   bool
	limit  int
	none   bool
}

func newReplay(src io.Reader, length int64, limit int) *replay {
	return &replay{src: src, length: length, limit: limit, none: src == nil || src == http.NoBody || length == 0}
}

func (p *replay) keep(b []byte) {
	if p.over {
		return
	}
	if len(p.buf)+len(b) > p.limit {
		p.over, p.buf = true, nil
		return
	}
	p.buf = append(p.buf, b...)
}

// first is the first hop's body, nil when the caller sent none.
func (p *replay) first() *hopBody {
	if p.none {
		return nil
	}
	return &hopBody{mu: &p.mu, r: p.src, keep: p.keep, length: p.length}
}

// again reads what the caller has not sent yet and gives the whole body again.
func (p *replay) again() (*hopBody, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.src != nil {
		chunk := make([]byte, 32<<10)
		for {
			n, err := p.src.Read(chunk)
			p.keep(chunk[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, errReplay("the request body could not be read again for the redirect: " + err.Error())
			}
		}
		p.src = nil
	}
	if p.over {
		return nil, errReplay("the request body is too large to send again on a redirect")
	}
	return &hopBody{mu: &sync.Mutex{}, r: bytes.NewReader(p.buf), length: int64(len(p.buf))}, nil
}

func (p *replay) drop() {
	p.mu.Lock()
	p.buf, p.over = nil, true
	p.mu.Unlock()
}

type errReplay string

func (e errReplay) Error() string { return string(e) }

// hopBody is one hop's request body: counted, and closed for that hop only (the transport may close it from another
// goroutine; the caller's body stays open for a replay).
type hopBody struct {
	mu     *sync.Mutex
	r      io.Reader
	keep   func([]byte)
	length int64
	closed atomic.Bool
	n      atomic.Int64
}

func (b *hopBody) Read(p []byte) (int, error) {
	if b.closed.Load() {
		return 0, http.ErrBodyReadAfterClose
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() || b.r == nil {
		return 0, http.ErrBodyReadAfterClose
	}
	n, err := b.r.Read(p)
	if b.keep != nil {
		b.keep(p[:n])
	}
	b.n.Add(int64(n))
	return n, err
}

func (b *hopBody) Close() error {
	b.closed.Store(true)
	return nil
}

func (b *hopBody) close() { b.closed.Store(true) }

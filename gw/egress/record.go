package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Record is one outbound connection, the body of an egress.event (ipc/v1/egress.event.schema.json). It never holds a
// path, a query or a header (a key may sit in a query). Bytes are body bytes as the caller sent and read them (a gzip
// answer counts decoded). Status 0 means no answer (omitted in JSON).
type Record struct {
	TS       int64   `json:"ts"` // ms since the epoch, when the connection was asked for
	Purpose  string  `json:"purpose"`
	Method   string  `json:"method"`
	Scheme   string  `json:"scheme"` // http | https | tcp
	Host     string  `json:"host"`   // lower case, IPv6 without brackets
	Port     int     `json:"port"`
	Status   int     `json:"status,omitempty"`
	Outcome  string  `json:"outcome"` // ok | error | refused | would-refuse | checked
	Error    string  `json:"error,omitempty"`
	BytesOut int64   `json:"bytesOut"`
	BytesIn  int64   `json:"bytesIn"`
	MS       float64 `json:"ms"`
	Via      string  `json:"via"` // proxy | tunnel | engine
}

// Outcomes and vias.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeRefused     = "refused"
	OutcomeWouldRefuse = "would-refuse"
	OutcomeChecked     = "checked"
	ViaProxy           = "proxy"
	ViaTunnel          = "tunnel"
	ViaEngine          = "engine"
)

// Error codes (X-ZP-Egress-Error and Record.Error).
const (
	CodeRefused    = "EGRESS_REFUSED"
	CodePrivate    = "EGRESS_PRIVATE"
	CodeRedirects  = "EGRESS_REDIRECTS"
	CodeRedirect   = "EGRESS_REDIRECT"
	CodeBadRequest = "EGRESS_BAD_REQUEST"
	// CodeAborted is recorded (never answered) when the caller went away before the call ended.
	CodeAborted = "ABORTED"
)

// unspecified is the purpose of a request that named none (the record's purpose is never empty).
const unspecified = "unspecified"

func ms(since time.Time) float64 { return float64(time.Since(since).Microseconds()) / 1000 }

// target names a URL's destination as a record does: lower-case host without brackets, port with the scheme's
// default.
func target(u *url.URL) (host string, port int) {
	host = strings.ToLower(u.Hostname())
	port, _ = strconv.Atoi(u.Port())
	if u.Port() == "" {
		port = map[string]int{"http": 80, "https": 443}[u.Scheme]
	}
	return host, port
}

var winsock = map[syscall.Errno]string{10061: "ECONNREFUSED", 10054: "ECONNRESET", 10060: "ETIMEDOUT",
	10053: "ECONNRESET", 10065: "EHOSTUNREACH", 10051: "ENETUNREACH"}

// errorCode names a failed connection or exchange by the code a fetch caller reads in err.cause.code: ECONNREFUSED,
// ENOTFOUND, ETIMEDOUT, ECONNRESET, CERT_INVALID, EHOSTUNREACH, ENETUNREACH, EGRESS_PRIVATE, else EPROTO; and a
// message without the URL.
func errorCode(err error) (code, message string) {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	message = err.Error()
	var priv privateError
	var dns *net.DNSError
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	var errno syscall.Errno
	var ne net.Error
	switch {
	case errors.As(err, &priv):
		return CodePrivate, priv.Error()
	case errors.As(err, &dns):
		if dns.IsTimeout {
			return "ETIMEDOUT", message
		}
		return "ENOTFOUND", message
	case errors.As(err, &cv), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci):
		return "CERT_INVALID", message
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED", message
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "ECONNRESET", message
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH", message
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH", message
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "ETIMEDOUT", message
	case errors.As(err, &errno):
		if name, ok := winsock[errno]; ok {
			return name, message
		}
	}
	return "EPROTO", message
}

// RecordingTransport records every round trip of rt (the engine's own provider and token calls, spec Decision 9) as a
// Record with via "engine": one per round trip, emitted when the answer's body ends (EOF, an error or Close), or at
// once when the round trip fails. Bytes are counted as the bodies are read.
func RecordingTransport(rt http.RoundTripper, purpose string, record func(Record)) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if purpose == "" {
		purpose = unspecified
	}
	return &recordingTransport{rt: rt, purpose: purpose, record: record}
}

type recordingTransport struct {
	rt      http.RoundTripper
	purpose string
	record  func(Record)
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	host, port := target(req.URL)
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	rec := Record{TS: start.UnixMilli(), Purpose: t.purpose, Method: method, Scheme: req.URL.Scheme, Host: host,
		Port: port, Via: ViaEngine}
	out := &counter{}
	sent := req
	if req.Body != nil && req.Body != http.NoBody {
		sent = req.Clone(req.Context())
		sent.Body = &countingBody{ReadCloser: req.Body, n: out}
	}
	resp, err := t.rt.RoundTrip(sent)
	if err != nil {
		rec.Outcome = OutcomeError
		rec.Error, _ = errorCode(err)
		if req.Context().Err() != nil {
			rec.Error = CodeAborted
		}
		rec.BytesOut = out.Load()
		rec.MS = ms(start)
		t.emit(rec)
		return nil, err
	}
	rec.Status = resp.StatusCode
	rec.Outcome = OutcomeOK
	rb := &recordedBody{ReadCloser: resp.Body}
	rb.done = func(readErr error) {
		rec.BytesOut = out.Load()
		rec.BytesIn = rb.n.Load()
		if readErr != nil {
			rec.Outcome = OutcomeError
			rec.Error, _ = errorCode(readErr)
			if req.Context().Err() != nil {
				rec.Error = CodeAborted
			}
		}
		rec.MS = ms(start)
		t.emit(rec)
	}
	resp.Body = rb
	return resp, nil
}

func (t *recordingTransport) emit(r Record) {
	if t.record != nil {
		t.record(r)
	}
}

type counter = atomic.Int64

// countingBody counts the bytes read through it.
type countingBody struct {
	io.ReadCloser
	n *counter
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// recordedBody counts an answer's body and calls done once, at EOF, at a read error or at Close.
type recordedBody struct {
	io.ReadCloser
	n    counter
	once sync.Once
	done func(readErr error)
}

func (b *recordedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	switch {
	case err == io.EOF:
		b.once.Do(func() { b.done(nil) })
	case err != nil:
		b.once.Do(func() { b.done(err) })
	}
	return n, err
}

func (b *recordedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.done(nil) })
	return err
}

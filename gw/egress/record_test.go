package egress

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRecordingTransport(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, "got %d bytes", len(b))
	}))
	defer ts.Close()
	rec := newRecorder()
	client := &http.Client{Transport: RecordingTransport(&http.Transport{}, "gateway", nil, rec.add)}
	resp, err := client.Post(ts.URL+"/v1/chat?key=secret", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	rec.none(t, 50*time.Millisecond) // not before the body ends
	body := readAll(t, resp)
	r := rec.wait(t, 1)[0]
	port, _ := strconv.Atoi(portOf(t, ts.URL))
	want := Record{TS: r.TS, Purpose: "gateway", Method: "POST", Scheme: "http", Host: "127.0.0.1", Port: port,
		Status: 200, Outcome: "ok", BytesOut: 7, BytesIn: int64(len(body)), MS: r.MS, Via: "engine"}
	if r != want || body != "got 7 bytes" {
		t.Fatalf("record %+v\nwant   %+v (%q)", r, want, body)
	}
	// Closing without reading still records, once.
	resp, err = client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	if r := rec.wait(t, 1)[0]; r.Method != "GET" || r.BytesOut != 0 {
		t.Fatalf("record %+v", r)
	}
	rec.none(t, 50*time.Millisecond)
	// A failed round trip is recorded with its code.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, err := client.Get("http://" + addr + "/"); err == nil {
		t.Fatal("no error")
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != "error" || r.Error != "ECONNREFUSED" || r.Status != 0 {
		t.Fatalf("record %+v", r)
	}
}

func TestErrorCode(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
	for want, err := range map[string]error{
		"ECONNREFUSED":   &url.Error{Op: "Get", URL: "http://x/?k=s", Err: op(syscall.ECONNREFUSED)},
		"ECONNRESET":     op(syscall.ECONNRESET),
		"ETIMEDOUT":      timeout,
		"ENOTFOUND":      &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true},
		"CERT_INVALID":   x509.UnknownAuthorityError{},
		"EGRESS_PRIVATE": op(privateError{addr: "10.0.0.1"}),
		"EHOSTUNREACH":   op(syscall.EHOSTUNREACH),
		"EPROTO":         errors.New("malformed HTTP response"),
	} {
		code, msg := errorCode(err)
		if code != want {
			t.Errorf("%v: %s, want %s", err, code, want)
		}
		if strings.Contains(msg, "k=s") {
			t.Errorf("message holds the URL: %s", msg)
		}
	}
	if code, _ := errorCode(io.ErrUnexpectedEOF); code != "ECONNRESET" {
		t.Error(code)
	}
	if code, _ := errorCode(context.DeadlineExceeded); code != "ETIMEDOUT" {
		t.Error(code)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestRecordingTransportPolicy: the engine's own calls are checked against the allow-list (spec Decision 13): enforce
// refuses before anything is sent (record "refused", a *RefusedError naming EGRESS_REFUSED), report records
// "would-refuse" and the call goes on; a listed host and a nil policy pass with one record.
func TestRecordingTransportPolicy(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()
	rec := newRecorder()
	var pol *Policy
	client := &http.Client{Transport: RecordingTransport(&http.Transport{}, "gateway",
		func() *Policy { return pol }, rec.add)}
	port, _ := strconv.Atoi(portOf(t, ts.URL))

	pol = &Policy{Mode: ModeEnforce, Allow: []Rule{{Host: "api.example.com"}}}
	_, err := client.Post(ts.URL+"/v1", "application/json", strings.NewReader(`{}`))
	var re *RefusedError
	if err == nil || !errors.As(err, &re) || !strings.Contains(err.Error(), "EGRESS_REFUSED") ||
		re.EgressCode() != CodeRefused || re.Port != port {
		t.Fatalf("enforce: err %v", err)
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != OutcomeRefused || r.Error != CodeRefused || r.Host != "127.0.0.1" ||
		r.Port != port || r.Via != ViaEngine || r.Status != 0 {
		t.Fatalf("enforce record %+v", r)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a refused call reached the target %d times", n)
	}

	pol = &Policy{Mode: ModeReport, Allow: []Rule{{Host: "api.example.com"}}}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if rs := rec.wait(t, 2); rs[0].Outcome != OutcomeWouldRefuse || rs[0].Error != CodeRefused ||
		rs[1].Outcome != OutcomeOK || rs[1].Status != 200 {
		t.Fatalf("report records %+v", rs)
	}

	for name, p := range map[string]*Policy{"listed": {Mode: ModeEnforce, Allow: []Rule{{Host: "127.0.0.1"}}},
		"nil": nil} {
		pol = p
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		readAll(t, resp)
		if r := rec.wait(t, 1)[0]; r.Outcome != OutcomeOK {
			t.Fatalf("%s: record %+v", name, r)
		}
		rec.none(t, 30*time.Millisecond)
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("hits %d", n)
	}
}

// getBodyOnly is a transport that sends the body it asks again through GetBody (as net/http does for a retry).
type getBodyOnly struct{}

func (getBodyOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	_ = req.Body.Close()
	b, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, b)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Request: req,
		Header: http.Header{}}, nil
}

func TestRecordingTransportCountsARetriedBody(t *testing.T) {
	rec := newRecorder()
	client := &http.Client{Transport: RecordingTransport(getBodyOnly{}, "gateway", nil, rec.add)}
	resp, err := client.Post("http://api.example.com/v1", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if r := rec.wait(t, 1)[0]; r.BytesOut != 7 {
		t.Fatalf("bytesOut %d, want 7 (the body sent again through GetBody)", r.BytesOut)
	}
}

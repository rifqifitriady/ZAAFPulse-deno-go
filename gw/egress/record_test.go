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
	client := &http.Client{Transport: RecordingTransport(&http.Transport{}, "gateway", rec.add)}
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

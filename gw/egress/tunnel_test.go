package egress

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openTunnel sends CONNECT for hostport and returns the connection and the status line.
func openTunnel(t *testing.T, base, hostport, token string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/"))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, "CONNECT "+hostport+" HTTP/1.1\r\nHost: "+hostport+"\r\nX-ZP-Egress-Token: "+token+
		"\r\nX-ZP-Egress-Purpose: brain:postgres\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp.Status + "|" + resp.Header.Get("X-ZP-Egress-Error")
}

func echoServer(t *testing.T) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.(*net.TCPConn).CloseWrite()
				_ = c.Close()
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestTunnelEcho(t *testing.T) {
	ln := echoServer(t)
	_, base, rec := startEgress(t, allowAll)
	port := ln.Addr().(*net.TCPAddr).Port
	c, br, status := openTunnel(t, base, "db.test:"+strconv.Itoa(port), testToken)
	defer c.Close()
	if status != "200 Connection Established|" {
		t.Fatalf("status %q", status)
	}
	_, _ = io.WriteString(c, "hello tunnel")
	buf := make([]byte, len("hello tunnel"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello tunnel" {
		t.Fatalf("echo %q %v", buf, err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("rest %q %v", rest, err)
	}
	r := rec.wait(t, 1)[0]
	want := Record{TS: r.TS, Purpose: "brain:postgres", Method: "CONNECT", Scheme: "tcp", Host: "db.test", Port: port,
		Outcome: "ok", BytesOut: 12, BytesIn: 12, MS: r.MS, Via: "tunnel"}
	if r != want {
		t.Fatalf("record %+v\nwant   %+v", r, want)
	}
}

func TestTunnelRefusals(t *testing.T) {
	ln := echoServer(t)
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	s, base, rec := startEgress(t, Policy{Mode: ModeEnforce, Allow: []Rule{{Host: "db.test", Port: 5432}}})
	c, _, status := openTunnel(t, base, "db.test:"+port, testToken)
	_ = c.Close()
	if status != "502 Bad Gateway|EGRESS_REFUSED" {
		t.Fatalf("enforce: %q", status)
	}
	if r := rec.wait(t, 1)[0]; r.Outcome != "refused" || r.Scheme != "tcp" || r.Via != "tunnel" {
		t.Fatalf("record %+v", r)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	cport := strconv.Itoa(closed.Addr().(*net.TCPAddr).Port)
	_ = closed.Close()
	s.SetPolicy(Policy{Mode: ModeReport})
	c, _, status = openTunnel(t, base, "127.0.0.1:"+cport, testToken)
	_ = c.Close()
	if status != "502 Bad Gateway|ECONNREFUSED" {
		t.Fatalf("refused port: %q", status)
	}
	if recs := rec.wait(t, 2); recs[0].Outcome != "would-refuse" || recs[1].Error != "ECONNREFUSED" {
		t.Fatalf("records %+v", recs)
	}
	c, _, status = openTunnel(t, base, "gone.invalid:5432", testToken)
	_ = c.Close()
	if status != "502 Bad Gateway|ENOTFOUND" {
		t.Fatalf("unknown name: %q", status)
	}
}

func TestCloseEndsTunnels(t *testing.T) {
	ln := echoServer(t)
	s, base, rec := startEgress(t, allowAll)
	c, br, status := openTunnel(t, base, "127.0.0.1:"+strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), testToken)
	defer c.Close()
	if !strings.HasPrefix(status, "200") {
		t.Fatal(status)
	}
	s.Close(10 * time.Millisecond)
	if _, err := io.ReadAll(br); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatal(err)
	}
	rec.wait(t, 1)
}

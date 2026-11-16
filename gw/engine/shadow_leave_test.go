package engine

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Final review I1: a client that leaves a shadowed stream makes the front's ReverseProxy panic with
// http.ErrAbortHandler; the shadow slot it held must still be released (and a "skipped" line posted), or after 8 such
// leaves the shadow stops comparing anything. More leaves than slots, then a whole shadowed stream is still compared.
func TestShadowSlotReleasedWhenTheClientLeaves(t *testing.T) {
	g := newStreamRig(t, http.NotFoundHandler()) // the engine's state; the control plane below answers
	g.e.SetModes(func(sub string) (bool, bool) { return false, sub == "chat-stream" })
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 20; i++ {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(control.Close)
	cu, _ := url.Parse(control.URL)
	proxy := httputil.NewSingleHostReverseProxy(cu)
	proxy.FlushInterval = -1
	lines := make(chan string, 64)
	own := func(method, path string, body []byte) (int, []byte, error) {
		if method == http.MethodPost {
			lines <- string(body)
		}
		return http.StatusNotFound, nil, nil
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.e.Shadow("chat", w, r, "/v1/chat/completions", proxy, own)
	}))
	t.Cleanup(gw.Close)
	post := func(ctx context.Context) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/chat/completions", strings.NewReader(streamBody))
		req.Header.Set("Authorization", g.key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	const leaves = 12 // more than the 8 slots
	for i := 0; i < leaves; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		resp := post(ctx)
		_, _ = bufio.NewReader(resp.Body).ReadString('\n')
		cancel()
		_ = resp.Body.Close()
		select {
		case l := <-lines:
			if !strings.Contains(l, `"class":"chat-stream"`) || !strings.Contains(l, `"outcome":"skipped"`) {
				t.Fatalf("leave %d: %s", i, l)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("leave %d: no shadow line (slots held: %d)", i, len(shadowSlots))
		}
	}
	end := time.Now().Add(5 * time.Second)
	for len(shadowSlots) > 0 && time.Now().Before(end) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(shadowSlots); n > 0 {
		t.Fatalf("%d shadow slots still held after %d leaves", n, leaves)
	}
	resp := post(context.Background())
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	select {
	case l := <-lines:
		if !strings.Contains(l, `"class":"chat-stream"`) || strings.Contains(l, "the client left") {
			t.Fatalf("the whole stream's line: %s", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a whole shadowed stream was not compared after the leaves")
	}
}

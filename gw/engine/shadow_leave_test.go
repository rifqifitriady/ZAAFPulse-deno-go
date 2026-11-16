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

// Final review m1, m2: a request the engine hands off gets its hand-off line under the sub-class its body asks for
// (a stream: chat-stream), from Shadow and from Serve (chat shadowed while chat-stream is on go).
func TestHandoffLinesUnderTheirSubClass(t *testing.T) {
	g := newStreamRig(t, http.NotFoundHandler())
	lines := make(chan string, 8)
	own := func(method, path string, body []byte) (int, []byte, error) {
		if method == http.MethodPost {
			lines <- string(body)
		}
		return http.StatusNotFound, nil, nil
	}
	g.e.SetOwn(own)
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "js") })
	call := func(entry, body string) {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		r.Host, r.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:5000"
		r.Header.Set("Authorization", g.key)
		w := httptest.NewRecorder()
		if entry == "shadow" {
			g.e.Shadow("chat", w, r, "/v1/chat/completions", js, own)
		} else {
			g.e.Serve("chat", w, r, "/v1/chat/completions", js)
		}
		if w.Body.String() != "js" {
			t.Fatalf("%s %s: answered %q, not by the control plane", entry, body, w.Body.String())
		}
	}
	expect := func(what, class string) {
		t.Helper()
		select {
		case l := <-lines:
			if !strings.Contains(l, `"class":"`+class+`"`) || !strings.Contains(l, `"outcome":"handoff"`) {
				t.Fatalf("%s: %s", what, l)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no %s hand-off line", what, class)
		}
	}
	// m1: only chat-stream shadowed; a stream the engine would hand off (a string stream_options).
	g.e.SetModes(func(sub string) (bool, bool) { return false, sub == "chat-stream" })
	call("shadow", `{"model":"oa/m1","stream":true,"stream_options":"x","messages":[]}`)
	expect("m1 shadow", "chat-stream")
	// m2: chat on js and shadowed, chat-stream on go; a whole call the engine hands off (an unknown model).
	g.e.SetModes(func(sub string) (bool, bool) { return sub == "chat-stream", sub == "chat" })
	call("serve", `{"model":"zz/none","messages":[]}`)
	expect("m2 serve", "chat")
	// and a stream handed off while chat-stream is on go: no line (only a sub-class on js is shadowed).
	call("serve", `{"model":"oa/m1","stream":true,"stream_options":"x","messages":[]}`)
	select {
	case l := <-lines:
		t.Fatalf("a line for a sub-class on go: %s", l)
	case <-time.After(200 * time.Millisecond):
	}
}

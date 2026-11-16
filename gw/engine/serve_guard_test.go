package engine

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

var antKey = strings.Join([]string{"sk", "ant", "api03", "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"}, "-")

// guardRig is a stream rig whose global policy is edited, with the provider's requests recorded.
func guardRig(t *testing.T, edit func(*State)) (*streamRig, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	rig := newStreamRig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		if strings.Contains(string(b), `"stream":true`) {
			sseProvider([]string{"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", "data: [DONE]\n\n"}, nil)(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c1","usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	st := *rig.e.State()
	edit(&st)
	rig.e.SetState(&st)
	return rig, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

// A blocked call, whole or streamed, is a JSON 400 before any provider request, with one line.
func TestServeBlocksBeforeTheProvider(t *testing.T) {
	rig, sent := guardRig(t, func(s *State) { s.Gateway.Guardrails.Secrets = "block" })
	for _, stream := range []string{"false", "true"} {
		resp := rig.post(context.Background(), t, `{"model":"oa/m1","stream":`+stream+`,"messages":[{"role":"user","content":"key `+antKey+`"}]}`)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 400 || resp.Header.Get("Content-Type") != "application/json" ||
			!strings.Contains(string(b), `"code":"guardrail_blocked"`) {
			t.Fatalf("stream %s: %d %s %s", stream, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
		if l := rig.oneLine(t); l.Status != 400 || l.Guard == nil || l.Guard.Action != "blocked" {
			t.Fatalf("line %+v", l)
		}
	}
	if n := len(sent()); n != 0 {
		t.Fatalf("the provider got %d requests", n)
	}
}

// A redacted call sends the placeholder upstream, whole or streamed; the line says redacted.
func TestServeRedactsUpstream(t *testing.T) {
	rig, sent := guardRig(t, func(s *State) { s.Gateway.Guardrails.Secrets = "redact" })
	for i, stream := range []string{"false", "true"} {
		resp := rig.post(context.Background(), t, `{"model":"oa/m1","stream":`+stream+`,"messages":[{"role":"user","content":"key `+antKey+`"}]}`)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("stream %s: %d", stream, resp.StatusCode)
		}
		up := sent()[i]
		if strings.Contains(up, antKey) || !strings.Contains(up, "‹secret: Anthropic API key›") {
			t.Fatalf("upstream body %s", up)
		}
		if l := rig.oneLine(t); l.Guard == nil || l.Guard.Action != "redacted" {
			t.Fatalf("line %+v", l)
		}
	}
}

// Log-mode failures are logged once a minute per check, by name and count only.
func TestServeLogsFailuresOnce(t *testing.T) {
	was := checkBody
	checkBody = func(v jsv.Value, _ guard.Policy, _ string) (guard.Result, error) {
		return guard.Result{Body: v, Action: "logged", Hits: []guard.Hit{}, Error: true,
			Failures: []guard.Failure{{Check: "secrets", Count: 1, Err: "secret-detector-text"}}}, nil
	}
	t.Cleanup(func() { checkBody = was })
	rig, _ := guardRig(t, func(s *State) { s.Gateway.Guardrails.Secrets = "log" })
	var mu sync.Mutex
	var logs []string
	rig.e.logf = func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, f)
		mu.Unlock()
	}
	for range 2 {
		resp := rig.post(context.Background(), t, `{"model":"oa/m1","messages":[{"role":"user","content":"x"}]}`)
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		rig.oneLine(t)
	}
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "failed on %d string(s) in log mode") {
			n++
		}
		if strings.Contains(l, "secret-detector-text") {
			t.Fatal("the detector's text was logged")
		}
	}
	if n != 1 {
		t.Fatalf("%d failure log lines: %v", n, logs)
	}
}

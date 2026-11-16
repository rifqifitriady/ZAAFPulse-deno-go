package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// captureOf is the capture the control plane would have kept for a chat fixture (its provider request, the provider's
// answer, its own answer and the usage line with the cost in USD).
func captureOf(t *testing.T, c chatCase, want chatOut) *Capture {
	t.Helper()
	var u map[string]any
	_ = json.Unmarshal(want.Usage, &u)
	u["cost"] = u["cost"].(float64) / 1e9
	cap := &Capture{Usage: u}
	body := want.Request.Body
	cap.ProviderRequests = append(cap.ProviderRequests, struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    *string           `json:"body"`
	}{want.Request.URL, want.Request.Headers, &body})
	cap.ProviderResponse = &ProviderAnswer{Status: c.Provider.Status, Body: decodeUTF8([]byte(c.Provider.Body))}
	cap.Client = &struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}{want.Client.Status, want.Client.Body}
	return cap
}

// Every served chat fixture compares the same; a changed answer is a diff named without its text; a request the
// control plane served without a provider call, where the engine would have called one, is a diff too.
func TestShadowCompare(t *testing.T) {
	n := 0
	for _, f := range loadFixtures(t, "chat") {
		if f.Fn != "chat" {
			continue
		}
		var c chatCase
		var want chatOut
		_ = json.Unmarshal(f.In, &c)
		_ = json.Unmarshal(f.Out, &want)
		if c.Provider.Throw != "" {
			continue // no provider answer: nothing to compare
		}
		s := stateFor(t, c)
		p := s.PlanChat(requestFor(c), fixtureNow)
		if l := s.Compare("chat", &p, captureOf(t, c, want)); l.Outcome != "same" {
			t.Errorf("%s: %+v", f.Name, l)
		}
		n++
		if strings.HasSuffix(f.Name, "-basic.json") {
			cp := captureOf(t, c, want)
			cp.Client.Body = strings.Replace(cp.Client.Body, `"hi"`, `"a secret answer"`, 1)
			l := s.Compare("chat", &p, cp)
			b, _ := json.Marshal(l)
			if l.Outcome != "diff" || len(l.Diffs) != 1 || l.Diffs[0].Field != "client.body" || strings.Contains(string(b), "secret") {
				t.Errorf("changed answer: %s", b)
			}
			if l := s.Compare("chat", &p, nil); l.Outcome != "diff" || l.Diffs[0].Field != "served" {
				t.Errorf("no capture: %+v", l)
			}
			// The control plane made the provider call and it failed: nothing to compare.
			failed := captureOf(t, c, want)
			failed.ProviderResponse = nil
			if l := s.Compare("chat", &p, failed); l.Outcome != "skipped" {
				t.Errorf("failed call: %+v", l)
			}
			// Its own rate limit answered (each engine counts its own window).
			limited := captureOf(t, c, want)
			limited.ProviderRequests, limited.ProviderResponse, limited.Client.Status = nil, nil, 429
			if l := s.Compare("chat", &p, limited); l.Outcome != "skipped" {
				t.Errorf("rate limited: %+v", l)
			}
			// It refused what the engine would serve: a diff.
			limited.Client.Status = 403
			if l := s.Compare("chat", &p, limited); l.Outcome != "diff" || l.Diffs[0].Field != "served" {
				t.Errorf("refused: %+v", l)
			}
		}
	}
	if n < 15 {
		t.Fatalf("only %d fixtures compared", n)
	}
}

// Final review I3: when the engine would have answered 429 itself, the call compares nothing of the serving path. Both
// sides at 429 is "skipped" (not one of the owner's 20 compares); the server answering anything else is a diff.
func TestRateLimitedShadowLine(t *testing.T) {
	if l := rateLimitedLine("chat", 429); l.Outcome != "skipped" || l.Reason == "" {
		t.Errorf("429 both: %+v", l)
	}
	if l := rateLimitedLine("chat", 200); l.Outcome != "diff" || l.Diffs[0].Field != "rateLimit" {
		t.Errorf("server 200: %+v", l)
	}
}

// A stream capture is compared by replaying the provider's text: the same text gives "same"; a client text that
// differs is a diff named by length and hash; a truncated capture is skipped; a cut stream replays without End.
func TestCompareStream(t *testing.T) {
	var base chatCase
	for _, f := range loadFixtures(t, "chat") {
		if strings.HasSuffix(f.Name, "-stream-basic.json") {
			_ = json.Unmarshal(f.In, &base)
		}
	}
	s := stateFor(t, base)
	p := s.PlanChat(requestFor(base), fixtureNow)
	provider := `data: {"model":"m","choices":[{"delta":{"content":"a"}}]}` + "\n\n" + "data: {\"mo"
	hdr := map[string]string{}
	for k := range p.Header {
		hdr[strings.ToLower(k)] = p.Header.Get(k)
	}
	hb, _ := json.Marshal(hdr)
	client := `data: {"model":"oa/m1","choices":[{"delta":{"content":"a"}}]}` + "\n\n"
	mk := func(ended string, truncated bool, clientBody string) *Capture {
		c := &Capture{}
		_ = json.Unmarshal([]byte(`{"providerRequests":[{"url":`+mustQuote(p.URL)+`,"headers":`+string(hb)+`,"body":`+mustQuote(string(p.Body))+`}],
			"providerResponse":{"status":200,"body":`+mustQuote(provider)+`,"ended":"`+ended+`","truncated":`+fmt.Sprint(truncated)+`},
			"client":{"status":200,"body":`+mustQuote(clientBody)+`},
			"usage":{"keyId":"k1","provider":"oa","model":"oa/m1","kind":"chat","format":"openai","stream":true,"status":200,"input":0,"output":0,"cached":0,"local":true,"cost":0}}`), c)
		return c
	}
	if l := s.Compare("chat-stream", &p, mk("cut", false, client)); l.Outcome != "same" {
		t.Fatalf("%+v", l)
	}
	if l := s.Compare("chat-stream", &p, mk("cut", false, client+"x")); l.Outcome != "diff" || l.Diffs[0].Field != "client.body" {
		t.Fatalf("%+v", l)
	}
	if l := s.Compare("chat-stream", &p, mk("cut", true, client)); l.Outcome != "skipped" {
		t.Fatalf("%+v", l)
	}
}

func mustQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

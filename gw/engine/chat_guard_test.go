package engine

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

type guardCase struct {
	Config   ipc.ConfigPush      `json:"config"`
	Secrets  ipc.SecretsHandoff  `json:"secrets"`
	Snapshot *ipc.BudgetSnapshot `json:"snapshot"`
	Request  struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"provider"`
}

type guardOut struct {
	Request *struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Client struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

// loadGuardFixtures reads gw/testdata/chat-guard with the secrets put back (scripts/fixtures/lib.js).
func loadGuardFixtures(t *testing.T) map[string][2]json.RawMessage {
	t.Helper()
	dir := filepath.Join("..", "testdata", "chat-guard")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in %s (run node scripts/fixtures/gen-chat-guard.js): %v", dir, err)
	}
	out := map[string][2]json.RawMessage{}
	for _, f := range files {
		fx, err := jsv.LoadFixture(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		out[f.Name()] = [2]json.RawMessage{json.RawMessage(jsv.MustStringify(fx.In[0])), json.RawMessage(jsv.MustStringify(fx.Out))}
	}
	return out
}

func usageMap(rec ipc.UsageRecord) map[string]any {
	var m map[string]any
	b, _ := json.Marshal(rec)
	_ = json.Unmarshal(b, &m)
	delete(m, "ts")
	delete(m, "ms")
	delete(m, "ttft")
	return m
}

// Every case of gw/testdata/chat-guard (the JS gateway's own answers): the engine plans it without a hand-off, decides
// the budget, runs the checks, and sends the provider, answers the client and writes the line the JS did.
func TestChatGuardFixtures(t *testing.T) {
	for name, io := range loadGuardFixtures(t) {
		var c guardCase
		var want guardOut
		if err := json.Unmarshal(io[0], &c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := json.Unmarshal(io[1], &want); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c.Config.Gen, c.Secrets.Gen = 1, 1
		s := NewState(&c.Config, &c.Secrets).WithSnapshot(c.Snapshot)
		h := http.Header{}
		for k, v := range c.Request.Headers {
			h.Set(k, v)
		}
		r := &Request{Method: "POST", RawPath: "/v1/chat/completions", Header: h, Host: "127.0.0.1:4777",
			Peer: "127.0.0.1:50000", Body: []byte(c.Request.Body)}
		p := s.PlanChat(r, fixtureNow)
		if p.Handoff != "" {
			t.Errorf("%s: handed off: %s", name, p.Handoff)
			continue
		}
		var a *Answer
		if ref, _ := s.Budget(&p, fixtureNow, ipc.StaleAfterMs, nil); ref != nil {
			a = ref
		} else if g := s.Guard(&p, 1); g != nil {
			a = g
		}
		if a != nil {
			if want.Request != nil {
				t.Errorf("%s: the engine refused (%d) where the JS called the provider", name, a.Status)
				continue
			}
			if a.Status != want.Client.Status || string(a.Body) != want.Client.Body {
				t.Errorf("%s: client\n got %d %s\nwant %d %s", name, a.Status, a.Body, want.Client.Status, want.Client.Body)
			}
			for _, k := range []string{"content-type", "cache-control", "access-control-allow-origin"} {
				if a.Header.Get(k) != want.Client.Headers[k] {
					t.Errorf("%s: %s %q, want %q", name, k, a.Header.Get(k), want.Client.Headers[k])
				}
			}
			if a.NoLine {
				if string(want.Usage) != "null" {
					t.Errorf("%s: no line, the JS wrote %s", name, want.Usage)
				}
			} else {
				sameJSON(t, name+" usage", usageMap(a.Usage), want.Usage)
			}
			continue
		}
		if want.Request == nil {
			t.Errorf("%s: the engine would call the provider; the JS answered %d %s", name, want.Client.Status, want.Client.Body)
			continue
		}
		if p.URL != want.Request.URL || string(p.Body) != want.Request.Body {
			t.Errorf("%s: provider request\n got %s %s\nwant %s %s", name, p.URL, p.Body, want.Request.URL, want.Request.Body)
		}
		got := map[string]string{}
		for k := range p.Header {
			got[strings.ToLower(k)] = p.Header.Get(k)
		}
		sameJSON(t, name+" provider headers", got, mustJSON(want.Request.Headers))
		var rec ipc.UsageRecord
		var status int
		var body string
		if p.Stream && StreamsBody(c.Provider.Status) {
			cs := NewChatStream(p.Model, p.WantsUsage, func() float64 { return 1 })
			out, _ := cs.Feed([]byte(c.Provider.Body))
			out = append(out, cs.End()...)
			status, body, rec = 200, string(out), s.StreamLine(&p, cs, 1)
		} else {
			a := s.Respond(&p, c.Provider.Status, []byte(c.Provider.Body), 1)
			status, body, rec = a.Status, string(a.Body), a.Usage
		}
		if status != want.Client.Status || body != want.Client.Body {
			t.Errorf("%s: client\n got %d %s\nwant %d %s", name, status, body, want.Client.Status, want.Client.Body)
		}
		sameJSON(t, name+" usage", usageMap(rec), want.Usage)
	}
}

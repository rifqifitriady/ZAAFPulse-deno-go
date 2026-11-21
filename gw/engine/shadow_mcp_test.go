package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// mcpCapture is the capture the control plane keeps for a fixture's JS answer (lib/gateway/shadow.js): the client's
// status and bytes, the last line, and the tool it called with the tool endpoint's answer for the recorded value.
func mcpCapture(t *testing.T, f mcpFixture) *Capture {
	t.Helper()
	c := &Capture{Client: &CaptureClient{Status: f.Out.Client.Status, Body: f.Out.Client.Body}}
	if string(f.Out.Usage) != "null" && len(f.Out.Usage) > 0 {
		if err := json.Unmarshal(f.Out.Usage, &c.Usage); err != nil {
			t.Fatal(err)
		}
		if n, ok := c.Usage["cost"].(float64); ok {
			c.Usage["cost"] = n / 1e9 // the usage log holds USD
		}
	}
	if len(f.Out.ToolCalls) > 0 {
		r := string(endpointOf(f.In))
		c.Tool = &CaptureTool{Name: f.Out.ToolCalls[0].Name, Args: f.Out.ToolCalls[0].Args, R: &r}
	}
	return c
}

// compareMCPFixture plans the fixture as the shadow does (with its failing checks) and compares it with capture c.
func compareMCPFixture(t *testing.T, f mcpFixture, c *Capture) ShadowLine {
	t.Helper()
	s := mcpStateOf(t, f.In)
	p := s.PlanFor("mcp", mcpRequestOf(f.In), fixtureNow)
	wasBody, wasText := checkBody, checkText
	defer func() { checkBody, checkText = wasBody, wasText }()
	if f.In.FailBody {
		checkBody = func(jsv.Value, guard.Policy, string) (guard.Result, error) {
			return guard.Result{}, errors.New("a check failed")
		}
	}
	if f.In.FailText {
		checkText = func(jsv.Value, guard.Policy, string) (guard.TextResult, error) {
			return guard.TextResult{}, errors.New("a check failed")
		}
	}
	return s.compareCaptured(SubMCP, &p, c)
}

// Every case of gw/testdata/mcp compares the same through the shadow; the rate-limited one is skipped.
func TestShadowMCPFixtures(t *testing.T) {
	for _, f := range loadMCP(t) {
		t.Run(f.Name, func(t *testing.T) {
			l := compareMCPFixture(t, f, mcpCapture(t, f))
			want := "same"
			if f.In.RateLimited {
				want = "skipped"
			}
			if l.Outcome != want || l.Class != SubMCP || l.Status != f.Out.Client.Status {
				t.Fatalf("%+v", l)
			}
		})
	}
}

func mcpFixtureNamed(t *testing.T, name string) mcpFixture {
	t.Helper()
	for _, f := range loadMCP(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %s", name)
	return mcpFixture{}
}

// A changed reply byte, a changed argument, a different line, a different tool value are diffs; a capture without a
// tool call (the control plane refused first) compares the refusal; a 429 and a value the JS could not replay are
// skipped.
func TestShadowMCPDiffs(t *testing.T) {
	field := func(t *testing.T, l ShadowLine, want string) {
		t.Helper()
		if l.Outcome != "diff" || len(l.Diffs) == 0 || l.Diffs[0].Field != want {
			t.Fatalf("want a %s diff: %+v", want, l)
		}
	}
	f := mcpFixtureNamed(t, "call-result-inject-log.json")
	c := mcpCapture(t, f)
	c.Client.Body = strings.Replace(c.Client.Body, `"id":1`, `"id":2`, 1)
	field(t, compareMCPFixture(t, f, c), "client.body")

	c = mcpCapture(t, f)
	c.Tool.Args = strings.Replace(c.Tool.Args, "}", `,"x":1}`, 1)
	field(t, compareMCPFixture(t, f, c), "tool.args")

	c = mcpCapture(t, f)
	c.Tool.Name = "dt_other"
	field(t, compareMCPFixture(t, f, c), "tool.name")

	c = mcpCapture(t, f)
	c.Usage["status"] = float64(502)
	field(t, compareMCPFixture(t, f, c), "usage.status")

	c = mcpCapture(t, f)
	c.Usage = nil
	field(t, compareMCPFixture(t, f, c), "usage")

	c = mcpCapture(t, f)
	r := `{"r":{"result":{"content":[{"type":"text","text":"other"}]}}}`
	c.Tool.R = &r
	field(t, compareMCPFixture(t, f, c), "client.body")

	c = mcpCapture(t, f)
	c.Tool.R = nil // no endpoint answer: the engine's Internal error, the JS's reply
	field(t, compareMCPFixture(t, f, c), "client.body")

	c = mcpCapture(t, f)
	c.Tool.Skip = "the tool's value is not JSON"
	if l := compareMCPFixture(t, f, c); l.Outcome != "skipped" || !strings.Contains(l.Reason, "not JSON") {
		t.Fatalf("%+v", l)
	}

	c = mcpCapture(t, f)
	c.Tool = nil // the JS answered without calling the tool, the engine would call it
	field(t, compareMCPFixture(t, f, c), "served")

	c = mcpCapture(t, f)
	c.Tool, c.Client.Status, c.Usage = nil, 429, nil
	if l := compareMCPFixture(t, f, c); l.Outcome != "skipped" {
		t.Fatalf("%+v", l)
	}
	if l := compareMCPFixture(t, f, nil); l.Outcome != "diff" || l.Diffs[0].Field != "served" {
		t.Fatalf("%+v", l)
	}

	// The JS refused before the tool (the arguments blocked): the refusal is compared.
	b := mcpFixtureNamed(t, "call-block-args.json")
	c = mcpCapture(t, b)
	if c.Tool != nil {
		t.Fatal("call-block-args called the tool")
	}
	c.Client.Body = strings.Replace(c.Client.Body, "-32602", "-32603", 1)
	field(t, compareMCPFixture(t, b, c), "client.body")
	c = mcpCapture(t, b)
	r = `{}`
	c.Tool = &CaptureTool{Name: "dt_query", Args: "{}", R: &r}
	field(t, compareMCPFixture(t, b, c), "served")
}

// The shadow path end to end: a shadowed tools/call is forwarded marked, its capture asked for and compared (same),
// without a tool call of the engine's.
func TestShadowedMCPCompared(t *testing.T) {
	f := mcpFixtureNamed(t, "call-redact-args.json")
	capture, _ := json.Marshal(mcpCapture(t, f))
	e := New(NewUpstream(func(string) string { return "" }), func(*ipc.UsageReport) (int64, error) { return 0, nil }, t.Logf)
	e.SetState(mcpStateOf(t, f.In))
	e.SetModes(func(string) (bool, bool) { return false, true })
	e.SetTools(func(_ context.Context, body []byte) (int, []byte, error) {
		t.Error("the engine called the tool for the shadow")
		return 0, nil, errors.New("no")
	})
	lines := make(chan string, 4)
	asked := make(chan string, 1)
	own := func(method, path string, body []byte) (int, []byte, error) {
		if method == http.MethodGet && strings.HasPrefix(path, "/_zp/shadow/") {
			asked <- path
			return 200, capture, nil
		}
		lines <- method + " " + path + " " + string(body)
		return 204, nil, nil
	}
	js := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(f.In.Request.Body))
	req.Host, req.RemoteAddr = "127.0.0.1:4777", "127.0.0.1:50000"
	for k, v := range f.In.Request.Headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.Shadow("mcp", w, req, "/mcp", js, own)
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	select {
	case l := <-lines:
		if !strings.HasPrefix(l, "POST /_zp/shadow ") || !strings.Contains(l, `"class":"mcp"`) ||
			!strings.Contains(l, `"outcome":"same"`) {
			t.Fatalf("line %s", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no shadow line")
	}
	if len(asked) != 1 { // marked: the capture was asked for
		t.Fatal("no capture asked for")
	}
}

package engine

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// mcpCase is one case of gw/testdata/mcp (scripts/fixtures/gen-mcp.js).
type mcpCase struct {
	Case    string             `json:"case"`
	Config  ipc.ConfigPush     `json:"configPush"`
	Secrets ipc.SecretsHandoff `json:"secretsPush"`
	Request struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Tool        json.RawMessage `json:"tool"`
	Thrown      bool            `json:"thrown"`
	RateLimited bool            `json:"rateLimited"`
	FailBody    bool            `json:"failBody"`
	FailText    bool            `json:"failText"`
}

type mcpOut struct {
	ToolCalls []struct {
		Name string `json:"name"`
		Args string `json:"args"`
	} `json:"toolCalls"`
	Client struct {
		Status  int                `json:"status"`
		Headers map[string]*string `json:"headers"`
		Body    string             `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

type mcpFixture struct {
	Name string
	In   mcpCase
	Out  mcpOut
}

// loadMCP reads the call-* files of gw/testdata/mcp with the secrets put back, sorted by name.
func loadMCP(t *testing.T) []mcpFixture {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "testdata", "mcp", "call-*.json"))
	if len(files) == 0 {
		t.Fatal("no fixtures in gw/testdata/mcp (run node scripts/fixtures/gen-mcp.js)")
	}
	var out []mcpFixture
	for _, f := range files {
		fx, err := jsv.LoadFixture(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		x := mcpFixture{Name: filepath.Base(f)}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.In[0])), &x.In); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := json.Unmarshal([]byte(jsv.MustStringify(fx.Out)), &x.Out); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func mcpStateOf(t *testing.T, c mcpCase) *State {
	t.Helper()
	b, _ := json.Marshal(c) // a copy of its own
	var cp mcpCase
	if err := json.Unmarshal(b, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Config.Gen, cp.Secrets.Gen = 1, 1
	return NewState(&cp.Config, &cp.Secrets)
}

func mcpRequestOf(c mcpCase) *Request {
	h := make(map[string][]string)
	for k, v := range c.Request.Headers {
		h[http.CanonicalHeaderKey(k)] = []string{v}
	}
	return &Request{Method: c.Request.Method, RawPath: c.Request.Path, Header: h, Host: "127.0.0.1:4777",
		Peer: "127.0.0.1:50000", Body: []byte(c.Request.Body)}
}

// sameAnswer compares an answer (and its line) with the JS client answer and line.
func sameAnswer(t *testing.T, a Answer, want mcpOut) {
	t.Helper()
	w := &recordWriter{}
	line := writeAnswer(w, a)
	if w.status != want.Client.Status || w.body.String() != want.Client.Body {
		t.Errorf("client\n got %d %s\nwant %d %s", w.status, w.body.String(), want.Client.Status, want.Client.Body)
	}
	for k, v := range want.Client.Headers {
		g := w.header.Get(k)
		if (v == nil) != (g == "") || (v != nil && *v != g) {
			t.Errorf("header %s: got %q, want %v", k, g, ptrText(v))
		}
	}
	sameJSON(t, "usage", usageOfRecord(line), want.Usage)
}

// Every case of gw/testdata/mcp (the JS gateway's own answers): the engine plans it without a hand-off, sends the tool
// the same arguments (or refuses as JS does), and from the tool's recorded value gives the client the same status,
// headers and bytes and the same usage line.
func TestMCPFixtures(t *testing.T) {
	for _, f := range loadMCP(t) {
		t.Run(f.Name, func(t *testing.T) {
			s := mcpStateOf(t, f.In)
			p := s.PlanFor("mcp", mcpRequestOf(f.In), fixtureNow)
			if p.Handoff != "" {
				t.Fatalf("handed off: %s", p.Handoff)
			}
			if p.Class != SubMCP || p.Upstream != UpTool || p.Shape != ShapeOpenAI {
				t.Errorf("plan class %q upstream %q shape %d", p.Class, p.Upstream, p.Shape)
			}
			if f.In.RateLimited {
				l := NewRateLimiter()
				l.Take(p.Key.ID, p.Key.RPM, fixtureNow)
				sameAnswer(t, RateLimited(p.Key, l.Take(p.Key.ID, p.Key.RPM, fixtureNow), ShapeOpenAI), f.Out)
				return
			}
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
			args, st, refusal := s.CheckToolArgs(&p, 1)
			if refusal != nil {
				if len(f.Out.ToolCalls) > 0 {
					t.Fatalf("the engine refused where the JS called the tool")
				}
				sameAnswer(t, *refusal, f.Out)
				return
			}
			if len(f.Out.ToolCalls) != 1 {
				t.Fatalf("the engine would call the tool; the JS made %d calls", len(f.Out.ToolCalls))
			}
			if want := f.Out.ToolCalls[0]; p.Tool != want.Name || string(args) != want.Args {
				t.Errorf("tool call\n got %s %s\nwant %s %s", p.Tool, args, want.Name, want.Args)
			}
			endpoint := []byte(`{"thrown":true}`)
			if !f.In.Thrown {
				endpoint = append(append([]byte(`{"r":`), f.In.Tool...), '}')
			}
			sameAnswer(t, s.ToolReply(&p, st, endpoint, 1), f.Out)
		})
	}
}

// The control plane gone mid-call (no answer, or one that is not the endpoint's): -32603 Internal error, no line
// (known difference 1).
func TestMCPToolEndpointFailed(t *testing.T) {
	f := mcpBase(t)
	s := mcpStateOf(t, f)
	p := s.PlanMCP(mcpRequestOf(f), fixtureNow)
	_, st, _ := s.CheckToolArgs(&p, 1)
	for _, body := range []string{"", "not json", "[]", `"x"`} {
		var b []byte
		if body != "" {
			b = []byte(body)
		}
		a := s.ToolReply(&p, st, b, 1)
		if a.Status != 200 || string(a.Body) != `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}` ||
			!a.NoLine {
			t.Errorf("%q: %d %s line %v", body, a.Status, a.Body, !a.NoLine)
		}
	}
}

// mcpBase is the call-clean case: served, in scope, no hits.
func mcpBase(t *testing.T) mcpCase {
	t.Helper()
	for _, f := range loadMCP(t) {
		if f.Name == "call-clean.json" {
			return f.In
		}
	}
	t.Fatal("no call-clean fixture")
	return mcpCase{}
}

// MCPVersions is lib/mcp/protocol.js SUPPORTED_VERSIONS.
func TestMCPVersions(t *testing.T) {
	fx, err := jsv.LoadFixture(filepath.Join("..", "testdata", "mcp", "versions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsv.MustStringify(toJSVStrings(MCPVersions)), jsv.MustStringify(fx.Out); got != want {
		t.Errorf("MCPVersions %s, want %s", got, want)
	}
}

func toJSVStrings(list []string) *jsv.Array {
	a := jsv.Arr()
	for _, s := range list {
		a.Push(s)
	}
	return a
}

// Everything on /mcp but one tools/call JSON-RPC request the engine may serve is handed off (Decision 1).
func TestMCPHandoffs(t *testing.T) {
	base := mcpBase(t)
	body := func(b string) func(c *mcpCase) { return func(c *mcpCase) { c.Request.Body = b } }
	for _, tc := range []struct {
		name, why string
		edit      func(c *mcpCase)
	}{
		{"GET", "method", func(c *mcpCase) { c.Request.Method = "GET" }},
		{"no key", "key: ", func(c *mcpCase) { delete(c.Request.Headers, "authorization") }},
		{"a key without mcp", "key: not allowed", func(c *mcpCase) { c.Config.Keys[0].Scopes = []string{"llm"} }},
		{"bad version header", "mcp: protocol version", func(c *mcpCase) { c.Request.Headers["mcp-protocol-version"] = "2099-01-01" }},
		{"empty version header", "mcp: protocol version", func(c *mcpCase) { c.Request.Headers["mcp-protocol-version"] = "" }},
		{"empty body", "body: ", body("")},
		{"bad JSON", "body: ", body("{bad")},
		{"a batch", "body: ", body(`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}]`)},
		{"a response", "mcp: ", body(`{"jsonrpc":"2.0","id":1,"result":{}}`)},
		{"a notification", "mcp: a notification", body(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"x"}}`)},
		{"jsonrpc missing", "mcp: ", body(`{"id":1,"method":"tools/call","params":{"name":"x"}}`)},
		{"jsonrpc 2", "mcp: ", body(`{"jsonrpc":2,"id":1,"method":"tools/call","params":{"name":"x"}}`)},
		{"method not a string", "mcp: ", body(`{"jsonrpc":"2.0","id":1,"method":5}`)},
		{"initialize", "mcp: not tools/call", body(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)},
		{"ping", "mcp: not tools/call", body(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)},
		{"tools/list", "mcp: not tools/call", body(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)},
		{"resources/list", "mcp: not tools/call", body(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)},
		{"params 5", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":5}`)},
		{"params []", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}`)},
		{"params null", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":null}`)},
		{"params absent", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)},
		{"name 5", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":5}}`)},
		{"arguments []", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":[]}}`)},
		{"arguments x", "mcp: params", body(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":"x"}}`)},
		{"a browser header", "access: ", func(c *mcpCase) { c.Request.Headers["origin"] = "http://x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(base)
			var c mcpCase
			_ = json.Unmarshal(b, &c)
			tc.edit(&c)
			p := mcpStateOf(t, c).PlanMCP(mcpRequestOf(c), fixtureNow)
			if !strings.HasPrefix(p.Handoff, tc.why) || p.Handoff == "" {
				t.Errorf("handoff %q, want %q…", p.Handoff, tc.why)
			}
		})
	}
	// The base itself is served, and a repeated version header is read as Node joins it.
	p := mcpStateOf(t, base).PlanMCP(mcpRequestOf(base), fixtureNow)
	if p.Handoff != "" {
		t.Fatalf("base handed off: %s", p.Handoff)
	}
	r := mcpRequestOf(base)
	r.Header["Mcp-Protocol-Version"] = []string{"2025-06-18", "2025-06-18"}
	if p := mcpStateOf(t, base).PlanMCP(r, fixtureNow); !strings.HasPrefix(p.Handoff, "mcp: protocol version") {
		t.Errorf("a repeated version header: %q", p.Handoff)
	}
}

package engine

// The shadow compare of /mcp tools/call (P4g Decision 7): the control plane ran the call and noted the tool it called
// (lib/gateway/shadow.js noteTool: the arguments' JSON text and the tool endpoint's answer for the tool's value). The
// engine replays it: the arguments it would send against the noted ones, then the noted value through the result check
// and the reply builder (ToolReply), and compares the client's status and bytes and the usage line. The tool is never
// called for the shadow.

import (
	"encoding/json"
	"math"
	"net/http"
)

// CompareMCP is the line for the tools/call plan p and the control plane's capture c (nil: none was found).
func (s *State) CompareMCP(class string, p *Plan, c *Capture) ShadowLine {
	l := ShadowLine{Class: class}
	if c != nil && c.Client != nil {
		l.Status = c.Client.Status
	}
	if p.Handoff != "" {
		l.Outcome, l.Reason = "handoff", p.Handoff
		return l
	}
	d := func(field string, js, gov any) { l.Diffs = append(l.Diffs, Diff{field, js, gov}) }
	dt := func(field, js, gov string) { a, b := digest(js, gov); d(field, a, b) }
	args, st, refusal := s.CheckToolArgs(p, 0)
	switch {
	case c == nil || c.Client == nil:
		d("served", "no capture", "the engine would answer")
	case c.Tool == nil && c.Client.Status == http.StatusTooManyRequests:
		l.Outcome, l.Reason = "skipped", "the control plane's rate limit answered (each engine counts its own)"
		return l
	case c.Tool != nil && c.Tool.Skip != "":
		l.Outcome, l.Reason = "skipped", "mcp: "+c.Tool.Skip
		return l
	case c.Tool == nil && refusal == nil:
		d("served", "answered "+NumberString(float64(c.Client.Status))+" without a tool call",
			"the engine would call the tool")
	case c.Tool != nil && refusal != nil:
		d("served", "called the tool", "refused before the tool")
	case c.Tool == nil:
		// The control plane refused before the tool (the arguments check), as the engine would.
		compareMCPAnswer(d, dt, c, refusal)
	default:
		if c.Tool.Name != p.Tool {
			dt("tool.name", c.Tool.Name, p.Tool)
		}
		if c.Tool.Args != string(args) {
			dt("tool.args", c.Tool.Args, string(args))
		}
		var endpoint []byte
		if c.Tool.R != nil {
			endpoint = []byte(*c.Tool.R)
		}
		a := s.ToolReply(p, st, endpoint, 0)
		compareMCPAnswer(d, dt, c, &a)
	}
	l.Outcome = "same"
	if len(l.Diffs) > 0 {
		l.Outcome = "diff"
	}
	return l
}

// compareMCPAnswer compares the engine's answer a with the control plane's: the status, the bytes and the usage line
// (whether there is one, then its fields as Compare does).
func compareMCPAnswer(d func(string, any, any), dt func(string, string, string), c *Capture, a *Answer) {
	if a.Status != c.Client.Status {
		d("client.status", c.Client.Status, a.Status)
	}
	if string(a.Body) != c.Client.Body {
		dt("client.body", c.Client.Body, string(a.Body))
	}
	switch {
	case a.NoLine && c.Usage != nil:
		d("usage", "a line", "none")
	case !a.NoLine && c.Usage == nil:
		d("usage", "none", "a line")
	case !a.NoLine:
		var goU map[string]any
		b, _ := json.Marshal(a.Usage)
		_ = json.Unmarshal(b, &goU)
		for _, f := range usageFields {
			if canonical(c.Usage[f]) != canonical(goU[f]) {
				d("usage."+f, c.Usage[f], goU[f])
			}
		}
		if js, gov := text(c.Usage["error"]), text(goU["error"]); js != gov {
			dt("usage.error", js, gov)
		}
		jsCost, _ := c.Usage["cost"].(float64)
		if n := math.Round(jsCost * 1e9); int64(n) != a.Usage.Cost {
			d("usage.cost", int64(n), a.Usage.Cost)
		}
	}
}

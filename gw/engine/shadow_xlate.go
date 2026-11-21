package engine

// The shadow compare of the P4d classes (P4d Decision 21): the control plane served the call and kept the provider's
// answer as it was read, its client's head and bytes, its last usage line and the tape of the random bytes and clock
// reads its translation drew. The engine replays the provider's answer through the same plan, with an Env that reads
// the tape, and compares. Anthropic ping events are timing, not content: they are removed from both sides.

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
)

// tapeEnv replays a capture's tape: Random gives the recorded bytes in order, Now the recorded clock reads. check says
// how the replay used the tape ("" when exactly).
func tapeEnv(c *Capture) (*translate.Env, func() string) {
	var ri, ni int
	short := false
	env := &translate.Env{
		Random: func(n int) []byte {
			if c.Tape == nil || ri >= len(c.Tape.Random) {
				short = true
				return make([]byte, n)
			}
			b, err := hex.DecodeString(c.Tape.Random[ri])
			ri++
			if err != nil || len(b) != n {
				short = true
				return make([]byte, n)
			}
			return b
		},
		Now: func() int64 {
			if c.Tape == nil || ni >= len(c.Tape.Now) {
				short = true
				return 0
			}
			ni++
			return c.Tape.Now[ni-1]
		},
	}
	return env, func() string {
		switch {
		case short:
			return "the engine drew more than the tape holds"
		case c.Tape != nil && (ri < len(c.Tape.Random) || ni < len(c.Tape.Now)):
			return "the engine drew less than the tape holds"
		}
		return ""
	}
}

const anthropicPing = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

// stripPings removes the Anthropic keep-alive events.
func stripPings(s string) string { return strings.ReplaceAll(s, anthropicPing, "") }

// bufferWriter keeps a replayed answer.
type bufferWriter struct {
	status int
	header http.Header
	body   strings.Builder
}

func (w *bufferWriter) Head(status int, h http.Header) error {
	w.status, w.header = status, h
	return nil
}
func (w *bufferWriter) Write(b []byte) error { w.body.Write(b); return nil }
func (w *bufferWriter) Pinger(func() string) {}
func (w *bufferWriter) StopPings()           {}

// cutReader gives the captured text, then ErrCut when the provider broke off.
func cutReader(a ProviderAnswer) io.Reader {
	r := io.Reader(strings.NewReader(a.Body))
	if a.Ended == "cut" {
		r = io.MultiReader(r, errReader{})
	}
	return r
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, ErrCut }

// CompareTranslated is the line of a P4d class: p was planned before the control plane answered; c is its capture.
func (s *State) CompareTranslated(class string, p *Plan, c *Capture) ShadowLine {
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
	calls := 1
	if p.Upstream == UpNone {
		calls = 0
	}
	switch {
	case c == nil || c.Client == nil:
		d("served", "no capture", "the engine would answer")
	case c.Client.Truncated:
		l.Outcome, l.Reason = "skipped", "the capture is over its limit"
		return l
	case calls == 1 && len(c.Upstream) == 0 && len(c.ProviderRequests) > 0:
		l.Outcome, l.Reason = "skipped", "the provider call failed: no answer to compare"
		return l
	case len(c.ProviderRequests) == 0 && c.Client.Status == http.StatusTooManyRequests:
		l.Outcome, l.Reason = "skipped", "the control plane's rate limit answered (each engine counts its own)"
		return l
	case len(c.ProviderRequests) == 0 && budgetRefusalAny(c.Client.Status, c.Client.Body):
		l.Outcome, l.Reason = "skipped", budgetLag
		return l
	case len(c.ProviderRequests) != calls:
		d("providerRequests", len(c.ProviderRequests), calls)
	default:
		if calls == 1 {
			if len(c.Upstream) > 0 && c.Upstream[0].Truncated {
				l.Outcome, l.Reason = "skipped", "the capture is over its limit"
				return l
			}
			pr := c.ProviderRequests[0]
			if pr.URL != p.URL {
				dt("providerRequest.url", pr.URL, p.URL)
			}
			goH := map[string]string{}
			for k := range p.Header {
				goH[strings.ToLower(k)] = p.Header.Get(k)
			}
			if a, b := headerSet(pr.Headers), headerSet(goH); a != b {
				dt("providerRequest.headers", a, b)
			}
			jsBody := ""
			if pr.Body != nil {
				jsBody = *pr.Body
			}
			if jsBody != string(p.Body) {
				dt("providerRequest.body", jsBody, string(p.Body))
			}
		}
		q := *p
		env, check := tapeEnv(c)
		q.Env = env
		w := &bufferWriter{}
		var line *ipc.UsageRecord
		if calls == 0 {
			line = writeAnswer(w, *p.Self)
		} else {
			up := c.Upstream[0]
			h := http.Header{}
			h.Set("Content-Type", up.ContentType)
			line = s.Deliver(&q, ProviderResult{Status: up.Status, Header: h, Body: io.NopCloser(cutReader(up))},
				func() float64 { return 0 }, w)
		}
		if w.status != c.Client.Status {
			d("client.status", c.Client.Status, w.status)
		}
		for _, k := range []string{"content-type", "x-zp-dropped"} {
			if js, gov := c.Client.Headers[k], w.header.Get(k); js != gov {
				d("client.headers."+k, js, gov)
			}
		}
		if js, gov := stripPings(c.Client.Body), stripPings(w.body.String()); js != gov {
			dt("client.body", js, gov)
		}
		if why := check(); why != "" {
			d("tape", why, "")
		}
		switch {
		case line == nil && c.Usage != nil:
			d("usage", "a line", "none")
		case line != nil && c.Usage == nil:
			d("usage", "none", "a line")
		case line != nil:
			var goU map[string]any
			b, _ := json.Marshal(line)
			_ = json.Unmarshal(b, &goU)
			for _, f := range usageFields {
				if js, gov := canonical(c.Usage[f]), canonical(goU[f]); js != gov {
					d("usage."+f, c.Usage[f], goU[f])
				}
			}
			if js, gov := text(c.Usage["error"]), text(goU["error"]); js != gov {
				dt("usage.error", js, gov)
			}
			jsCost, _ := c.Usage["cost"].(float64)
			if n := math.Round(jsCost * 1e9); int64(n) != line.Cost {
				d("usage.cost", int64(n), line.Cost)
			}
		}
	}
	l.Outcome = "same"
	if len(l.Diffs) > 0 {
		l.Outcome = "diff"
	}
	return l
}

// budgetRefusalAny is budgetRefusal in any client shape: a 402 whose message names a used-up budget.
func budgetRefusalAny(status int, body string) bool {
	return status == http.StatusPaymentRequired && strings.Contains(body, `"message":"budget \"`)
}

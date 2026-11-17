package engine

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
)

// countingEnv is counterEnv that also keeps every draw, as the control plane's tape would.
func countingEnv(tape *struct {
	Random []string `json:"random"`
	Now    []int64  `json:"now"`
}) *translate.Env {
	base := counterEnv()
	return &translate.Env{
		Random: func(n int) []byte {
			b := base.Random(n)
			tape.Random = append(tape.Random, hex.EncodeToString(b))
			return b
		},
		Now: func() int64 {
			v := base.Now()
			tape.Now = append(tape.Now, v)
			return v
		},
	}
}

// xlateCapture is the capture the control plane keeps for a fixture's JS answer, with the tape the generator's draws make.
func xlateCapture(t *testing.T, f xlateFixture) *Capture {
	t.Helper()
	c := &Capture{Client: &CaptureClient{Status: f.Out.Client.Status, Body: f.Out.Client.Body, Headers: map[string]string{}}}
	for _, k := range []string{"content-type", "x-zp-dropped"} {
		if v := f.Out.Client.Headers[k]; v != nil {
			c.Client.Headers[k] = *v
		}
	}
	for _, r := range f.Out.Requests {
		c.ProviderRequests = append(c.ProviderRequests, struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		}{r.URL, r.Headers, r.Body})
	}
	if pr := f.In.Provider; pr != nil && pr.Throw == "" && len(f.Out.Requests) > 0 {
		a := ProviderAnswer{Status: pr.Status, ContentType: pr.Headers["content-type"], Body: pr.Body, Ended: "eof"}
		if pr.Chunks != nil {
			a.Body = strings.Join(pr.Chunks, "")
			if pr.Cut {
				a.Ended = "cut"
			}
		}
		c.Upstream = []ProviderAnswer{a}
	}
	if string(f.Out.Usage) != "null" {
		_ = json.Unmarshal(f.Out.Usage, &c.Usage)
		if n, ok := c.Usage["cost"].(float64); ok {
			c.Usage["cost"] = n / 1e9 // the usage log holds USD
		}
	}
	// The tape: the draws one replay with the generator's counter makes.
	c.Tape = &struct {
		Random []string `json:"random"`
		Now    []int64  `json:"now"`
	}{}
	s, p := planXlate(t, f)
	if refusalOf(t, s, &p, f.In) == nil && f.In.Provider != nil {
		p.Env = countingEnv(c.Tape)
		s.Deliver(&p, providerOf(f.In, nil), func() float64 { return 1 }, &recordWriter{})
	}
	return c
}

// compareFixture plans the fixture as the shadow does and compares it with capture c.
func compareFixture(t *testing.T, f xlateFixture, c *Capture) ShadowLine {
	t.Helper()
	s, p := planXlate(t, f)
	p.Env = translate.Default // the compare must take the tape, not the plan's own draws
	if ref := refusalOf(t, s, &p, f.In); ref != nil && p.Upstream != UpNone {
		return s.CompareRefusal(p.Class, &p, ref, c)
	}
	return s.CompareTranslated(p.Class, &p, c)
}

// Every served xlate fixture compares the same through the shadow (the provider text replayed with the tape).
func TestShadowTranslatedFixtures(t *testing.T) {
	for _, f := range loadXlate(t) {
		if f.In.RateLimited {
			continue // the engine's own window: rateLimitedLine, not a compare
		}
		t.Run(f.Name, func(t *testing.T) {
			l := compareFixture(t, f, xlateCapture(t, f))
			throw := f.In.Provider != nil && f.In.Provider.Throw != ""
			switch {
			case throw && l.Outcome == "skipped":
			case l.Outcome != "same":
				t.Fatalf("%+v", l)
			}
		})
	}
}

// A changed byte is a diff; the Anthropic pings are not; a tape the engine reads differently is a diff.
func TestShadowTranslatedDiffs(t *testing.T) {
	var f xlateFixture
	for _, x := range loadXlate(t) {
		if x.Name == "msg-oa-stream.json" {
			f = x
		}
	}
	c := xlateCapture(t, f)
	c.Client.Body = strings.Replace(c.Client.Body, "Hel", "Hal", 1)
	if l := compareFixture(t, f, c); l.Outcome != "diff" || l.Diffs[0].Field != "client.body" {
		t.Fatalf("%+v", l)
	}
	c = xlateCapture(t, f)
	i := strings.Index(c.Client.Body, "event: content_block_delta")
	c.Client.Body = c.Client.Body[:i] + anthropicPing + c.Client.Body[i:]
	if l := compareFixture(t, f, c); l.Outcome != "same" {
		t.Fatalf("a ping is a diff: %+v", l)
	}
	c = xlateCapture(t, f)
	c.Tape.Random = c.Tape.Random[1:]
	if l := compareFixture(t, f, c); l.Outcome != "diff" {
		t.Fatalf("a short tape compared the same: %+v", l)
	}
	c = xlateCapture(t, f)
	c.Client.Truncated = true
	if l := compareFixture(t, f, c); l.Outcome != "skipped" {
		t.Fatalf("%+v", l)
	}
}

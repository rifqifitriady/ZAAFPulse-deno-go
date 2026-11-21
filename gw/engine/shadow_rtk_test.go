package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
)

// The shadow compares a call the token saver rewrote (spec D14): the saver runs in the compare, the shortened body is
// compared with the one the control plane sent, and the line says saver so the token-saver switch counts it.
func TestShadowComparesSavedCalls(t *testing.T) {
	for _, name := range []string{"saver-whole", "saver-stream", "saver-redact-after", "saver-guard-block"} {
		s, p, want, c := guardFixture(t, name)
		cp := jsCapture(t, want, nil, c)
		l := s.compareCaptured(SubClass("chat", &p, nil), &p, cp)
		if l.Outcome != "same" || !l.Saver {
			t.Fatalf("%s: %+v", name, l)
		}
	}
	// A one-byte change in what the control plane sent is a diff on the provider body.
	s, p, want, c := guardFixture(t, "saver-whole")
	cp := jsCapture(t, want, nil, c)
	changed := strings.Replace(*cp.ProviderRequests[0].Body, "omitted", "omitteD", 1)
	cp.ProviderRequests[0].Body = &changed
	if l := s.compareCaptured("chat", &p, cp); l.Outcome != "diff" || l.Diffs[0].Field != "providerRequest.body" || !l.Saver {
		t.Fatalf("got %+v", l)
	}
	// The header off: no saver on either side, no saver mark.
	s, p, want, c = guardFixture(t, "saver-header-off")
	if l := s.compareCaptured("chat", &p, jsCapture(t, want, nil, c)); l.Outcome != "same" || l.Saver {
		t.Fatalf("header off: %+v", l)
	}
}

// A Save hand-off in the compare is a handoff line (spec D14), not a diff.
func TestShadowSaverHandoffLine(t *testing.T) {
	s, p, want, c := guardFixture(t, "saver-whole")
	for _, tg := range p.saver.targets {
		tg.holder.Set(tg.key, Str(strings.Repeat("\x1b]", 300000)))
	}
	l := s.compareCaptured("chat", &p, jsCapture(t, want, nil, c))
	if l.Outcome != "handoff" || l.Reason != saverOverBudget || l.Status != want.Client.Status {
		t.Fatalf("got %+v", l)
	}
	if why := s.Save(&p, time.Now()); why != saverOverBudget || len(p.Body) != 0 {
		t.Fatalf("a second Save: %q, body %d bytes (review m2: the hand-off stands)", why, len(p.Body))
	}
}

// Tasks 8-9 review m3: translated and passthrough saver calls through the shadow's own order (compareCaptured: Save,
// then the guardrails or the refusal, then the compare), from a plan that has not run the saver yet.
func TestShadowTranslatedSaverFixtures(t *testing.T) {
	want := map[string]bool{"msg-saver-whole": true, "msg-saver-stream": true, "msg-saver-guard-redact": true,
		"msg-saver-guard-block": true, "msg-native-saver": true, "msg-native-saver-guard-block": true,
		"chat-an-saver": true, "resp-saver": true, "gem-saver": true}
	n := 0
	for _, f := range loadXlate(t) {
		if !want[strings.TrimSuffix(f.Name, ".json")] {
			continue
		}
		n++
		t.Run(f.Name, func(t *testing.T) {
			s := xlateStateOf(t, f.In)
			r := xlateRequestOf(f.In)
			p := s.PlanFor(classOfPath(r.RawPath), r, fixtureNow)
			if p.Handoff != "" || p.saver == nil {
				t.Fatalf("handoff %q, saver %v", p.Handoff, p.saver != nil)
			}
			p.Env = translate.Default // the compare takes the tape
			if l := s.compareCaptured(p.Class, &p, xlateCapture(t, f)); l.Outcome != "same" || !l.Saver {
				t.Fatalf("%+v", l)
			}
		})
	}
	if n != len(want) {
		t.Fatalf("%d of %d saver fixtures found", n, len(want))
	}
}

// Final review I1: a panic out of the saver is a hand-off with the panic kept for the log, never a crash.
func TestSaverPanicHandsOff(t *testing.T) {
	saverHook = func() { panic("boom") }
	defer func() { saverHook = nil }()
	s, p, want, c := guardFixture(t, "saver-whole")
	if why := s.Save(&p, time.Now()); why != saverPanicked || p.SaverPanic != "boom" {
		t.Fatalf("Save: %q %q", why, p.SaverPanic)
	}
	_, p, _, _ = guardFixture(t, "saver-whole")
	if l := s.compareCaptured("chat", &p, jsCapture(t, want, nil, c)); l.Outcome != "handoff" || l.Reason != saverPanicked {
		t.Fatalf("compare: %+v", l)
	}
}

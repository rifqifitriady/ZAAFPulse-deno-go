package engine

import (
	"strings"
	"testing"
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
}

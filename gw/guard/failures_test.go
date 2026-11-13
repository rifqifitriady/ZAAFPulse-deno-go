package guard

import (
	"reflect"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// N1 (re-review 3): a detector that fails in log mode no longer goes unnoticed: the result lists each failing check,
// how many times it failed in the call and the first error, so the wiring can log or count it.
func TestLogModeFailuresAreReported(t *testing.T) {
	real := detectSecrets
	t.Cleanup(func() { detectSecrets = real })
	detectSecrets = func([]uint16, *jsre.Budget) []Span { panic("boom") }
	body := jsv.NewObject().Set("model", "m").Set("messages", jsv.Arr(
		jsv.NewObject().Set("role", "user").Set("content", "one"),
		jsv.NewObject().Set("role", "user").Set("content", "two")))
	p := Policy{Secrets: "log", PII: "off", Injection: "off"}
	r, err := CheckBody(body, p, "openai-chat")
	want := []Failure{{Check: "secrets", Count: 2, Err: "boom"}}
	if err != nil || !r.Error || !reflect.DeepEqual(r.Failures, want) {
		t.Fatalf("%+v %v", r, err)
	}
	tr, err := CheckText("one", p, "input")
	if err != nil || !tr.Error || !reflect.DeepEqual(tr.Failures, []Failure{{Check: "secrets", Count: 1, Err: "boom"}}) {
		t.Fatalf("%+v %v", tr, err)
	}
	if r, _ := CheckBody(chat("fine"), Defaults, "openai-chat"); r.Failures != nil {
		t.Fatalf("failures without a failure: %+v", r.Failures)
	}
}

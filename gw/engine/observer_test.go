package engine

import (
	"reflect"
	"testing"
)

// A stream fed one byte at a time gives what one feed gives (StringDecoder holds a split character back).
func TestObserverSplitFeedsGiveTheSameResult(t *testing.T) {
	text := "data: {\"id\":\"café\",\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"length\"}]}\n"
	whole := NewObserver()
	whole.Feed([]byte(text))
	split := NewObserver()
	for i := 0; i < len(text); i++ {
		split.Feed([]byte{text[i]})
	}
	a, b := whole.Result(), split.Result()
	if !reflect.DeepEqual(a, b) || a.ID != "café" || !reflect.DeepEqual(a.Reasons, []string{"stop", "length"}) {
		t.Fatalf("whole %+v, split %+v", a, b)
	}
}

// Known deviation 1: an id that is not a string is ignored (JS keeps it and then loses the client span to a TypeError).
func TestObserverIgnoresANonStringID(t *testing.T) {
	o := NewObserver()
	o.Feed([]byte(`{"type":"message","id":7,"role":"assistant","content":[],"stop_reason":"end_turn"}`))
	if r := o.Result(); r.ID != "" || !reflect.DeepEqual(r.Reasons, []string{"end_turn"}) {
		t.Fatalf("%+v", r)
	}
}

// Known deviation 2: a Gemini finishReason is lower-cased with strings.ToLower (JS: full Unicode special casing).
func TestObserverLowerCasesGeminiReasonsWithGo(t *testing.T) {
	o := NewObserver()
	o.Feed([]byte(`{"candidates":[{"finishReason":"İ"}]}`))
	if r := o.Result(); !reflect.DeepEqual(r.Reasons, []string{"i"}) { // JS gives "i\u0307"
		t.Fatalf("%+v", r)
	}
}

// A TypeError in JS (a property of null) ends the rest of the array's absorb, not what came before.
func TestObserverStopsAtATypeError(t *testing.T) {
	o := NewObserver()
	o.Feed([]byte(`[{"id":"a","choices":[{"finish_reason":"stop"}]},{"choices":[null]},{"choices":[{"finish_reason":"length"}]}]`))
	if r := o.Result(); r.ID != "a" || !reflect.DeepEqual(r.Reasons, []string{"stop"}) {
		t.Fatalf("%+v", r)
	}
}

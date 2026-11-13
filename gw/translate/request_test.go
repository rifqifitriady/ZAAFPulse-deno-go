package translate

import (
	"errors"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

func TestStopTables(t *testing.T) {
	if ToAnthropicStop["tool_calls"] != "tool_use" || FromAnthropicStop["pause_turn"] != "stop" || ToGeminiFinish["content_filter"] != "SAFETY" {
		t.Fatal("stop tables")
	}
	if len(ToAnthropicStop) != 5 || len(FromAnthropicStop) != 6 || len(ToGeminiFinish) != 4 {
		t.Fatal("stop table sizes changed")
	}
}

func TestBadRequestIsA400(t *testing.T) {
	_, err := OpenAIToAnthropicRequest(jsv.MustParse(`{"messages":[],"response_format":{"type":"json_object"}}`))
	var bad *BadRequest
	if !errors.As(err, &bad) || bad.Status() != 400 || bad.Type() != "invalid_request_error" {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultEnvIDsAreRandom(t *testing.T) {
	a, _ := OpenAIToAnthropic(jsv.NewObject(), "m")
	b, _ := OpenAIToAnthropic(jsv.NewObject(), "m")
	if a.Get("id") == b.Get("id") || len(jsv.ToString(a.Get("id"))) != len("msg_")+24 {
		t.Fatalf("ids %v %v", a.Get("id"), b.Get("id"))
	}
}

package guard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// Built by concatenation so the file holds no scanner-shaped literal.
const ant = "sk" + "-ant-api03-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"

func chat(content string) jsv.Value {
	return jsv.NewObject().Set("model", "gpt-x").Set("messages", jsv.Arr(jsv.NewObject().Set("role", "user").Set("content", content)))
}

func TestCheckBodyNeverChangesItsInput(t *testing.T) {
	body := jsv.MustParse(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"key ` + ant + ` mail a@b.com"}]}],"metadata":{"note":"` + ant + `"}}`)
	before := jsv.Canonical(body)
	r, err := CheckBody(body, Policy{Secrets: "redact", PII: "redact", Injection: "log"}, "openai-chat")
	if err != nil || r.Action != "redacted" {
		t.Fatalf("%v %v", r.Action, err)
	}
	if jsv.Canonical(body) != before {
		t.Fatal("input changed")
	}
	if jsv.Canonical(r.Body) == before {
		t.Fatal("nothing redacted")
	}
}

func TestFailClosed(t *testing.T) {
	real := detectSecrets
	t.Cleanup(func() { detectSecrets = real })
	detectSecrets = func([]uint16, *jsre.Budget) []Span { panic("boom") }
	for _, mode := range []string{"redact", "block"} {
		if _, err := CheckBody(chat("hello"), Policy{Secrets: mode, PII: "off", Injection: "log"}, "openai-chat"); err == nil || err.Error() != "boom" {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	r, err := CheckBody(chat("hello"), Policy{Secrets: "log", PII: "off", Injection: "log"}, "openai-chat")
	if err != nil || r.Action != "logged" || !r.Error {
		t.Fatalf("log mode: %+v %v", r, err)
	}
	if _, err := CheckText("hello", Policy{Secrets: "block"}, "input"); err == nil {
		t.Fatal("checkText did not fail closed")
	}
}

func TestBlockError(t *testing.T) {
	_, err := CheckBody(chat(ant), Policy{Secrets: "block", PII: "log", Injection: "log"}, "openai-chat")
	var b *Block
	if !errors.As(err, &b) || len(b.Hits) != 1 || strings.Contains(err.Error(), ant[:6]) {
		t.Fatalf("got %v", err)
	}
}

// The JS suite's cost bounds (test/guardrails.test.js): a 1 MB body with every check on, and a 2 MB single line.
func TestLargeBodiesStayFast(t *testing.T) {
	line := "The agent reads the token from the env, calls the API with the secret key and asks the AI model; see " +
		"https://example.com/docs, mail a@b.co or 10.0.0.1.\n"
	big := strings.Repeat(line, 1048576/len(line)/40+1)
	msgs := jsv.Arr()
	for i := 0; i < 40; i++ {
		msgs.Push(jsv.NewObject().Set("role", "user").Set("content", big))
	}
	start := time.Now()
	r, err := CheckBody(jsv.NewObject().Set("model", "m").Set("messages", msgs), Policy{Scope: "all", Secrets: "redact", PII: "redact", Injection: "log"}, "openai-chat")
	if err != nil || r.Action != "redacted" {
		t.Fatalf("%v %v", r.Action, err)
	}
	t.Logf("1 MB body: %v", time.Since(start))
	fn := `function f(a){return fetch(url,{method:"POST",body:JSON.stringify({token:a,secret:b})})}`
	start = time.Now()
	if _, err := CheckBody(chat(strings.Repeat(fn, 12000)), Defaults, "openai-chat"); err != nil {
		t.Fatal(err)
	}
	t.Logf("1 MB minified JS: %v", time.Since(start))
	if testing.Short() {
		return
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

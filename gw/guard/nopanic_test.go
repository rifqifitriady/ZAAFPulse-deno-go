package guard

import (
	"errors"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// expectBlock runs f and fails unless it returns a *Block; a panic fails the test instead of crashing it.
func expectBlock(t *testing.T, name string, f func() error) {
	t.Helper()
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("%s: panicked with %v", name, p)
			}
		}()
		err = f()
	}()
	var b *Block
	if err != nil && !errors.As(err, &b) {
		t.Errorf("%s: want a *Block, got %v", name, err)
	} else if err == nil {
		t.Errorf("%s: want a *Block, got no error", name)
	}
}

// I5 (re-review 2): the data: URL check of the walker ran without a budget and outside the limit handling; this
// 4.4 MB message made CheckBody panic with ErrStack under the default policy (JS: null).
func TestDataURLCheckFailsClosed(t *testing.T) {
	text := "data:a/b" + strings.Repeat(";x", 2_200_000) + ";base64,AAAA"
	expectBlock(t, "data URL", func() error { _, err := CheckBody(chat(text), Defaults, "openai-chat"); return err })
}

// Every entry point, given input that drives a matcher past its limits, returns a *Block and never panics.
func TestNoEntryPointPanicsPastALimit(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("10 MB inputs: not under -short or the race detector")
	}
	inputs := map[string]string{
		"data URL": "data:a/b" + strings.Repeat(";x", 2_200_000) + ";base64,AAAA",
		"ipv6":     " " + strings.Repeat("1:", 5_000_000) + "x",
		"email":    " x@" + strings.Repeat("a.", 5_000_000) + "1",
		"split":    "token " + strings.Repeat("a ", 5_000_000),
	}
	all := Policy{Scope: "all", Secrets: "log", PII: "log", Injection: "log"}
	for name, text := range inputs {
		for _, format := range Formats {
			if name == "split" && format == "arguments" {
				continue // MCP arguments get no injection check, the only one this input overruns
			}
			expectBlock(t, name+"/"+format, func() error {
				body := jsv.NewObject().Set("messages", jsv.Arr(jsv.NewObject().Set("role", "user").Set("content", text)))
				_, err := CheckBody(body, all, format)
				return err
			})
		}
		expectBlock(t, name+"/string body", func() error { _, err := CheckBody(text, all, "embeddings"); return err })
		for _, part := range []string{"input", "arguments", "result"} {
			if name == "data URL" {
				continue // CheckText never skips data: URLs, so no limit is reached on this one
			}
			if (part == "result") != (name == "split") && part != "input" {
				continue // a tool result gets injection only, arguments get no injection
			}
			expectBlock(t, name+"/text "+part, func() error { _, err := CheckText(text, all, part); return err })
		}
	}
}

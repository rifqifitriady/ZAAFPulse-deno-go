package ipc

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

const (
	testKey   = "test-openai-key-0001"
	testToken = "test-vertex-token-0001"
)

func handoffLine(body string) string {
	return `{"v":1,"type":"secrets.handoff","seq":3,"ts":1,"body":` + body + `}`
}

const validHandoff = `{"gen":1,"providers":[{"id":"openai","value":"` + testKey + `"},{"id":"vertex","token":{"value":"` +
	testToken + `","expiresAt":1}},{"id":"ollama"}],"mask":["` + testKey + `","` + testToken + `"]}`

func noSecret(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, testKey) || strings.Contains(s, testToken) {
		t.Fatalf("a secret reached %s: %s", what, s)
	}
}

// formats prints v with every formatter a log line or an error could use.
func formats(v any) map[string]string {
	out := map[string]string{
		"%v": fmt.Sprintf("%v", v), "%+v": fmt.Sprintf("%+v", v), "%#v": fmt.Sprintf("%#v", v),
		"%s": fmt.Sprintf("%s", v), "%q": fmt.Sprintf("%q", v), "Sprint": fmt.Sprint(v), "Sprintln": fmt.Sprintln(v),
	}
	if s, ok := v.(fmt.Stringer); ok {
		out["String"] = s.String()
	}
	if s, ok := v.(fmt.GoStringer); ok {
		out["GoString"] = s.GoString()
	}
	return out
}

// E6: after a secrets.handoff decode, the envelope no longer holds the raw body, and no formatter of the message, its
// envelope, its body or any part of it prints a secret.
func TestSecretsHandoffDecodeLeavesNoSecretToPrint(t *testing.T) {
	for _, strict := range []bool{true, false} {
		line := []byte(handoffLine(validHandoff))
		msg, err := Decode(line, strict)
		if err != nil {
			t.Fatal(err)
		}
		if len(msg.Envelope.Body) != 0 {
			t.Fatalf("Envelope.Body kept: %s", msg.Envelope.Body)
		}
		h := msg.Body.(*SecretsHandoff)
		if *h.Providers[0].Value != testKey || h.Providers[1].Token.Value != testToken || len(h.Mask) != 2 {
			t.Fatal("the decoded secrets are not usable")
		}
		if !bytes.Contains(line, []byte(testKey)) {
			t.Fatal("Decode changed the caller's line")
		}
		parts := map[string]any{
			"message": msg, "*message": &msg, "envelope": msg.Envelope, "body": h, "*body": *h,
			"providers": h.Providers, "provider": h.Providers[0], "*provider": &h.Providers[0],
			"token": *h.Providers[1].Token, "*token": h.Providers[1].Token,
		}
		for name, v := range parts {
			for f, s := range formats(v) {
				noSecret(t, name+" "+f, s)
			}
		}
	}
}

// A secrets.handoff that fails to decode: neither the error text (logged and sent back as a protocol error) nor the
// returned envelope carries a secret.
func TestSecretsHandoffDecodeErrorsLeaveNoSecret(t *testing.T) {
	for _, body := range []string{
		`{"gen":1,"providers":[{"id":"openai","value":"` + testKey + `","extra":"` + testToken + `"}],"mask":[]}`,
		`{"gen":0,"providers":[{"id":"openai","value":"` + testKey + `"}],"mask":["` + testToken + `"]}`,
		`{"gen":1,"providers":[{"id":"","value":"` + testKey + `"}],"mask":["` + testToken + `"]}`,
		`{"gen":1,"providers":[{"id":"vertex","token":{"value":"` + testToken + `"}}],"mask":["` + testKey + `"]}`,
		`{"gen":1,"providers":[{"id":"vertex","token":"` + testToken + `"}],"mask":["` + testKey + `"]}`,
		`{"gen":1,"providers":[{"id":"openai","value":{"k":"` + testKey + `"}}],"mask":[]}`,
		`{"gen":"` + testKey + `","providers":[],"mask":[]}`,
		`{"gen":1,"providers":[],"mask":"` + testKey + `"}`,
		`{"gen":1,"providers":[],"mask":[1,"` + testKey + `"]}`,
		`{"gen":1,"providers":[{"id":"` + testKey + `","value":null}],"mask":[]}`,
	} {
		for _, strict := range []bool{true, false} {
			msg, err := Decode([]byte(handoffLine(body)), strict)
			if err == nil {
				if !strict {
					continue // lenient decoding accepts an unknown field
				}
				t.Fatalf("decoded: %s", body)
			}
			noSecret(t, "the error", err.Error())
			noSecret(t, "the protocol error", (&ProtocolError{err}).Error())
			if len(msg.Envelope.Body) != 0 {
				t.Fatalf("Envelope.Body kept after a failed decode: %s", msg.Envelope.Body)
			}
			for f, s := range formats(msg) {
				noSecret(t, "message "+f, s)
			}
		}
	}
}

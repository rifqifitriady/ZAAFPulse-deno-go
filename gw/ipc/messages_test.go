package ipc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSecretsNeverPrint(t *testing.T) {
	v, tok := "test-openai-key-0001", "test-vertex-token-0001"
	h := SecretsHandoff{Gen: 1, Providers: []ProviderSecret{{ID: "openai", Value: &v}, {ID: "vertex", Token: &OAuthToken{Value: tok, ExpiresAt: 1}}}, Mask: []string{v, tok}}
	for _, s := range []string{fmt.Sprint(h), fmt.Sprintf("%v %+v", h, &h), fmt.Sprint(h.Providers), fmt.Sprintf("%+v", *h.Providers[1].Token)} {
		if strings.Contains(s, v) || strings.Contains(s, tok) {
			t.Fatalf("a secret reached a formatted string: %s", s)
		}
	}
}

func TestNullableKeepsAbsentNullAndValue(t *testing.T) {
	for _, in := range []string{
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0}`,
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"ttft":null}`,
		`{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"ttft":12.5}`,
	} {
		line := `{"v":1,"type":"usage.report","seq":1,"ts":0,"body":{"record":` + in + `}}`
		msg, err := Decode([]byte(line), true)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := Encode(msg.Envelope, msg.Body)
		var a, b any
		_ = json.Unmarshal([]byte(line), &a)
		_ = json.Unmarshal(out, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("\n in: %s\nout: %s", line, out)
		}
	}
}

func TestOptionalFieldMayNotBeNull(t *testing.T) {
	line := `{"v":1,"type":"usage.report","seq":1,"ts":0,"body":{"record":{"ts":1,"keyId":"k","provider":"p","model":"m","status":200,"ms":1,"cost":0,"requested":null}}}`
	if _, err := Decode([]byte(line), false); err == nil || !strings.Contains(err.Error(), "requested: must not be null") {
		t.Fatalf("got %v", err)
	}
}

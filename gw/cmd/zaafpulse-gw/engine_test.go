package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// chatConfig is a config.push with chat on the go engine, one provider at baseURL and one key for secret.
func chatConfig(seq, gen, port, upstream int, baseURL, secret string) string {
	sum := sha256.Sum256([]byte(secret))
	gw := fmt.Sprintf(`{"providers":[{"id":"pv","preset":"ollama","baseUrl":%q}],"routes":[],"guardrails":`+
		`{"scope":"external","secrets":"off","pii":"off","injection":"off"},"defaults":{"model":"","plannerModel":"",`+
		`"codeModel":""},"tokenSaver":true,"modelMap":[]}`, baseURL)
	key := fmt.Sprintf(`{"id":"k1","name":"k1","scopes":["llm"],"models":[],"agents":[],"knowledge":[],"budgetUSD":null,`+
		`"rpm":1,"privateOnly":false,"expiresAt":null,"revokedAt":null,"guardrails":{},"prefix":"zp_","approveOwnRuns":false,`+
		`"createdAt":0,"lastUsedAt":null,"hash":%q}`, hex.EncodeToString(sum[:]))
	return fmt.Sprintf(`{"v":1,"type":"config.push","seq":%d,"ts":0,"body":{"gen":%d,"gateway":%s,"keys":[%s],`+
		`"pricing":{"llama-9":{"input":1000000000,"output":2000000000}},"front":%s}}`,
		seq, gen, gw, key, frontJSON(port, upstream, `{"chat":"go"}`))
}

// End to end in the gateway process: chat on go is answered by the provider, never by the control plane; the call's
// usage line goes out as a usage.report; the key's rate limit answers 429 from Go; a hand-off reaches the control plane.
func TestChatOnTheGoEngine(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || string(b) != `{"model":"llama-9","messages":[]}` {
			t.Errorf("provider got %s %s", r.URL.Path, b)
		}
		_, _ = io.WriteString(w, `{"id":"c1","usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer provider.Close()
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "control plane")
	}))
	defer cp.Close()
	port := freePort(t)
	r := drive(t)
	r.next(t, "hello")
	r.send(`{"v":1,"type":"hello.ok","seq":1,"ts":0,"re":"h1","body":{"protocol":1,"cpVersion":"test"}}`)
	r.send(chatConfig(2, 1, port, portOf(cp.URL), provider.URL+"/v1", "zp_engineTestSecret"))
	r.send(secretsLine(3, 1))
	r.send(fmt.Sprintf(`{"v":1,"type":"budget.snapshot","seq":4,"ts":%d,"body":{"at":%d,"budgets":[],"keyCenter":{}}}`,
		time.Now().UnixMilli(), time.Now().UnixMilli()))
	r.next(t, "ready")
	post := func(secret, body string) (int, string) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if code, body := post("zp_engineTestSecret", `{"model":"pv/llama-9","messages":[]}`); code != 200 ||
		body != `{"id":"c1","usage":{"prompt_tokens":1000,"completion_tokens":500},"model":"pv/llama-9"}` {
		t.Fatalf("%d %s; stderr %s", code, body, r.stderr)
	}
	u := r.next(t, "usage.report")
	for _, want := range []string{`"keyId":"k1"`, `"provider":"pv"`, `"model":"pv/llama-9"`, `"status":200`,
		`"input":1000`, `"output":500`, `"cost":2000000`, `"local":true`} {
		if !strings.Contains(u, want) {
			t.Errorf("usage.report %s lacks %s", u, want)
		}
	}
	if code, body := post("zp_engineTestSecret", `{"model":"pv/llama-9","messages":[]}`); code != 429 ||
		!strings.Contains(body, "rate limit of 1 requests per minute reached") {
		t.Fatalf("rate limit: %d %s", code, body)
	}
	if code, body := post("zp_unknownKey", `{"model":"pv/llama-9"}`); code != 200 || body != "control plane" {
		t.Fatalf("hand-off: %d %s", code, body)
	}
}

func portOf(u string) int {
	var p int
	_, _ = fmt.Sscanf(u[strings.LastIndexByte(u, ':')+1:], "%d", &p)
	return p
}

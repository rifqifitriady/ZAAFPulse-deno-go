package provauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func pkcs8PEM(t *testing.T, k any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func saJSON(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// parts splits a JWT and checks its RS256 signature with the test key.
func parts(t *testing.T, jwt string) (header, claims string) {
	t.Helper()
	p := strings.Split(jwt, ".")
	if len(p) != 3 {
		t.Fatalf("not a JWT: %s", jwt)
	}
	h, _ := base64.RawURLEncoding.DecodeString(p[0])
	c, _ := base64.RawURLEncoding.DecodeString(p[1])
	sig, _ := base64.RawURLEncoding.DecodeString(p[2])
	sum := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	return string(h), string(c)
}

func TestGoogleServiceAccountPostsASignedJWT(t *testing.T) {
	srv, reqs := endpoint(t, func(int, seen) (int, string) { return 200, `{"access_token":"sa-1","expires_in":3600}` })
	now := t0.Add(1500 * time.Millisecond) // iat is the floor of the seconds
	sa := saJSON(t, map[string]any{"client_email": "sa@example.test", "private_key": pkcs8PEM(t, testKey), "token_uri": srv.URL + "/token"})
	src := GoogleServiceAccount(secretOf(sa), "https://www.googleapis.com/auth/cloud-platform", srv.Client(),
		func() time.Time { return now }, WithID("vertex"))
	var _ Source = src
	tok, exp, err := src.Token(context.Background())
	if err != nil || tok != "sa-1" || !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("%q %v %v", tok, exp, err)
	}
	form, err := url.ParseQuery((*reqs)[0].body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix((*reqs)[0].body, "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=") {
		t.Fatalf("body %s", (*reqs)[0].body)
	}
	h, c := parts(t, form.Get("assertion"))
	if h != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header %s", h)
	}
	iat := t0.Unix() + 1
	want := fmt.Sprintf(`{"iss":"sa@example.test","scope":"https://www.googleapis.com/auth/cloud-platform","aud":"%s/token","iat":%d,"exp":%d}`,
		srv.URL, iat, iat+3600)
	if c != want {
		t.Fatalf("claims %s\nwant   %s", c, want)
	}
}

func TestSignJWT(t *testing.T) {
	key := pkcs8PEM(t, testKey)
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testKey)}))
	const def = "https://oauth2.googleapis.com/token"
	cases := []struct {
		sa         map[string]any
		aud, claim string
	}{
		{map[string]any{"client_email": "a@example.test", "private_key": key}, def, `{"iss":"a@example.test","scope":"s",`},
		{map[string]any{"client_email": "a@example.test", "private_key": key, "token_uri": nil}, def, `{"iss":"a@example.test",`},
		{map[string]any{"client_email": "a@example.test", "private_key": key, "token_uri": ""}, def, `{"iss":"a@example.test",`},
		{map[string]any{"client_email": "a@example.test", "private_key": pkcs1, "token_uri": "https://t.example.test/x"}, "https://t.example.test/x", `{"iss":"a@example.test",`},
		{map[string]any{"private_key": key}, def, `{"scope":"s",`},
		{map[string]any{"client_email": nil, "private_key": key}, def, `{"iss":null,"scope":"s",`},
		{map[string]any{"client_email": "q\"\\\u2028<>&é@example.test", "private_key": key}, def, "{\"iss\":\"q\\\"\\\\\u2028<>&é@example.test\","},
	}
	for i, c := range cases {
		aud, jwt, err := signJWT(saJSON(t, c.sa), "s", t0)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		_, claims := parts(t, jwt)
		if aud != c.aud || !strings.HasPrefix(claims, c.claim) || !strings.Contains(claims, `"aud":`+jsString(c.aud)+`,`) {
			t.Errorf("case %d: aud %s, claims %s", i, aud, claims)
		}
	}
}

func TestGoogleServiceAccountErrors(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	run := func(raw string) string {
		_, _, err := GoogleServiceAccount(secretOf(raw), "s", nil, nil, WithID("vertex")).Token(context.Background())
		if err == nil {
			return ""
		}
		return err.Error()
	}
	cases := []struct{ got, want string }{
		{run(""), "vertex: no credential stored for google-sa"},
		{run("not json"), "vertex: the credential is not a service account JSON"},
		{run(`[1]`), "vertex: the credential is not a service account JSON"},
		{run(saJSON(t, map[string]any{"client_email": "a@example.test", "private_key": pkcs8PEM(t, ec)})),
			"vertex: the service account's private_key is not an RSA private key in PEM"},
		{run(saJSON(t, map[string]any{"client_email": "a@example.test"})),
			"vertex: the service account's private_key is not an RSA private key in PEM"},
		{run(saJSON(t, map[string]any{"client_email": 5, "private_key": pkcs8PEM(t, testKey)})),
			"vertex: the service account's client_email is not a string"},
		{run(saJSON(t, map[string]any{"token_uri": true, "private_key": pkcs8PEM(t, testKey)})),
			"vertex: the service account's token_uri is not a string"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q, want %q", i, c.got, c.want)
		}
	}
}

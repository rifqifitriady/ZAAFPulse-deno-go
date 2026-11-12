package provauth

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A Cached holding a token, a refresh token and a secret never prints them through fmt (as ipc.SecretsHandoff).
func TestCachedNeverPrintsSecrets(t *testing.T) {
	srv, _ := endpoint(t, func(int, seen) (int, string) {
		return 200, `{"access_token":"access-SECRET-1","refresh_token":"refresh-SECRET-2","expires_in":3600}`
	})
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c", Grant: "refresh_token"},
		secretOf(`{"client_secret":"client-SECRET-3","refresh_token":"rt-SECRET-4"}`), srv.Client(), nil, WithID("azure"))
	if _, _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("%v|%+v|%#v|%s", src, src, src, src)
	if strings.Contains(out, "SECRET") {
		t.Fatalf("a secret is printed: %s", out)
	}
	if !strings.Contains(out, "azure") {
		t.Fatalf("the id helps a reader: %s", out)
	}
}

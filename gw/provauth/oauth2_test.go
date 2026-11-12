package provauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.UnixMilli(1767225600000)

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }

type seen struct {
	header http.Header
	body   string
}

// endpoint is an httptest token endpoint on 127.0.0.1 that records each request and answers with answer(n), n from 1.
func endpoint(t *testing.T, answer func(n int, r seen) (int, string)) (*httptest.Server, *[]seen) {
	t.Helper()
	var mu sync.Mutex
	var reqs []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := seen{r.Header.Clone(), string(b)}
		mu.Lock()
		reqs = append(reqs, s)
		n := len(reqs)
		mu.Unlock()
		status, body := answer(n, s)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func secretOf(s string) Secret { return func() (string, error) { return s, nil } }

func TestOAuth2ClientCredentialsCachesUntilTheLead(t *testing.T) {
	srv, reqs := endpoint(t, func(n int, _ seen) (int, string) {
		return 200, fmt.Sprintf(`{"access_token":"cc-%d","expires_in":3600}`, n)
	})
	clk := &clock{now: t0}
	src := OAuth2(OAuth2Config{TokenURL: srv.URL + "/token", ClientID: "app-1", Scope: "x/.default"},
		secretOf("s3cret"), srv.Client(), clk.Now, WithID("azure"))
	var _ Source = src
	tok, exp, err := src.Token(context.Background())
	if err != nil || tok != "cc-1" || !exp.Equal(t0.Add(time.Hour)) {
		t.Fatalf("first token %q %v %v", tok, exp, err)
	}
	r := (*reqs)[0]
	if r.header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.header.Get("Accept") != "application/json" {
		t.Fatalf("headers %v", r.header)
	}
	if want := "grant_type=client_credentials&client_id=app-1&client_secret=s3cret&scope=x%2F.default"; r.body != want {
		t.Fatalf("body %s, want %s", r.body, want)
	}
	clk.Set(t0.Add(time.Second))
	if tok, _, _ := src.Token(context.Background()); tok != "cc-1" || len(*reqs) != 1 {
		t.Fatalf("a cache hit expected: %q, %d requests", tok, len(*reqs))
	}
	clk.Set(t0.Add(time.Hour - Lead - time.Millisecond)) // just more than Lead left
	if tok, _, _ := src.Token(context.Background()); tok != "cc-1" {
		t.Fatalf("still cached before the lead: %q", tok)
	}
	clk.Set(t0.Add(time.Hour - Lead)) // exactly Lead left: refresh
	if tok, _, _ := src.Token(context.Background()); tok != "cc-2" || len(*reqs) != 2 {
		t.Fatalf("refreshed at the lead: %q, %d requests", tok, len(*reqs))
	}
	if tok, _, _ := src.Refresh(context.Background()); tok != "cc-3" {
		t.Fatalf("forced refresh: %q", tok)
	}
}

func TestOAuth2RefreshTokenRotates(t *testing.T) {
	srv, reqs := endpoint(t, func(n int, _ seen) (int, string) {
		if n == 1 {
			return 200, `{"access_token":"a1","refresh_token":"rt-1","expires_in":60}`
		}
		return 200, fmt.Sprintf(`{"access_token":"a%d","expires_in":60}`, n)
	})
	clk := &clock{now: t0}
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c", Grant: "refresh_token"},
		secretOf(`{"client_secret":"cs","refresh_token":"rt-0"}`), srv.Client(), clk.Now, WithID("p"))
	for range 3 {
		if _, _, err := src.Token(context.Background()); err != nil { // 60 s < Lead: every call refreshes
			t.Fatal(err)
		}
	}
	want := []string{"refresh_token=rt-0", "refresh_token=rt-1", "refresh_token=rt-1"}
	for i, r := range *reqs {
		if !strings.HasSuffix(r.body, want[i]) || !strings.HasPrefix(r.body, "grant_type=refresh_token&client_id=c&client_secret=cs&") {
			t.Fatalf("request %d: %s", i+1, r.body)
		}
	}
}

func TestOAuth2ConcurrentCallersShareOneRefresh(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		_, _ = io.WriteString(w, `{"access_token":"shared","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), nil)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Go(func() {
			tok, _, err := src.Token(context.Background())
			if err == nil && tok != "shared" {
				err = fmt.Errorf("token %q", tok)
			}
			errs <- err
		})
	}
	for hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests, want 1", hits.Load())
	}
}

func TestOAuth2ACallerThatGivesUpDoesNotCancelTheRefresh(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		_, _ = io.WriteString(w, `{"access_token":"late","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, err := src.Token(ctx); done <- err }()
	for hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller got %v", err)
	}
	close(release)
	tok, _, err := src.Token(context.Background())
	if err != nil || tok != "late" || hits.Load() != 1 {
		t.Fatalf("%q %v, %d requests", tok, err, hits.Load())
	}
}

func TestOAuth2RefusesARedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), nil, WithID("azure"))
	_, _, err := src.Token(context.Background())
	if err == nil || err.Error() != "azure: the token endpoint answered with a redirect (307); it is not followed" {
		t.Fatalf("got %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestOAuth2TimesOut(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) })
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), nil, WithID("azure"))
	src.timeout = 50 * time.Millisecond
	_, _, err := src.Token(context.Background())
	if err == nil || err.Error() != "azure: token endpoint timed out" {
		t.Fatalf("got %v", err)
	}
}

func TestOAuth2Errors(t *testing.T) {
	srv, _ := endpoint(t, func(n int, r seen) (int, string) {
		switch {
		case strings.Contains(r.body, "client_id=echo"):
			return 400, `{"error":"bad client_secret s3cret-value"}`
		case strings.Contains(r.body, "client_id=none"):
			return 200, `{"token_type":"bearer"}`
		case strings.Contains(r.body, "client_id=html"):
			return 502, `<html>bad gateway</html>`
		}
		return 401, `{"error":"invalid_client"}`
	})
	run := func(cfg OAuth2Config, secret Secret, opts ...Option) string {
		cfg.TokenURL = srv.URL
		_, _, err := OAuth2(cfg, secret, srv.Client(), nil, append([]Option{WithID("azure")}, opts...)...).Token(context.Background())
		if err == nil {
			return ""
		}
		return err.Error()
	}
	cases := []struct{ got, want string }{
		{run(OAuth2Config{ClientID: "c"}, secretOf("s")), "azure: token refresh failed (401 invalid_client)"},
		{run(OAuth2Config{ClientID: "echo"}, secretOf("s3cret-value"), WithMask(MaskValues([]string{"s3cret-value", ""}))),
			"azure: token refresh failed (400 bad client_secret ***)"},
		{run(OAuth2Config{ClientID: "none"}, secretOf("s")), "azure: token refresh failed (200)"},
		{run(OAuth2Config{ClientID: "html"}, secretOf("s")), "azure: token refresh failed (502)"},
		{run(OAuth2Config{ClientID: "c"}, secretOf("")), "azure: no credential stored for oauth2"},
		{run(OAuth2Config{ClientID: "c", Grant: "refresh_token"}, secretOf("plain")), "azure: no refresh_token in the stored credential"},
		{run(OAuth2Config{ClientID: "c"}, func() (string, error) { return "", errors.New("azure credential: not found") }),
			"azure credential: not found"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q, want %q", i, c.got, c.want)
		}
	}
}

func TestOAuth2KeepsTheTokenAfterAFailedRefresh(t *testing.T) {
	srv, _ := endpoint(t, func(n int, _ seen) (int, string) {
		if n == 2 {
			return 503, `{}`
		}
		return 200, fmt.Sprintf(`{"access_token":"t%d","expires_in":3600}`, n)
	})
	clk := &clock{now: t0}
	src := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), clk.Now)
	if tok, _, _ := src.Token(context.Background()); tok != "t1" {
		t.Fatal(tok)
	}
	if _, _, err := src.Refresh(context.Background()); err == nil {
		t.Fatal("the forced refresh should fail")
	}
	if tok, _, err := src.Token(context.Background()); tok != "t1" || err != nil {
		t.Fatalf("the cached token is kept: %q %v", tok, err)
	}
}

// The stored secret: a JSON object gives its fields; text that is not JSON is the client secret itself; JSON that is
// not an object (a number, a string, null) gives no field, as the JS does for all but null (deviation 5).
func TestOAuth2StoredSecretForms(t *testing.T) {
	srv, reqs := endpoint(t, func(int, seen) (int, string) { return 200, `{"access_token":"x"}` })
	for _, s := range []string{`{"client_secret":"a b"}`, `{"other":1}`, `123456`, `"quoted"`, `null`, `{"client_secret":5}`} {
		if _, _, err := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf(s), srv.Client(), nil).Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"client_secret=a+b", "", "", "", "", ""}
	for i, r := range *reqs {
		got := strings.TrimPrefix(r.body, "grant_type=client_credentials&client_id=c")
		got = strings.TrimPrefix(got, "&")
		if got != want[i] {
			t.Errorf("secret %d: body rest %q, want %q", i, got, want[i])
		}
	}
}

func TestExpiresIn(t *testing.T) {
	cases := map[string]time.Duration{
		`{"access_token":"x"}`:                          time.Hour,
		`{"access_token":"x","expires_in":0}`:           time.Hour,
		`{"access_token":"x","expires_in":"abc"}`:       time.Hour,
		`{"access_token":"x","expires_in":" 90 "}`:      90 * time.Second,
		`{"access_token":"x","expires_in":"0x10"}`:      16 * time.Second,
		`{"access_token":"x","expires_in":-5}`:          -5 * time.Second,
		`{"access_token":"x","expires_in":1.5}`:         1500 * time.Millisecond,
		`{"access_token":"x","expires_in":"Infinity"}`:  maxExpiry,
		`{"access_token":"x","expires_in":"-Infinity"}`: -maxExpiry,
	}
	for body, want := range cases {
		srv, _ := endpoint(t, func(int, seen) (int, string) { return 200, body })
		_, exp, err := OAuth2(OAuth2Config{TokenURL: srv.URL, ClientID: "c"}, secretOf("s"), srv.Client(), func() time.Time { return t0 }).Token(context.Background())
		if err != nil || exp.Sub(t0) != want {
			t.Errorf("%s: %v %v, want %v", body, exp.Sub(t0), err, want)
		}
	}
}

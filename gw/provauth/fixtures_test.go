package provauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures generated from the JS (scripts/fixtures/gen-provauth.js, which runs lib/gateway/providers.js
// accessToken). Each file runs a sequence of calls against queued token answers; the requests Go sends and the result
// of each call must equal the JS ones after canonical JSON encoding.
var fixtureDir = filepath.Join("..", "testdata", "provauth")

type fixture struct {
	Fn string `json:"fn"`
	In struct {
		ID   string `json:"id"`
		Auth struct {
			Type     string `json:"type"`
			TokenURL string `json:"tokenUrl"`
			ClientID string `json:"clientId"`
			Scope    string `json:"scope"`
			Grant    string `json:"grant"`
		} `json:"auth"`
		Secret    *string  `json:"secret"`
		Mask      []string `json:"mask"`
		Responses []struct {
			Status  int               `json:"status"`
			Body    string            `json:"body"`
			Headers map[string]string `json:"headers"`
		} `json:"responses"`
		Steps []struct {
			Now   int64 `json:"now"`
			Force bool  `json:"force"`
		} `json:"steps"`
	} `json:"in"`
	Out json.RawMessage `json:"out"`
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func canonical(t *testing.T, v any) string {
	t.Helper()
	if raw, ok := v.(json.RawMessage); ok {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		v = decoded
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestFixturesFromJS(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if len(files) == 0 {
		t.Fatalf("no fixtures in %s: run node scripts/fixtures/gen-provauth.js", fixtureDir)
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".json"), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var fx fixture
			if err := json.Unmarshal(data, &fx); err != nil || fx.Fn != "accessToken" {
				t.Fatalf("%v (fn %q)", err, fx.Fn)
			}
			in := fx.In
			var mu sync.Mutex
			requests := []any{}
			queue := in.Responses
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, map[string]any{
					"url": r.URL.String(), "method": r.Method, "body": string(body),
					"headers": map[string]any{"content-type": r.Header.Get("Content-Type"), "accept": r.Header.Get("Accept")},
				})
				if len(queue) == 0 {
					return nil, errors.New("no response queued")
				}
				q := queue[0]
				queue = queue[1:]
				h := http.Header{}
				for k, v := range q.Headers {
					h.Set(k, v)
				}
				return &http.Response{StatusCode: q.Status, Header: h, Body: io.NopCloser(strings.NewReader(q.Body)), Request: r}, nil
			})}
			var now time.Time
			clk := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
			secret := func() (string, error) {
				if in.Secret == nil {
					return "", nil
				}
				return *in.Secret, nil
			}
			opts := []Option{WithID(in.ID), WithMask(MaskValues(in.Mask))}
			var src *Cached
			if in.Auth.Type == "oauth2" {
				src = OAuth2(OAuth2Config{TokenURL: in.Auth.TokenURL, ClientID: in.Auth.ClientID, Scope: in.Auth.Scope,
					Grant: in.Auth.Grant}, secret, client, clk, opts...)
			} else {
				src = GoogleServiceAccount(secret, in.Auth.Scope, client, clk, opts...)
			}
			results := []any{}
			for _, step := range in.Steps {
				mu.Lock()
				now = time.UnixMilli(step.Now)
				mu.Unlock()
				get := src.Token
				if step.Force {
					get = src.Refresh
				}
				tok, exp, err := get(context.Background())
				if err != nil {
					results = append(results, map[string]any{"throws": err.Error()})
				} else {
					results = append(results, map[string]any{"token": tok, "exp": exp.UnixMilli()})
				}
			}
			if len(queue) != 0 {
				t.Errorf("%d responses left unused", len(queue))
			}
			got := canonical(t, map[string]any{"requests": requests, "results": results})
			if want := canonical(t, fx.Out); got != want {
				t.Fatalf("Go differs from the JS\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

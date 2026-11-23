package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// P7 review M1: the cloud metadata service is refused in Go in every mode (report, enforce, public) and on every path
// (the first URL, a followed redirect hop, /check, CONNECT, the dialer, the engine's own client), whatever the
// allow-list says. No test reaches a real metadata address: the refusal happens before any connect, and the endpoint's
// connect limit is short so a regression fails fast instead of waiting on a real network.

// metadataTargets are the URLs a redirect may point at; every one must be refused.
var metadataTargets = []string{
	"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
	"http://169.254.170.2/v2/credentials/x",
	"http://100.100.100.200/latest/meta-data/",
	"http://[fd00:ec2::254]/latest/meta-data/",
	"http://[::ffff:169.254.169.254]/latest/meta-data/", // IPv4-mapped is the same address
	"http://[::ffff:a9fe:a9fe]/latest/",
	"http://metadata.google.internal/computeMetadata/v1/",
	"http://METADATA.GOOGLE.INTERNAL./computeMetadata/v1/", // case and a trailing dot change nothing
	"http://metadata/computeMetadata/v1/",
	"http://metadata.goog/computeMetadata/v1/",
	"http://instance-data/latest/meta-data/",
	"http://instance-data.ec2.internal/latest/meta-data/",
	"http://imds.meta.test/latest/meta-data/", // a name that resolves to 169.254.169.254: refused at the dialer
}

// metaLookup is fakeLookup plus *.meta.test, which resolves to the AWS metadata address.
func metaLookup(ctx context.Context, host string) ([]net.IP, error) {
	if strings.HasSuffix(host, ".meta.test") {
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	return fakeLookup(ctx, host)
}

func startMetaEgress(t *testing.T, pol Policy) (*Server, string, *recorder) {
	t.Helper()
	s, base, rec := startEgress(t, pol)
	s.lookup = metaLookup
	s.connectTimeout = 300 * time.Millisecond
	return s, base, rec
}

// metaPolicies are the modes the refusal must hold in. The enforce policy lists every metadata name and address: the
// allow-list cannot open them.
var metaPolicies = map[string]Policy{
	"report": {Mode: ModeReport},
	"enforce listing them": {Mode: ModeEnforce, Allow: []Rule{{Host: "*.test"}, {Host: "127.0.0.1"},
		{Host: "169.254.169.254"}, {Host: "169.254.170.2"}, {Host: "100.100.100.200"}, {Host: "fd00:ec2::254"},
		{Host: "::ffff:169.254.169.254"}, {Host: "::ffff:a9fe:a9fe"}, {Host: "metadata.google.internal"},
		{Host: "metadata.google.internal."}, {Host: "metadata"}, {Host: "metadata.goog"}, {Host: "instance-data"},
		{Host: "instance-data.ec2.internal"}, {Host: "*.meta.test"}}},
}

func TestMetadataRefusedOnARedirectHop(t *testing.T) {
	for name, pol := range metaPolicies {
		for _, public := range []bool{false, true} {
			_, base, rec := startMetaEgress(t, pol)
			reached := make(chan string, 1)
			// The first hop is a local fake that redirects to the metadata URL (as a hostile remote agent would).
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if to := r.URL.Query().Get("to"); to != "" {
					http.Redirect(w, r, to, http.StatusFound)
					return
				}
				reached <- r.URL.Path
			}))
			for _, to := range metadataTargets {
				h := map[string]string{"Authorization": "Bearer k", "Cookie": "s=1"}
				first := ts.URL + "/start?to=" + to
				if public {
					// Public mode refuses 127.0.0.1 itself; the first hop is then the redirect target directly.
					h["X-ZP-Egress-Public"] = "1"
					first = to
				}
				resp, err := sendErr(context.Background(), base, "POST", first, strings.NewReader("{}"), h)
				if err != nil {
					t.Fatal(err)
				}
				body := readAll(t, resp)
				what := name + " public=" + strconv.FormatBool(public) + " " + to
				if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("X-ZP-Egress-Error") != CodePrivate {
					t.Errorf("%s: %d %q %s", what, resp.StatusCode, resp.Header.Get("X-ZP-Egress-Error"), body)
				}
				if !strings.Contains(body, "metadata") {
					t.Errorf("%s: message %s", what, body)
				}
				// The refusal is recorded "refused" EGRESS_PRIVATE (never "would-refuse": report mode does not apply).
				var last Record
				for _, r := range rec.drain() {
					// The allow-list verdict comes before resolution, so a name that only resolves to the metadata
					// service may be recorded would-refuse first in report mode; its connect is still refused.
					if r.Outcome == OutcomeWouldRefuse && r.Host != "127.0.0.1" && !strings.HasSuffix(r.Host, ".meta.test") {
						t.Errorf("%s: a would-refuse for the metadata hop %+v", what, r)
					}
					last = r
				}
				if last.Outcome != OutcomeRefused || last.Error != CodePrivate {
					t.Errorf("%s: last record %+v", what, last)
				}
			}
			select {
			case p := <-reached:
				t.Errorf("%s: the fake was reached at %s, not only redirected from", name, p)
			default:
			}
			ts.Close()
		}
	}
}

func TestMetadataRefusedByCheck(t *testing.T) {
	for name, pol := range metaPolicies {
		_, base, rec := startMetaEgress(t, pol)
		for _, to := range metadataTargets {
			if strings.Contains(to, ".meta.test") {
				continue // /check resolves nothing; a name is judged by its text
			}
			req, _ := http.NewRequest(http.MethodPost, base+"check", nil)
			req.Header.Set("X-ZP-Egress-Token", testToken)
			req.Header.Set("X-ZP-Egress-URL", to)
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			readAll(t, resp)
			if resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-ZP-Egress-Error") != CodePrivate {
				t.Errorf("%s %s: %d %q", name, to, resp.StatusCode, resp.Header.Get("X-ZP-Egress-Error"))
			}
			if r := rec.wait(t, 1)[0]; r.Outcome != OutcomeWouldRefuse || r.Error != CodePrivate || r.Via != ViaCheck {
				t.Errorf("%s %s: record %+v", name, to, r)
			}
		}
	}
}

func TestMetadataRefusedByConnect(t *testing.T) {
	for name, pol := range metaPolicies {
		_, base, rec := startMetaEgress(t, pol)
		for _, hp := range []string{"169.254.169.254:80", "[fd00:ec2::254]:80", "100.100.100.200:80",
			"169.254.170.2:80", "[::ffff:169.254.169.254]:80", "metadata.google.internal:80", "metadata.:80",
			"imds.meta.test:5432"} {
			c, _, status := openTunnel(t, base, hp, testToken)
			_ = c.Close()
			if status != "502 Bad Gateway|"+CodePrivate {
				t.Errorf("%s %s: %q", name, hp, status)
			}
			recs := rec.drain()
			if len(recs) == 0 || recs[len(recs)-1].Outcome != OutcomeRefused || recs[len(recs)-1].Error != CodePrivate {
				t.Errorf("%s %s: records %+v", name, hp, recs)
			}
			for _, r := range recs {
				if r.Outcome == OutcomeWouldRefuse && !strings.HasSuffix(r.Host, ".meta.test") {
					t.Errorf("%s %s: would-refuse %+v", name, hp, r)
				}
			}
		}
	}
}

// The dialer refuses a metadata address in every mode, before the connect: names are resolved and every address is
// checked, and the Control hook checks the address actually dialled.
func TestMetadataRefusedByTheDialer(t *testing.T) {
	s, _, _ := startMetaEgress(t, Policy{Mode: ModeReport})
	for _, public := range []bool{false, true} {
		for _, addr := range []string{"169.254.169.254:80", "[fd00:ec2::254]:443", "imds.meta.test:80",
			"metadata.google.internal:80", "metadata:80"} {
			_, err := s.dial(context.Background(), "tcp", addr, public)
			code, msg := errorCode(err)
			if code != CodePrivate || !strings.Contains(msg, "metadata") {
				t.Errorf("public=%v %s: %q %q", public, addr, code, msg)
			}
		}
	}
	for _, addr := range []string{"169.254.169.254:80", "[::ffff:169.254.169.254]:80", "[fd00:ec2::254]:80",
		"100.100.100.200:80", "169.254.170.2:80", "[64:ff9b::a9fe:a9fe]:80"} {
		if err := RefuseMetadata("tcp", addr, nil); err == nil {
			t.Errorf("Control hook let %s through", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "93.184.215.14:443", "169.254.1.1:80"} {
		if err := RefuseMetadata("tcp", addr, nil); err != nil {
			t.Errorf("Control hook refused %s: %v", addr, err)
		}
	}
}

// The engine's own client (RecordingTransport) refuses a metadata host whatever the policy: nothing is sent.
func TestMetadataRefusedByTheEngineTransport(t *testing.T) {
	sent := false
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { sent = true; return nil, errors.New("sent") })
	for _, pol := range []*Policy{nil, {Mode: ModeReport}, metaPoliciesPtr("enforce listing them")} {
		rec := newRecorder()
		client := &http.Client{Transport: RecordingTransport(rt, "gateway", func() *Policy { return pol }, rec.add)}
		for _, to := range metadataTargets {
			if strings.Contains(to, ".meta.test") {
				continue // resolved by the engine's own dialer (the gateway sets RefuseMetadata on it)
			}
			_, err := client.Post(to, "application/json", strings.NewReader("{}"))
			var eg interface{ EgressCode() string }
			if err == nil || !errors.As(err, &eg) || eg.EgressCode() != CodePrivate {
				t.Errorf("%v %s: %v", pol, to, err)
			}
			if r := rec.wait(t, 1)[0]; r.Outcome != OutcomeRefused || r.Error != CodePrivate || r.Via != ViaEngine {
				t.Errorf("%s: record %+v", to, r)
			}
		}
	}
	if sent {
		t.Fatal("a metadata request reached the inner transport")
	}
}

func TestIsMetadataHost(t *testing.T) {
	for _, h := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254",
		"FD00:EC2:0:0:0:0:0:254", "[fd00:ec2::254]", "::ffff:169.254.169.254", "::ffff:a9fe:a9fe",
		"64:ff9b::a9fe:a9fe", "metadata.google.internal", "Metadata.Google.Internal.", "metadata", "metadata.",
		"metadata.goog", "instance-data", "instance-data.ec2.internal", "fd00:ec2::254%en0"} {
		if !IsMetadataHost(h) {
			t.Errorf("%q is not seen as metadata", h)
		}
	}
	for _, h := range []string{"", "127.0.0.1", "169.254.169.253", "example.com", "metadata.example.com",
		"google.internal", "fd00:ec2::255", "100.100.100.201"} {
		if IsMetadataHost(h) {
			t.Errorf("%q is seen as metadata", h)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func metaPoliciesPtr(name string) *Policy {
	p := metaPolicies[name]
	return &p
}

// drain returns the records that have arrived, waiting briefly for a straggler.
func (r *recorder) drain() []Record {
	var out []Record
	for {
		select {
		case rec := <-r.ch:
			out = append(out, rec)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}

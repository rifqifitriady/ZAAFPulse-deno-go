package engine

import (
	"net/http"
	"strings"
	"testing"
)

func TestStartTrace(t *testing.T) {
	const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	cases := []struct {
		name    string
		tp      []string
		ts      []string
		cont    bool // the incoming trace is continued
		sampled bool
		state   string
	}{
		{"none", nil, nil, false, true, ""},
		{"valid", []string{tp}, []string{"a=1"}, true, true, "a=1"},
		{"unsampled", []string{strings.TrimSuffix(tp, "01") + "00"}, nil, true, false, ""},
		{"two headers are joined and refused", []string{tp, tp}, nil, false, true, ""},
		{"surrounding spaces", []string{" \t" + tp + " "}, nil, true, true, ""},
		{"uppercase hex", []string{strings.ToUpper(tp)}, nil, false, true, ""},
		{"two tracestate headers joined", []string{tp}, []string{"a=1", "b=2"}, true, true, "a=1, b=2"},
		{"tracestate over 512", []string{tp}, []string{strings.Repeat("x", 513)}, true, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range c.tp {
				h.Add("Traceparent", v)
			}
			for _, v := range c.ts {
				h.Add("Tracestate", v)
			}
			tc := StartTrace(h, counterIDs())
			if got := tc.TraceID == "0af7651916cd43dd8448eb211c80319c"; got != c.cont || tc.Remote != c.cont {
				t.Fatalf("continued %v, remote %v, want %v", got, tc.Remote, c.cont)
			}
			if tc.Sampled != c.sampled || tc.TraceState != c.state {
				t.Fatalf("sampled %v state %q", tc.Sampled, tc.TraceState)
			}
			if len(tc.SpanID) != 16 || strings.Trim(tc.SpanID, "0") == "" {
				t.Fatalf("span id %q", tc.SpanID)
			}
		})
	}
}

func TestRandomIDsAreHexAndNeverZero(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := RandomIDs(8)
		if len(id) != 16 || strings.Trim(id, "0") == "" || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("id %q", id)
		}
	}
}

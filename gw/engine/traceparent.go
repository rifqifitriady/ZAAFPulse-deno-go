package engine

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
)

// SpanIDs draws an id of n random bytes as lowercase hex, never all zero (lib/otel.js newId).
type SpanIDs func(n int) string

// RandomIDs is the engine's SpanIDs.
func RandomIDs(n int) string {
	b := make([]byte, n)
	for {
		_, _ = rand.Read(b)
		for _, x := range b {
			if x != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}

// TraceCtx is a request's trace context (P4f D6): lib/otel.js requestContext's ctx.self (TraceID, SpanID: the server
// span's id, Sampled, TraceState) and ctx.parent (ParentID and Remote: the caller's span, from its traceparent).
type TraceCtx struct {
	TraceID, SpanID, ParentID, TraceState string
	Sampled, Remote                       bool
}

// W3C Trace Context level 1: version-traceid-parentid-flags, lowercase hex; a future version may append fields.
var traceparentRe = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$`)

// StartTrace is requestContext's context: the request's traceparent continued when it is valid, else a new sampled
// trace. The ids are drawn in requestContext's order: the trace id (a new trace only), then the server span's id.
// Node joins repeated traceparent and tracestate headers with ", ", which the parse then reads as one value.
func StartTrace(h http.Header, ids SpanIDs) TraceCtx {
	var tc TraceCtx
	if tp := h.Values("Traceparent"); len(tp) > 0 {
		if in, ok := parseTraceparent(strings.Join(tp, ", ")); ok {
			tc.TraceID, tc.ParentID, tc.Sampled, tc.Remote = in.traceID, in.spanID, in.sampled, true
			if ts := h.Values("Tracestate"); len(ts) > 0 {
				tc.TraceState = cleanTracestate(strings.Join(ts, ", "))
			}
		}
	}
	if tc.TraceID == "" {
		tc.TraceID, tc.Sampled = ids(16), true
	}
	tc.SpanID = ids(8)
	return tc
}

type incoming struct {
	traceID, spanID string
	sampled         bool
}

// parseTraceparent is lib/otel.js parseTraceparent: the value trimmed (String.prototype.trim), version ff refused,
// version 00 with appended fields refused, all-zero ids refused; sampled is the low bit of the flags.
func parseTraceparent(v string) (incoming, bool) {
	m := traceparentRe.FindStringSubmatch(jsTrim(v))
	if m == nil || m[1] == "ff" || (m[1] == "00" && m[5] != "") || allZeroHex(m[2]) || allZeroHex(m[3]) {
		return incoming{}, false
	}
	return incoming{traceID: m[2], spanID: m[3], sampled: hexNibble(m[4][1])&1 == 1}, true
}

// cleanTracestate is lib/otel.js cleanTracestate: kept when it is at most 512 UTF-16 units of printable ASCII, then
// trimmed; else dropped.
func cleanTracestate(v string) string {
	if utf16Len(v) > 512 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return ""
		}
	}
	return strings.Trim(v, " ")
}

func allZeroHex(s string) bool { return strings.Trim(s, "0") == "" }

func hexNibble(c byte) byte {
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - '0'
}

// jsTrim is String.prototype.trim: WhiteSpace and LineTerminator code points at both ends.
func jsTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

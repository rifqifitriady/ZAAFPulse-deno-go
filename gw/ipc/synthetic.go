package ipc

import (
	"encoding/json"
	"fmt"
)

// DefaultEmitBase is the ts of the stub's first synthetic round (2026-01-01T00:00:00Z) when --emit-base is not given.
const DefaultEmitBase int64 = 1767225600000

// Synthetic is the event the stub's --emit sends as sequence seq: rounds of a usage.report, an audit.event and a
// trace.span (round r = (seq-1)/3 + 1). The audit event is a governance entry in odd rounds and an MCP tool call in
// even ones. Everything is derived from seq, so a restarted stub emits the same stream and a test can rebuild every
// event (test/ipc-stub.js synthetic() is the JS twin).
func Synthetic(seq, base int64) (string, any) {
	round := (seq-1)/3 + 1
	ts := base + round
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	switch (seq - 1) % 3 {
	case 0:
		stream := false
		return TypeUsageReport, &UsageReport{Record: UsageRecord{
			Ts: ts, KeyID: "k-emit", Provider: "stub", Model: "stub/model-a", Kind: str("chat"), Format: str("openai"),
			Stream: &stream, Status: 200, Ms: 12, Input: i64(1000), Output: i64(500), Cached: i64(0), Cost: 1_500_000,
			SessionID: str(fmt.Sprintf("emit-%d", round)),
		}}
	case 1:
		if round%2 == 1 {
			e := AuditEvent{
				Action: "key.update",
				Target: &AuditTarget{Type: "key", ID: str("k-emit"), Name: str(fmt.Sprintf("emit %d", round))},
				Actor:  &AuditActor{Via: "apikey", KeyID: str("k-emit")},
			}
			raw, _ := json.Marshal(e)
			return TypeAuditEvent, &AuditEventBody{Log: "governance", Event: raw, governance: &e}
		}
		e := MCPAuditEntry{Ts: ts, Integration: "stub", Tool: "echo", OK: true, Ms: 3, Args: []string{"text"}, KeyID: str("k-emit")}
		raw, _ := json.Marshal(e)
		return TypeAuditEvent, &AuditEventBody{Log: "mcp", Event: raw, mcp: &e}
	default:
		return TypeTraceSpan, &TraceSpanBody{Span: SpanRecord{
			T: fmt.Sprintf("%032x", round), S: fmt.Sprintf("%016x", round), N: "chat stub/model-a", K: "client",
			St: ts * 1000, D: 12000, OK: true, Src: "gateway", Svc: "zaafpulse",
			A: map[string]any{"gen_ai.request.model": "model-a"},
		}}
	}
}

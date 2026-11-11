package ipc

import (
	"testing"
)

// Every synthetic event is a valid v1 message under the strict decoder, for both kinds of audit event.
func TestSyntheticEventsAreValid(t *testing.T) {
	want := []string{TypeUsageReport, TypeAuditEvent, TypeTraceSpan, TypeUsageReport, TypeAuditEvent, TypeTraceSpan}
	for seq := int64(1); seq <= 6; seq++ {
		typ, body := Synthetic(seq, DefaultEmitBase)
		if typ != want[seq-1] {
			t.Fatalf("seq %d: %s", seq, typ)
		}
		line, err := Encode(Envelope{V: Version, Type: typ, Seq: seq, Ts: 1}, body)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := Decode(line, true)
		if err != nil {
			t.Fatalf("seq %d: %v\n%s", seq, err, line)
		}
		if a, ok := msg.Body.(*AuditEventBody); ok {
			if (seq == 2) != (a.Governance() != nil) || (seq == 5) != (a.MCP() != nil) {
				t.Fatalf("seq %d: round 1 is governance, round 2 is mcp", seq)
			}
		}
	}
}

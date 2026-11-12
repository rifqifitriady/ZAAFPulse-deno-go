# P4f: the order of store, mark file and ack on a power loss

Date: 2026-11-12. Branch `migrate/p4-lib-ipc-auth` (P4 stage 1, stream C). P4f entry condition 4 (P4a results,
"P4b (and P4f) entry conditions"; final review T2-a): "fsync the store before the mark file, or accept and document
the power-loss order". This record is the decision: **no fsync; the order is accepted and documented here.** It is
the owner's to reverse.

## What the chain writes

For each gateway event (`usage.report`, `audit.event`, `trace.span`):

1. Go appends it to an outbox segment (`gw/ipc/outbox.go`), then sends it.
2. The control plane stores it through a strict writer: the usage log, the governance chain, the MCP audit log or the
   trace store (`lib/ipc/ingest.js`, `lib/ipc/server-sinks.js`). Each stored line carries `"ipc": { stream, seq }`.
3. Before each ack the control plane writes the mark file `.data/ipc-marks.json` (temp file, then rename;
   `lib/ipc/markfile.js`), then sends the ack.
4. Go writes `stream.json` (`acked`) and deletes the segments that are done.

None of these writes is fsynced: not the outbox segments, not `stream.json`, not the four stores, not the mark file.
The `js` engine's own stores are not fsynced either. That is the durability target set in P3 D4 and kept since: **a
killed process, not a power loss** (`gw/ipc/outbox.go` header).

## A killed process: the order holds

When either process is killed (or crashes), the operating system still holds every write that completed, in the order
the process made them. A mark is written only after the store returned, and an ack only after the mark, so the mark
file never runs ahead of the stores and every acknowledged event is stored. On the next start the mark is the larger
of the mark file and the stores' tails, and the outbox replays from `acked + 1`; events at or below the mark are
skipped. Exactly once holds. Tests: `test/ipc-kill.test.js` (control plane killed), `test/front-kill.test.js` (gateway
killed during a stream), `test/ipc-hardening.test.js` (acks wait for the mark file), `test/ipc-marks-session.test.js`
(marks re-read per session, the mark file never lowered).

## A power loss: three holes, not one

After a power loss the disk may hold any prefix of each file's unsynced writes, independently per file:

1. **The mark file ahead of a store** (T2-a): the new mark survives, the store's last lines do not. The replay skips
   those events as duplicates: they are lost.
2. **The outbox behind what was sent**: an event was sent and stored, but neither its segment line nor the store line
   survives. Nothing can resend it: lost.
3. **The outbox's seqs rolled back**: `stream.json` and the segments return to an older state while the mark file
   (or a store line) keeps a higher mark. New events reuse seqs that are already marked and are skipped as
   duplicates: lost.

Fsyncing the stores before the mark closes hole 1 only. Holes 2 and 3 need an fsync on the gateway's request path
(every outbox append before its send, and every `stream.json` write), plus the stores and the mark file on every ack:
a per-request disk flush on the data plane and a per-batch flush on the control plane. That is a program-level cost
decision, not a P4f one.

## Consequence

On a power loss the chain is **at most once for the last moments** before the cut (the writes the OS had not flushed,
typically a few seconds): those events can be lost. **No event is ever counted twice**: every hole above loses an
event, none stores one again. The `js` engine loses the same window (its stores are not fsynced), so the `go` engine
keeps parity with it.

## Reversing it

If power-loss durability is wanted, it is one change across the chain, decided once: fsync each outbox append before
its send and each `stream.json` save (Go); fsync each IPC store append before the mark, and the mark file before the
ack (JS); measure the cost on the gateway benchmark first (`bench/front-overhead.js`).

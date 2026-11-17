package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotDir copies every regular file of dir into a new folder: what a kill right now leaves on disk.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	out := t.TempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == lockName {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// mustBox and mustJournal open an outbox and a journal and close them when the test ends, on failure too (final review
// m9: on Windows an open handle makes the temp folder's removal fail and hides the real failure).
func mustBox(t *testing.T, dir string) *Outbox {
	t.Helper()
	box, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Close() })
	return box
}

func mustJournal(t *testing.T, dir string) *CallJournal {
	t.Helper()
	j, err := OpenCallJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func modelRecord(i int) UsageRecord {
	return UsageRecord{KeyID: "k", Provider: "p", Model: fmt.Sprintf("m%d", i)}
}

// usageModels reads every usage.report line in the outbox at dir from seq from on, by model.
func usageModels(t *testing.T, box *Outbox, from int64) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := box.ReadFrom(from, func(_ int64, line []byte) error {
		var env struct {
			Type string `json:"type"`
			Body struct {
				Record UsageRecord `json:"record"`
			} `json:"body"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return err
		}
		if env.Type == TypeUsageReport {
			out[env.Body.Record.Model]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func openedIn(t *testing.T, dir string) int {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, CallsFile))
	return bytes.Count(b, []byte(`{"o":`))
}

// TestJournalExactlyOneLineAtEveryCrashPoint is P4f D14 at every write boundary: six calls open, four get their
// line (two of them acknowledged, their segments pruned), two are in flight. A kill after any single file write
// (journal line, outbox line, stream.json, segment removal) then a restart and Recover leaves exactly one usage line for
// every call opened before the kill, and never two for any call.
func TestJournalExactlyOneLineAtEveryCrashPoint(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	box.SetPerSegment(1) // every line its own segment: an ack prunes them
	j := mustJournal(t, dir)
	var snaps []string
	hook := func() { snaps = append(snaps, snapshotDir(t, dir)) }
	j.afterWrite, box.afterWrite = hook, hook
	sent := map[int64]string{} // seq -> model, for the lines acknowledged (and maybe pruned) in this run
	ids := make([]int64, 6)
	for i := range ids {
		if ids[i] = j.Open(int64(1000+i), modelRecord(i)); ids[i] == 0 {
			t.Fatal("open failed")
		}
	}
	for i := 0; i < 4; i++ {
		id, rec := ids[i], modelRecord(i)
		rec.Status = 200
		seq, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: rec}, 2000,
			func(s int64) error { return j.NoteClose(id, s) }, func() { j.Reopen(id) }, func() { j.Done(id) }, nil)
		if err != nil {
			t.Fatal(err)
		}
		sent[seq] = rec.Model
		if i == 1 {
			if err := box.Ack(2); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = j.Close()
	_ = box.Close()
	if len(snaps) < 12 {
		t.Fatalf("only %d crash points", len(snaps))
	}
	for n, snap := range snaps {
		opened := openedIn(t, snap)
		rbox := mustBox(t, snap)
		rj := mustJournal(t, snap)
		if _, err := rj.Recover(rbox, 9999); err != nil {
			t.Fatalf("crash point %d: recover: %v", n, err)
		}
		counts := usageModels(t, rbox, rbox.Acked()+1)
		for seq := int64(1); seq <= rbox.Acked(); seq++ {
			counts[sent[seq]]++ // acknowledged: stored by the control plane, its segment may be gone
		}
		for i := 0; i < 6; i++ {
			want := 0
			if i < opened {
				want = 1
			}
			if got := counts[fmt.Sprintf("m%d", i)]; got != want {
				t.Fatalf("crash point %d: call %d has %d lines, want %d (counts %v)", n, i, got, want, counts)
			}
		}
		_ = rj.Close()
		_ = rbox.Close()
	}
}

// A kill during Recover (after it noted a close and appended that line) does not recover the call again.
func TestRecoverTwiceRecoversOnce(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	j.Open(1, modelRecord(0))
	j.Open(2, modelRecord(1))
	_ = j.Close()
	_ = box.Close()
	// First recovery, killed after its first line: a snapshot taken at the outbox write of call 0's line.
	box = mustBox(t, dir)
	j = mustJournal(t, dir)
	var snap string
	box.afterWrite = func() {
		if snap == "" {
			snap = snapshotDir(t, dir)
		}
	}
	if _, err := j.Recover(box, 5); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	_ = box.Close()
	box = mustBox(t, snap)
	j = mustJournal(t, snap)
	n, err := j.Recover(box, 6)
	if err != nil || n != 1 {
		t.Fatalf("second recovery wrote %d (%v), want 1: call 1 only", n, err)
	}
	if c := usageModels(t, box, 1); c["m0"] != 1 || c["m1"] != 1 {
		t.Fatalf("lines %v", c)
	}
	_ = j.Close()
	_ = box.Close()
}

func TestRecoveredLine(t *testing.T) {
	in, out, ttft, trace := int64(5), int64(6), 1.5, "t"
	base := UsageRecord{KeyID: "k", Provider: "p", Model: "m", Input: &in, Output: &out, Cost: 7, TTFT: Some(ttft), TraceID: &trace}
	r := Recovered(base, 1234)
	if r.Status != 599 || r.Ms != 0 || r.Ts != 1234 || r.Cost != 0 || r.Error == nil || *r.Error != RecoveredError ||
		r.Input != nil || r.Output != nil || r.TraceID != nil || r.TTFT.Set || r.KeyID != "k" {
		t.Fatalf("%+v", r)
	}
}

func TestNoteFailureAppendsNothing(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	defer box.Close()
	next := box.Next()
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(int64) error { return errors.New("disk full") }, func() { t.Fatal("undo after a failed note") }, func() {}, nil)
	if err == nil || box.Next() != next {
		t.Fatalf("err %v, next %d -> %d", err, next, box.Next())
	}
}

func TestAppendFailureReopens(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	id := j.Open(1, modelRecord(0))
	box.write = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	reopened := false
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(s int64) error { return j.NoteClose(id, s) }, func() { reopened = true; j.Reopen(id) }, func() {}, nil)
	if err == nil || !reopened {
		t.Fatalf("err %v reopened %v", err, reopened)
	}
	box.write = (*os.File).Write
	_ = j.Close()
	_ = box.Close()
	box = mustBox(t, dir)
	defer box.Close()
	j = mustJournal(t, dir)
	defer j.Close()
	if n, err := j.Recover(box, 2); err != nil || n != 1 {
		t.Fatalf("recovered %d (%v), want 1", n, err)
	}
}

// With no call open, a journal past 64 KiB is emptied at the next open.
func TestJournalCompacts(t *testing.T) {
	dir := t.TempDir()
	j := mustJournal(t, dir)
	defer j.Close()
	for i := 0; i < 2000; i++ {
		id := j.Open(int64(i), modelRecord(i))
		if err := j.NoteClose(id, int64(i+1)); err != nil {
			t.Fatal(err)
		}
		j.Done(id)
	}
	st, _ := os.Stat(filepath.Join(dir, CallsFile))
	if st.Size() > 2*journalCompactAt {
		t.Fatalf("journal is %d bytes", st.Size())
	}
}

// P4a T1-c: an ack that prunes segments while a replay reads them: every line is sent once, in order, or the replay
// skips a pruned segment (its events are stored); never twice, never a panic.
func TestPruneDuringReplay(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	defer box.Close()
	box.SetPerSegment(2)
	for i := 0; i < 6; i++ {
		if _, _, err := box.Append(TypeUsageReport, &UsageReport{Record: modelRecord(i)}, 1); err != nil {
			t.Fatal(err)
		}
	}
	gate := make(chan struct{})
	acked := make(chan error, 1)
	var seqs []int64
	first := true
	err := box.Replay(1, func(line []byte) error {
		seq, _ := SeqOf(line)
		seqs = append(seqs, seq)
		if first {
			first = false
			go func() { acked <- box.Ack(4); close(gate) }()
			<-gate
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-acked; err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("order %v", seqs)
		}
	}
	if seqs[len(seqs)-1] != 6 {
		t.Fatalf("the last line was not sent: %v", seqs)
	}
}

// emitOne stores call id's line through the journal as the gateway does.
func emitOne(t *testing.T, j *CallJournal, box *Outbox, id int64, i int) {
	t.Helper()
	rec := modelRecord(i)
	rec.Status, rec.Cost = 200, 7
	if _, err := j.Emit(box, id, TypeUsageReport, &UsageReport{Record: rec}, 1, nil); err != nil {
		t.Fatal(err)
	}
}

// recoverAt reopens a crash snapshot, recovers it and returns the usage lines per model and record.
func recoverAt(t *testing.T, snap string) (map[string]int, []UsageRecord) {
	t.Helper()
	rbox := mustBox(t, snap)
	rj := mustJournal(t, snap)
	if _, err := rj.Recover(rbox, 9999); err != nil {
		t.Fatal(err)
	}
	var recs []UsageRecord
	_ = rbox.ReadFrom(1, func(_ int64, line []byte) error {
		var env struct {
			Body struct {
				Record UsageRecord `json:"record"`
			} `json:"body"`
		}
		_ = json.Unmarshal(line, &env)
		recs = append(recs, env.Body.Record)
		return nil
	})
	return usageModels(t, rbox, 1), recs
}

// Final review I1: call A's close is noted, then call B opens and compacts the journal before A's line is appended; a
// kill right there must still leave A exactly one line (before the fix: zero), and B one.
func TestCompactionBetweenCloseAndAppendKeepsTheCall(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	a := j.Open(1, modelRecord(0))
	j.limit = 0 // the next Open compacts
	var snap string
	var b int64
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(s int64) error {
			if err := j.NoteClose(a, s); err != nil {
				return err
			}
			b = j.Open(2, modelRecord(1)) // a concurrent request, inside A's note
			snap = snapshotDir(t, dir)    // the kill
			return nil
		}, func() { j.Reopen(a) }, func() { j.Done(a) }, nil)
	if err != nil || b == 0 {
		t.Fatal(err, b)
	}
	counts, _ := recoverAt(t, snap)
	if counts["m0"] != 1 || counts["m1"] != 1 {
		t.Fatalf("lines after recovery: %v (want m0 1, m1 1)", counts)
	}
}

// Final review I1, second path: A's append fails after a compaction; Reopen must leave A recoverable (before the fix
// the u line had no o line before it and A got zero lines).
func TestFailedAppendAfterCompactionRecoversTheCall(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	a := j.Open(1, modelRecord(0))
	j.limit = 0
	box.write = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(s int64) error {
			if err := j.NoteClose(a, s); err != nil {
				return err
			}
			_ = j.Open(2, modelRecord(1)) // compacts
			return nil
		}, func() { j.Reopen(a) }, func() { j.Done(a) }, nil)
	if err == nil {
		t.Fatal("the append should fail")
	}
	box.write = (*os.File).Write
	counts, _ := recoverAt(t, snapshotDir(t, dir))
	if counts["m0"] != 1 {
		t.Fatalf("call A has %d lines after recovery, want 1 (%v)", counts["m0"], counts)
	}
}

// Final review I2: with one call held open for the whole run, 5,000 calls keep the journal bounded (before: 624,566 B).
func TestJournalStaysBoundedWithACallHeldOpen(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	held := j.Open(1, modelRecord(0))
	for i := 0; i < 5000; i++ {
		emitOne(t, j, box, j.Open(int64(i), modelRecord(i)), i)
	}
	st, err := os.Stat(filepath.Join(dir, CallsFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 2*journalCompactAt {
		t.Fatalf("journal is %d bytes with one call held open", st.Size())
	}
	// The held call is still in it: a crash now recovers it, and only it.
	counts, _ := recoverAt(t, snapshotDir(t, dir))
	if counts["m0"] != 2 || len(counts) != 5000 { // m0: its emitted line plus the held call's recovered one
		t.Fatalf("held %d: %d models, m0 %d", held, len(counts), counts["m0"])
	}
}

// Final review m6: a stream that ends with no line whose c line cannot be written leaves no spurious 599 at the next
// start (the journal is rewritten without it).
func TestEndWithAFailedWriteLeavesNoLine(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	id := j.Open(1, modelRecord(0))
	j.failWrite = func() error { return errors.New("disk full") }
	j.End(id)
	j.failWrite = nil
	if counts, _ := recoverAt(t, snapshotDir(t, dir)); len(counts) != 0 {
		t.Fatalf("lines after recovery: %v (want none)", counts)
	}
	_ = box
}

// Final review m6: a close that cannot be noted does not turn the call's real line into a cost-0 599: the journal is
// rewritten without the call and the real line is stored plainly; after a restart there is exactly that line.
func TestEmitWhenTheNoteFailsStoresTheRealLine(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	id := j.Open(1, modelRecord(0))
	j.failWrite = func() error { return errors.New("disk full") }
	emitOne(t, j, box, id, 0)
	j.failWrite = nil
	counts, recs := recoverAt(t, snapshotDir(t, dir))
	if counts["m0"] != 1 || len(recs) != 1 || recs[0].Status != 200 || recs[0].Cost != 7 {
		t.Fatalf("lines %v %+v (want one real line)", counts, recs)
	}
}

// recoverChain recovers snap, and also every state a kill during that recovery could leave (a snapshot after each of
// its writes), recovering each of those again: every call opened in snap must end with exactly one line in all of them.
func recoverChain(t *testing.T, snap string, models []string) {
	t.Helper()
	check := func(where string, counts map[string]int) {
		for _, m := range models {
			if counts[m] != 1 {
				t.Fatalf("%s: %s has %d lines, want 1 (%v)", where, m, counts[m], counts)
			}
		}
	}
	// The recovery itself, with a kill after each of its writes.
	work := snapshotDir(t, snap)
	rbox := mustBox(t, work)
	rj := mustJournal(t, work)
	var kills []string
	rbox.afterWrite = func() { kills = append(kills, snapshotDir(t, work)) }
	rj.afterWrite = rbox.afterWrite
	if _, err := rj.Recover(rbox, 9999); err != nil {
		t.Fatal(err)
	}
	check("after recovery", usageModels(t, rbox, rbox.Acked()+1))
	for i, k := range kills {
		counts, _ := recoverAt(t, k)
		check(fmt.Sprintf("after a kill at write %d of the recovery", i), counts)
	}
}

// Re-review R1: a long call L is in flight and a later call S has its close noted at seq n when the gateway is killed
// before S's line is stored. Recovering L first moved Next past n, and S was skipped as written: zero lines.
func TestRecoverAnOpenCallBeforeANotedUnwrittenOne(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	l := j.Open(1, modelRecord(0))
	s := j.Open(2, modelRecord(1))
	_ = l
	if err := j.NoteClose(s, box.Next()); err != nil { // noted under the send lock, then the kill: no line
		t.Fatal(err)
	}
	recoverChain(t, snapshotDir(t, dir), []string{"m0", "m1"})
}

// Re-review r4: every ordering of two and three calls, each in flight, noted but unwritten (at most one: the send lock
// holds one note at a time), or written, killed there; recovery (and a kill during it) leaves each exactly one line.
func TestRecoverEveryOrderingOfCallStates(t *testing.T) {
	states := []string{"open", "noted", "written"}
	var plans [][]string
	for _, n := range []int{2, 3} {
		var walk func(prefix []string)
		walk = func(prefix []string) {
			if len(prefix) == n {
				noted := 0
				for _, s := range prefix {
					if s == "noted" {
						noted++
					}
				}
				if noted <= 1 {
					plans = append(plans, append([]string(nil), prefix...))
				}
				return
			}
			for _, s := range states {
				walk(append(prefix, s))
			}
		}
		walk(nil)
	}
	for _, plan := range plans {
		t.Run(strings.Join(plan, "-"), func(t *testing.T) {
			dir := t.TempDir()
			box := mustBox(t, dir)
			j := mustJournal(t, dir)
			ids := make([]int64, len(plan))
			models := make([]string, len(plan))
			for i := range plan {
				ids[i] = j.Open(int64(i+1), modelRecord(i))
				models[i] = fmt.Sprintf("m%d", i)
			}
			noted := -1
			for i, st := range plan {
				switch st {
				case "written":
					emitOne(t, j, box, ids[i], i)
				case "noted":
					noted = i
				}
			}
			if noted >= 0 {
				if err := j.NoteClose(ids[noted], box.Next()); err != nil {
					t.Fatal(err)
				}
			}
			recoverChain(t, snapshotDir(t, dir), models)
		})
	}
}

// Re-review r2: when the c line cannot be appended, the close is written by rewriting the journal, before the line is
// stored: a kill at any write of that path leaves the call exactly one line (never none, never two).
func TestNoteByRewriteNeverLosesTheCall(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	id := j.Open(1, modelRecord(0))
	var snaps []string
	j.failWrite = func() error { return errors.New("disk full") }
	j.afterWrite = func() { snaps = append(snaps, snapshotDir(t, dir)) }
	box.afterWrite = j.afterWrite
	emitOne(t, j, box, id, 0)
	j.failWrite = nil
	snaps = append(snaps, snapshotDir(t, dir))
	for i, snap := range snaps {
		counts, recs := recoverAt(t, snap)
		if counts["m0"] != 1 {
			t.Fatalf("kill %d: %d lines, want 1", i, counts["m0"])
		}
		if i == len(snaps)-1 && (recs[0].Status != 200 || recs[0].Cost != 7) {
			t.Fatalf("the real line was replaced: %+v", recs[0])
		}
	}
}

// Re-review r3: a compaction whose rename keeps failing (a Windows sharing violation) is not retried on every open.
func TestCompactionBacksOffAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	box := mustBox(t, dir)
	j := mustJournal(t, dir)
	tries := 0
	j.rename = func(string, string) error { tries++; return errors.New("sharing violation") }
	j.limit = 0
	for i := 0; i < 500; i++ {
		emitOne(t, j, box, j.Open(int64(i), modelRecord(i)), i)
	}
	if tries > 5 {
		t.Fatalf("%d compaction attempts in 500 opens while the rename fails", tries)
	}
}

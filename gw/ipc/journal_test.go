package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	box, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	box.SetPerSegment(1) // every line its own segment: an ack prunes them
	j, err := OpenCallJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
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
			func(s int64) error { return j.NoteClose(id, s) }, func() { j.Reopen(id) }, nil)
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
		rbox, err := OpenOutbox(snap)
		if err != nil {
			t.Fatalf("crash point %d: %v", n, err)
		}
		rj, err := OpenCallJournal(snap)
		if err != nil {
			t.Fatal(err)
		}
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
	box, _ := OpenOutbox(dir)
	j, _ := OpenCallJournal(dir)
	j.Open(1, modelRecord(0))
	j.Open(2, modelRecord(1))
	_ = j.Close()
	_ = box.Close()
	// First recovery, killed after its first line: a snapshot taken at the outbox write of call 0's line.
	box, _ = OpenOutbox(dir)
	j, _ = OpenCallJournal(dir)
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
	box, _ = OpenOutbox(snap)
	j, _ = OpenCallJournal(snap)
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
	box, _ := OpenOutbox(dir)
	defer box.Close()
	next := box.Next()
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(int64) error { return errors.New("disk full") }, func() { t.Fatal("undo after a failed note") }, nil)
	if err == nil || box.Next() != next {
		t.Fatalf("err %v, next %d -> %d", err, next, box.Next())
	}
}

func TestAppendFailureReopens(t *testing.T) {
	dir := t.TempDir()
	box, _ := OpenOutbox(dir)
	j, _ := OpenCallJournal(dir)
	id := j.Open(1, modelRecord(0))
	box.write = func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") }
	reopened := false
	_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: modelRecord(0)}, 1,
		func(s int64) error { return j.NoteClose(id, s) }, func() { reopened = true; j.Reopen(id) }, nil)
	if err == nil || !reopened {
		t.Fatalf("err %v reopened %v", err, reopened)
	}
	box.write = (*os.File).Write
	_ = j.Close()
	_ = box.Close()
	box, _ = OpenOutbox(dir)
	defer box.Close()
	j, _ = OpenCallJournal(dir)
	defer j.Close()
	if n, err := j.Recover(box, 2); err != nil || n != 1 {
		t.Fatalf("recovered %d (%v), want 1", n, err)
	}
}

// With no call open, a journal past 64 KiB is emptied at the next open.
func TestJournalCompacts(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenCallJournal(dir)
	defer j.Close()
	for i := 0; i < 2000; i++ {
		id := j.Open(int64(i), modelRecord(i))
		if err := j.NoteClose(id, int64(i+1)); err != nil {
			t.Fatal(err)
		}
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
	box, _ := OpenOutbox(dir)
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

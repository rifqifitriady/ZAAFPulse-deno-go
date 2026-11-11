package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func appendN(t *testing.T, o *Outbox, n int) {
	t.Helper()
	for range n {
		typ, body := Synthetic(o.Next(), DefaultEmitBase)
		if _, _, err := o.Append(typ, body, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func replayed(t *testing.T, o *Outbox, from int64) []int64 {
	t.Helper()
	var seqs []int64
	err := o.Replay(from, func(line []byte) error {
		msg, err := Decode(line[:len(line)-1], true)
		if err != nil {
			return err
		}
		seqs = append(seqs, msg.Seq)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seqs
}

func TestOutboxAppendReplayAck(t *testing.T) {
	o, err := OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.SetPerSegment(3)
	appendN(t, o, 7)
	if got := o.Segments(); !slices.Equal(got, []int64{1, 4, 7}) {
		t.Fatalf("segments %v", got)
	}
	if got := replayed(t, o, 1); !slices.Equal(got, []int64{1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("replay %v", got)
	}
	if err := o.Ack(5); err != nil {
		t.Fatal(err)
	}
	if got := o.Segments(); !slices.Equal(got, []int64{4, 7}) {
		t.Fatalf("after ack 5: segments %v", got)
	}
	if o.Depth() != 2 || o.Acked() != 5 {
		t.Fatalf("depth %d acked %d", o.Depth(), o.Acked())
	}
	if got := replayed(t, o, 6); !slices.Equal(got, []int64{6, 7}) {
		t.Fatalf("replay from 6: %v", got)
	}
	if err := o.Ack(99); err != nil { // more than was sent: capped
		t.Fatal(err)
	}
	if len(o.Segments()) != 0 || o.Depth() != 0 || o.Acked() != 7 {
		t.Fatalf("after ack all: %v depth %d acked %d", o.Segments(), o.Depth(), o.Acked())
	}
	appendN(t, o, 1)
	if got := o.Segments(); !slices.Equal(got, []int64{8}) {
		t.Fatalf("a new segment starts at 8: %v", got)
	}
}

// A killed gateway leaves no stream.json update since the last ack: reopening finds the stream and the next seq from
// the segments.
func TestOutboxReopenAfterKill(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	stream := o.Stream()
	appendN(t, o, 5)
	_ = o.Ack(2)
	appendN(t, o, 1) // seq 6, after the last stream.json write
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Stream() != stream || o2.Next() != 7 || o2.Acked() != 2 {
		t.Fatalf("stream %s next %d acked %d", o2.Stream(), o2.Next(), o2.Acked())
	}
	if got := replayed(t, o2, o2.Acked()+1); !slices.Equal(got, []int64{3, 4, 5, 6}) {
		t.Fatalf("replay %v", got)
	}
}

func TestOutboxTruncatesALineCutByAKill(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	appendN(t, o, 3)
	seg := filepath.Join(dir, "00000000000000000001.jsonl")
	f, _ := os.OpenFile(seg, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"v":1,"type":"usage.rep`)
	_ = f.Close()
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Next() != 4 {
		t.Fatalf("next %d", o2.Next())
	}
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("replay %v", got)
	}
	appendN(t, o2, 1)
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("replay after append %v", got)
	}
}

func TestOutboxDamagedStreamFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "stream.json"), []byte(`{"stream":"x"}`), 0o600)
	if _, err := OpenOutbox(dir); err == nil {
		t.Fatal("a damaged stream.json must not start a new stream silently")
	}
}

// A kill between creating a segment and writing to it leaves an empty file: it is removed, and the next seq still
// follows the last event written, so no seq is used twice.
func TestOutboxEmptyLastSegment(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	o.SetPerSegment(3)
	appendN(t, o, 4) // segments 1 and 4
	_ = os.WriteFile(filepath.Join(dir, "00000000000000000005.jsonl"), nil, 0o600)
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Next() != 5 || !slices.Equal(o2.Segments(), []int64{1, 4}) {
		t.Fatalf("next %d segments %v", o2.Next(), o2.Segments())
	}
}

// Ack writes stream.json before it deletes a segment: when a segment goes, stream.json already holds the ack and the
// next seq (found by the kill tests: a kill between the two made a reopened outbox reuse sent seqs).
func TestOutboxAckWritesStreamFileBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	o.SetPerSegment(2)
	appendN(t, o, 5)
	removed := 0
	o.remove = func(path string) error {
		removed++
		var st streamState
		data, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
		if err := json.Unmarshal(data, &st); err != nil || st.Acked != 5 || st.Next != 6 {
			t.Fatalf("stream.json when %s goes: %s", filepath.Base(path), data)
		}
		return os.Remove(path)
	}
	if err := o.Ack(5); err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed %d segments", removed)
	}
}

// A write that fails part-way (disk full) leaves no fragment behind: the segment is truncated back to its size before
// the write, so the next Append starts a clean line and Replay reads every stored event.
func TestOutboxFailedWriteLeavesNoFragment(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	appendN(t, o, 2)
	o.write = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("no space left on device")
	}
	typ, body := Synthetic(o.Next(), DefaultEmitBase)
	if _, _, err := o.Append(typ, body, 1); err == nil {
		t.Fatal("a failed write must be reported")
	}
	if o.Next() != 3 {
		t.Fatalf("a failed write must not use a seq: next %d", o.Next())
	}
	o.write = (*os.File).Write
	appendN(t, o, 2)
	if got := replayed(t, o, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("replay %v", got)
	}
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) || o2.Next() != 5 {
		t.Fatalf("reopened: replay %v next %d", got, o2.Next())
	}
}

// When the truncate fails too, the segment is closed and the next Append starts a new segment: the fragment stays at
// the end of a segment that is never written again, where Replay and open skip it.
func TestOutboxFailedWriteAndTruncateStartsANewSegment(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	appendN(t, o, 2)
	o.write = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("no space left on device")
	}
	o.truncate = func(*os.File, int64) error { return errors.New("read-only file system") }
	typ, body := Synthetic(o.Next(), DefaultEmitBase)
	if _, _, err := o.Append(typ, body, 1); err == nil {
		t.Fatal("a failed write must be reported")
	}
	o.write, o.truncate = (*os.File).Write, (*os.File).Truncate
	appendN(t, o, 2)
	if got := o.Segments(); !slices.Equal(got, []int64{1, 3}) {
		t.Fatalf("segments %v", got)
	}
	if got := replayed(t, o, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("replay %v", got)
	}
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3, 4}) || o2.Next() != 5 {
		t.Fatalf("reopened: replay %v next %d", got, o2.Next())
	}
}

// A newest segment that holds only a torn line (a kill during its first write) is truncated to empty and removed on
// open; the next seq follows the last complete line of the segment before it.
func TestOutboxTornFirstLineOfNewestSegment(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	o.SetPerSegment(2)
	appendN(t, o, 2)
	_ = os.WriteFile(filepath.Join(dir, "00000000000000000003.jsonl"), []byte(`{"v":1,"type":"tr`), 0o600)
	o2, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Next() != 3 || !slices.Equal(o2.Segments(), []int64{1}) {
		t.Fatalf("next %d segments %v", o2.Next(), o2.Segments())
	}
	appendN(t, o2, 1)
	if got := replayed(t, o2, 1); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("replay %v", got)
	}
}

// An ack whose stream.json write fails leaves the in-memory ack where it was.
func TestOutboxAckSaveFailureKeepsTheOldAck(t *testing.T) {
	dir := t.TempDir()
	o, _ := OpenOutbox(dir)
	appendN(t, o, 3)
	_ = os.Mkdir(filepath.Join(dir, "stream.json.tmp"), 0o700) // WriteFile to a folder fails
	if err := o.Ack(2); err == nil {
		t.Fatal("the failed save must be reported")
	}
	if o.Acked() != 0 {
		t.Fatalf("acked %d", o.Acked())
	}
}

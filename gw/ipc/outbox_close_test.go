package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// T1-b (P4f entry 4): a second Close must not write stream.json: by then another gateway may own the outbox, and the
// old state would roll its ack back.
func TestOutboxSecondCloseWritesNothing(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 3)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := openOutbox(t, dir) // the lock is free again
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Ack(3); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if err := o.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if string(before) != string(after) {
		t.Fatalf("the second Close rewrote stream.json: %s -> %s", before, after)
	}
}

// T1-a: after Close nothing is appended, sent, replayed or acknowledged, and no file is created or written.
func TestOutboxRefusesEverythingAfterClose(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 2)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	stream, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	entries := func() int { e, _ := os.ReadDir(dir); return len(e) }
	n := entries()
	sent := false
	typ, body := Synthetic(3, DefaultEmitBase)
	if _, err := o.AppendSend(typ, body, 1, func([]byte) error { sent = true; return nil }); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("AppendSend after Close: %v", err)
	}
	if _, _, err := o.Append(typ, body, 1); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Append after Close: %v", err)
	}
	if err := o.Replay(1, func([]byte) error { sent = true; return nil }); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Replay after Close: %v", err)
	}
	if err := o.Ack(2); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Ack after Close: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if sent || entries() != n || string(after) != string(stream) {
		t.Fatalf("sent %v, files %d -> %d, stream.json %s -> %s", sent, n, entries(), stream, after)
	}
}

// T1-a: Close waits for a send in progress (it takes the send lock), so the event that send stored is in the
// stream.json Close writes, and nothing is appended after Close.
func TestOutboxCloseWaitsForASendInProgress(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	inSend, release := make(chan struct{}), make(chan struct{})
	go func() {
		typ, body := Synthetic(1, DefaultEmitBase)
		_, _ = o.AppendSend(typ, body, 1, func([]byte) error { close(inSend); <-release; return nil })
	}()
	<-inSend
	closed := make(chan error, 1)
	go func() { closed <- o.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during a send: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	var st streamState
	if err := json.Unmarshal(data, &st); err != nil || st.Next != 2 {
		t.Fatalf("stream.json %s (%v), want next 2", data, err)
	}
}

// A killed outbox (the test helper drops the lock without writing) is not written by a later Close either: without
// the lock, stream.json may belong to another gateway.
func TestOutboxCloseAfterKillWritesNothing(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 1)
	before, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	kill(o)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if string(before) != string(after) {
		t.Fatalf("Close without the lock wrote stream.json: %s -> %s", before, after)
	}
}

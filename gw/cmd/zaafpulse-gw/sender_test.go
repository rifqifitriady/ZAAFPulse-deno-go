package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

func senderOutbox(t *testing.T) *ipc.Outbox {
	t.Helper()
	box, err := ipc.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Close() })
	box.SetPerSegment(500)
	return box
}

func usageEvent() *ipc.UsageReport {
	return &ipc.UsageReport{Record: ipc.UsageRecord{Ts: 1, KeyID: "k", Provider: "p", Model: "m", Status: 200, Ms: 1}}
}

// A control plane that does not read: 10,000 appends return at once, far past the queue; once it reads, every event
// arrives in seq order, once (the overflow was read back from the outbox).
func TestSenderNeverBlocksAndKeepsOrder(t *testing.T) {
	box := senderOutbox(t)
	release := make(chan struct{})
	var mu sync.Mutex
	var got []int64
	s := newSender(box, func(line []byte) error {
		<-release
		seq, err := ipc.SeqOf(line)
		if err != nil {
			return err
		}
		mu.Lock()
		got = append(got, seq)
		mu.Unlock()
		return nil
	}, t.Logf)
	s.start(box.Next())
	const n = 10000
	start := time.Now()
	for range n {
		if _, err := box.AppendSend(ipc.TypeUsageReport, usageEvent(), 1, s.enqueue); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("appends took %s while the control plane did not read", d)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		k := len(got)
		mu.Unlock()
		if k >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.stop(time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("sent %d of %d", len(got), n)
	}
	for i, seq := range got {
		if seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, seq)
		}
	}
}

// A send that fails ends the sender; appends still succeed and the events stay in the outbox for the next start.
func TestSenderStopsOnSendError(t *testing.T) {
	box := senderOutbox(t)
	s := newSender(box, func([]byte) error { return errors.New("broken pipe") }, t.Logf)
	s.start(box.Next())
	for range 10 {
		if _, err := box.AppendSend(ipc.TypeUsageReport, usageEvent(), 1, s.enqueue); err != nil {
			t.Fatal(err)
		}
	}
	s.stop(time.Second)
	if d := box.Depth(); d != 10 {
		t.Fatalf("depth %d, want 10", d)
	}
}

// A control plane that never reads again: stop gives up after its grace.
func TestSenderStopGivesUpAfterGrace(t *testing.T) {
	box := senderOutbox(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s := newSender(box, func([]byte) error { <-block; return nil }, t.Logf)
	s.start(box.Next())
	if _, err := box.AppendSend(ipc.TypeUsageReport, usageEvent(), 1, s.enqueue); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.stop(100 * time.Millisecond)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("stop took %s", d)
	}
}

// A sender that never started stops at once.
func TestSenderStopBeforeStart(t *testing.T) {
	s := newSender(senderOutbox(t), func([]byte) error { return nil }, t.Logf)
	s.stop(time.Second)
}

// A second stop (the deferred one after drain) returns at once instead of waiting its grace again.
func TestSenderSecondStopDoesNotWait(t *testing.T) {
	box := senderOutbox(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s := newSender(box, func([]byte) error { <-block; return nil }, t.Logf)
	s.start(box.Next())
	if _, err := box.AppendSend(ipc.TypeUsageReport, usageEvent(), 1, s.enqueue); err != nil {
		t.Fatal(err)
	}
	s.stop(50 * time.Millisecond)
	start := time.Now()
	s.stop(5 * time.Second)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("the second stop waited %s", d)
	}
}

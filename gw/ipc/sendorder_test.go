package ipc

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// While an event's send is in progress (the control plane is slow to read), neither a replay nor the next event can
// send: both wait for it, so nothing reaches the channel ahead of an event already stored.
func TestOutboxSendBlocksReplayAndNextEvent(t *testing.T) {
	o, err := openOutbox(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, sending := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	note := func(s string) func([]byte) error {
		return func(line []byte) error {
			seq, _ := seqOf(line[:len(line)-1])
			mu.Lock()
			order = append(order, fmt.Sprintf("%s%d", s, seq))
			mu.Unlock()
			return nil
		}
	}
	typ, body := Synthetic(1, DefaultEmitBase)
	first := make(chan error, 1)
	go func() {
		_, err := o.AppendSend(typ, body, 1, func(line []byte) error {
			close(sending)
			<-release
			return note("a")(line)
		})
		first <- err
	}()
	<-sending
	others := make(chan error, 2)
	go func() { others <- o.Replay(1, note("r")) }()
	go func() {
		_, err := o.AppendSend(typ, body, 1, note("a"))
		others <- err
	}()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	early := append([]string(nil), order...)
	mu.Unlock()
	if len(early) != 0 {
		t.Fatalf("sent while event 1 was still being sent: %v", early)
	}
	close(release)
	for _, c := range []chan error{first, others, others} {
		if err := <-c; err != nil {
			t.Fatal(err)
		}
	}
	if order[0] != "a1" {
		t.Fatalf("order %v", order)
	}
}

// E10: an event's append and its send, and a replay's sends, happen under one lock. With appends and replays racing,
// the receiver sees every replay as one unbroken run from its start to the newest event sent so far, and every new
// event right after the event before it: the ingest's gap check never fires.
func TestOutboxSendsAndReplayKeepOrder(t *testing.T) {
	o, err := openOutbox(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.SetPerSegment(7)
	type sent struct {
		src string // "a" for an appended event, "r<n>" for replay n
		seq int64
	}
	var mu sync.Mutex // guards got only; the order under test is the outbox's
	var got []sent
	sink := func(src string) func([]byte) error {
		return func(line []byte) error {
			seq, err := seqOf(line[:len(line)-1])
			if err != nil {
				return err
			}
			runtime.Gosched() // give a racing send the chance to cut in
			mu.Lock()
			got = append(got, sent{src, seq})
			mu.Unlock()
			return nil
		}
	}
	const writers, perWriter, replays = 4, 50, 20
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range perWriter {
				typ, body := Synthetic(1, DefaultEmitBase)
				if _, err := o.AppendSend(typ, body, 1, sink("a")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for i := range replays {
		wg.Go(func() {
			if err := o.Replay(1, sink(fmt.Sprintf("r%d", i))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var last int64 // the newest appended event the receiver has seen
	for i := 0; i < len(got); {
		s := got[i]
		if s.src == "a" {
			if s.seq != last+1 {
				t.Fatalf("at %d: event %d after %d", i, s.seq, last)
			}
			last = s.seq
			i++
			continue
		}
		// A replay: seqs 1..last, sent together.
		for want := int64(1); want <= last; want++ {
			if i >= len(got) || got[i].src != s.src || got[i].seq != want {
				t.Fatalf("at %d: replay %s broken (want %d of 1..%d): %v", i, s.src, want, last, got[i:min(i+5, len(got))])
			}
			i++
		}
	}
	if last != writers*perWriter {
		t.Fatalf("last %d", last)
	}
}

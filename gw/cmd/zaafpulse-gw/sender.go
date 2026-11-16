package main

import (
	"errors"
	"sync"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// senderQueue is how many stored lines the sender holds in memory before it reads the rest from the outbox.
const senderQueue = 4096

// sender writes the outbox's events to the control plane from one goroutine, in seq order, so that no caller waits for
// the control plane to read (P4e Decision 19). The outbox's AppendSend still stores each event and hands it over under
// one lock (D4, E10), with enqueue as its send: lines arrive here in seq order and enqueue never blocks. When the queue
// is full the line is dropped from memory only (it is stored), the sender drains what it holds, then reads the rest
// from the outbox. A send error ends the sender: the control plane is gone, and the next start replays the outbox.
type sender struct {
	box  *ipc.Outbox
	send func(line []byte) error
	logf func(string, ...any)

	queue chan []byte
	wake  chan struct{}
	quit  chan struct{}
	done  chan struct{}
	once  sync.Once

	mu      sync.Mutex
	lost    bool // a line did not fit the queue: the rest comes from the outbox
	started bool
	stopped bool // stop was called

	next int64 // the seq the control plane gets next (the goroutine only)
}

func newSender(box *ipc.Outbox, send func([]byte) error, logf func(string, ...any)) *sender {
	return &sender{box: box, send: send, logf: logf, queue: make(chan []byte, senderQueue),
		wake: make(chan struct{}, 1), quit: make(chan struct{}), done: make(chan struct{})}
}

// start begins sending at seq next (after the handshake's replay sent everything before it).
func (s *sender) start(next int64) {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	s.next = next
	go s.run()
}

// enqueue is AppendSend's send: it never blocks.
func (s *sender) enqueue(line []byte) error {
	s.mu.Lock()
	if !s.lost {
		select {
		case s.queue <- line:
			s.mu.Unlock()
			return nil
		default:
			s.lost = true
		}
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *sender) run() {
	defer close(s.done)
	for {
		select {
		case line := <-s.queue:
			if !s.one(line) {
				return
			}
		case <-s.wake:
		case <-s.quit:
			s.flush()
			return
		}
		if !s.catchUp() {
			return
		}
	}
}

// one sends a queued line, unless the outbox read already sent it; a line past the next seq means one was lost.
func (s *sender) one(line []byte) bool {
	seq, err := ipc.SeqOf(line)
	if err != nil || seq < s.next {
		return true
	}
	if seq > s.next {
		s.mu.Lock()
		s.lost = true
		s.mu.Unlock()
		return true
	}
	if err := s.send(line); err != nil {
		s.logf("ipc: sending to the control plane failed: %v (the outbox keeps the events)", err)
		return false
	}
	s.next = seq + 1
	return true
}

// catchUp reads from the outbox what the queue dropped, once the queue is empty.
func (s *sender) catchUp() bool {
	s.mu.Lock()
	if !s.lost || len(s.queue) > 0 {
		s.mu.Unlock()
		return true
	}
	s.lost = false // before the read: a line dropped from now on sets it again
	s.mu.Unlock()
	err := s.box.ReadFrom(s.next, func(seq int64, line []byte) error {
		if seq < s.next {
			return nil
		}
		if err := s.send(line); err != nil {
			return err
		}
		s.next = seq + 1
		return nil
	})
	if errors.Is(err, ipc.ErrOutboxClosed) {
		return false
	}
	if err != nil {
		s.logf("ipc: sending stored events failed: %v (the outbox keeps them)", err)
		return false
	}
	return true
}

// flush sends what is queued (and what the queue dropped) before stopping.
func (s *sender) flush() {
	for {
		select {
		case line := <-s.queue:
			if !s.one(line) {
				return
			}
		default:
			s.catchUp()
			return
		}
	}
}

// stop sends what it holds for at most grace, then gives up (the outbox keeps the rest for the next start). Call it
// before the outbox closes.
func (s *sender) stop(grace time.Duration) {
	s.mu.Lock()
	started, again := s.started, s.stopped
	s.stopped = true
	s.mu.Unlock()
	if !started || again {
		return // a second stop (the deferred one after drain or shutdown) does not wait again
	}
	s.once.Do(func() { close(s.quit) })
	select {
	case <-s.done:
	case <-time.After(grace):
		s.logf("ipc: %s passed with events still unsent; the next start sends them", grace)
	}
}

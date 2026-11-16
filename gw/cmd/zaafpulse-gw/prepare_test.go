package main

import (
	"sync"
	"testing"
	"time"
)

// The gateway compiles the guard's patterns at start, off the start path (P4e Decision 12).
func TestRunPreparesTheGuard(t *testing.T) {
	called := make(chan struct{})
	var once sync.Once
	was := prepareGuard
	prepareGuard = func() { once.Do(func() { close(called) }) }
	t.Cleanup(func() { prepareGuard = was })
	r := drive(t)
	r.next(t, "hello")
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("the guard was not prepared within 1 s of the start")
	}
}

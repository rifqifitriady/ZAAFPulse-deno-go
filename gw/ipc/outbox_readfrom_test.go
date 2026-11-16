package ipc

import (
	"sync"
	"sync/atomic"
	"testing"
)

// ReadFrom runs while events are appended (P4e: the gateway's sender reads the outbox after its queue overflowed): every
// read sees whole lines only, seqs rising by one from the start, and a read after the last append sees them all.
func TestReadFromWhileAppending(t *testing.T) {
	o, err := openOutbox(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.SetPerSegment(100)
	const n = 5000
	var wg sync.WaitGroup
	var finished atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer finished.Store(true)
		for range n {
			typ, body := Synthetic(o.Next(), DefaultEmitBase)
			if _, _, err := o.Append(typ, body, 1); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	read := func() int64 {
		var want int64 = 1
		err := o.ReadFrom(1, func(seq int64, line []byte) error {
			if seq != want {
				t.Fatalf("seq %d, want %d", seq, want)
			}
			if line[len(line)-1] != '\n' {
				t.Fatalf("seq %d: a line without its newline", seq)
			}
			if s, err := SeqOf(line); err != nil || s != seq {
				t.Fatalf("SeqOf %d %v, want %d", s, err, seq)
			}
			want++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return want - 1
	}
	for !finished.Load() {
		read()
	}
	wg.Wait()
	if got := read(); got != n {
		t.Fatalf("read %d of %d after the last append", got, n)
	}
}

func TestReadFromAfterCloseIsErrOutboxClosed(t *testing.T) {
	o, err := OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 3)
	_ = o.Close()
	if err := o.ReadFrom(1, func(int64, []byte) error { return nil }); err != ErrOutboxClosed {
		t.Fatalf("got %v", err)
	}
}

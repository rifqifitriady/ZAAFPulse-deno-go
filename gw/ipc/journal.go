package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// CallJournal records the go engine's calls in flight (P4f D14), next to the outbox in calls.journal: append only, one
// writer (the outbox's lock covers the folder), no fsync (the target is a killed process, as for the outbox).
//
//	{"o": id, "ts": start ms, "r": base record}  a call starts, before its provider call;
//	{"c": id, "seq": n}                         its usage line takes seq n (noted under the outbox's send lock, right
//	                                            before that line is appended); without seq: it ends with no line;
//	{"u": id}                                   the append after its close failed: it is open again.
//
// On start, before hello, Recover appends one line for each call of the previous run that has none: no close, or a
// close whose seq the reopened outbox never wrote (seq >= Next). Next after a reopen is above every written seq and not
// above an unwritten one (stream.json is saved on ack and close only, never ahead of an append), and no event is
// appended between a close and its line (the send lock), so a call gets exactly one line: never none, never two.
// Recover notes its own closes first, so a kill during the recovery does not recover a call twice.
type CallJournal struct {
	mu         sync.Mutex
	path       string
	f          *os.File
	size       int64
	next       int64
	open       int    // calls opened and not yet closed in this run
	afterWrite func() // tests: runs after each completed write (a crash point)
}

type journalLine struct {
	O   int64        `json:"o,omitzero"`
	Ts  int64        `json:"ts,omitzero"`
	R   *UsageRecord `json:"r,omitzero"`
	C   int64        `json:"c,omitzero"`
	Seq int64        `json:"seq,omitzero"`
	U   int64        `json:"u,omitzero"`
}

const (
	// CallsFile is the journal's name in the outbox folder.
	CallsFile = "calls.journal" // not .jsonl: the outbox reads every *.jsonl name as a segment
	// RecoveredError is the error text of a recovered line (P4f D15).
	RecoveredError = "the gateway stopped before the call ended"
	// journalCompactAt: with no call open, a journal past this size is emptied.
	journalCompactAt = 64 << 10
)

// OpenCallJournal opens dir's calls.journal for appending (created when missing). The caller holds the outbox's lock.
func OpenCallJournal(dir string) (*CallJournal, error) {
	p := filepath.Join(dir, CallsFile)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the call journal: %w", err)
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("the call journal: %w", err)
	}
	return &CallJournal{path: p, f: f, size: size, next: 1}, nil
}

// write appends one line; a write that fails part-way is cut back, so no fragment is followed by another line. Under mu.
func (j *CallJournal) write(l journalLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := j.f.Write(b)
	if err != nil {
		if n > 0 {
			_ = truncateAndSeek(j.f, j.size)
		}
		return err
	}
	j.size += int64(n)
	if j.afterWrite != nil {
		j.afterWrite()
	}
	return nil
}

// Open records a call about to start and returns its id; 0 when the line could not be written (the call goes on, and a
// crash during it loses its line, as before P4f).
func (j *CallJournal) Open(ts int64, base UsageRecord) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.open == 0 && j.size > journalCompactAt {
		j.compact()
	}
	id := j.next
	if err := j.write(journalLine{O: id, Ts: ts, R: &base}); err != nil {
		return 0
	}
	j.next++
	j.open++
	return id
}

// NoteClose records that call id's usage line takes seq (0: the call ends with no line).
func (j *CallJournal) NoteClose(id, seq int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.write(journalLine{C: id, Seq: seq}); err != nil {
		return err
	}
	j.open--
	return nil
}

// Reopen undoes a close whose line was not appended: the next start writes the call's line.
func (j *CallJournal) Reopen(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.write(journalLine{U: id}) == nil {
		j.open++
	}
}

// compact empties the journal: no call is open, so every line in it is settled. Under mu.
func (j *CallJournal) compact() {
	if truncateAndSeek(j.f, 0) == nil {
		j.size = 0
	}
}

// Recover writes the line of every call a previous run left without one, then empties the journal, and returns how many
// it wrote. It runs once, before hello, with no other writer of the outbox; the replay after the handshake sends them.
func (j *CallJournal) Recover(box *Outbox, now int64) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	data, err := io.ReadAll(j.f)
	if err != nil {
		return 0, err
	}
	if _, err := j.f.Seek(j.size, io.SeekStart); err != nil {
		return 0, err
	}
	type call struct {
		ts     int64
		base   UsageRecord
		closed bool
		seq    int64
	}
	calls := map[int64]*call{}
	var order []int64
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // a line cut by a kill (the last one), or damaged: skipped
		}
		switch {
		case l.O != 0 && l.R != nil:
			if _, seen := calls[l.O]; !seen {
				order = append(order, l.O)
			}
			calls[l.O] = &call{ts: l.Ts, base: *l.R}
		case l.C != 0:
			if c := calls[l.C]; c != nil {
				c.closed, c.seq = true, l.Seq
			}
		case l.U != 0:
			if c := calls[l.U]; c != nil {
				c.closed, c.seq = false, 0
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range order {
		c := calls[id]
		if c.closed && (c.seq == 0 || c.seq < box.Next()) {
			continue
		}
		_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: Recovered(c.base, c.ts)}, now,
			func(seq int64) error { return j.write(journalLine{C: id, Seq: seq}) }, func() {}, nil)
		if err != nil {
			return n, err
		}
		n++
	}
	j.compact()
	if j.size != 0 {
		return n, errors.New("the call journal could not be emptied")
	}
	return n, nil
}

// Recovered is the usage line of a call the gateway stopped during (P4f D15, owner answer 2026-11-16): the call's base,
// status 599, the fixed error text, no token counts, cost 0, ts its start, ms 0, no trace id (no span was stored).
func Recovered(base UsageRecord, ts int64) UsageRecord {
	rec := base
	msg := RecoveredError
	rec.Ts, rec.Ms, rec.Status, rec.Error, rec.Cost = ts, 0, 599, &msg, 0
	rec.Input, rec.Output, rec.Cached, rec.TraceID = nil, nil, nil, nil
	rec.TTFT = Nullable[float64]{}
	return rec
}

// Close closes the journal file.
func (j *CallJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

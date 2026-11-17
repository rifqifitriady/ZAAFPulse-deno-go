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
	"slices"
	"sync"
)

// CallJournal records the go engine's calls in flight (P4f D14), next to the outbox in calls.journal: append only between
// compactions, one writer (the outbox's lock covers the folder), no fsync (the target is a killed process, as for the
// outbox).
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
//
// A call stays in the journal's table until its line is appended (Done) or it ends with none (End), not just until its
// close is noted (final review I1: a compaction between the close and the line lost the call). The journal is compacted
// by copy (final review I2): past a threshold, the table's calls (each one's o line, and its c line once noted) are
// written to calls.journal.tmp, which is renamed over the journal; so it stays bounded with calls always in flight.
type CallJournal struct {
	mu         sync.Mutex
	path       string
	f          *os.File
	size       int64
	next       int64
	limit      int64                  // compact once the journal passes this size
	calls      map[int64]*journalCall // calls whose line is not appended yet
	afterWrite func()                 // tests: runs after each completed write (a crash point)
	failWrite  func() error           // tests: makes the next line writes fail
}

type journalCall struct {
	open []byte // the o line, newline included
	seq  int64  // the noted close (0: none yet)
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
	// journalCompactAt: the smallest size at which the journal is compacted; after a compaction the next one waits until
	// it has grown to four times its compacted size, so many calls in flight do not compact on every open.
	journalCompactAt = 64 << 10
)

// OpenCallJournal opens dir's calls.journal for appending (created when missing). The caller holds the outbox's lock.
func OpenCallJournal(dir string) (*CallJournal, error) {
	p := filepath.Join(dir, CallsFile)
	_ = os.Remove(p + ".tmp") // a compaction a kill cut short: the journal itself is whole
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the call journal: %w", err)
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("the call journal: %w", err)
	}
	return &CallJournal{path: p, f: f, size: size, next: 1, limit: journalCompactAt, calls: map[int64]*journalCall{}}, nil
}

func lineOf(l journalLine) ([]byte, error) {
	b, err := json.Marshal(l)
	return append(b, '\n'), err
}

// write appends one line; a write that fails part-way is cut back, so no fragment is followed by another line. Under mu.
func (j *CallJournal) write(b []byte) error {
	if j.failWrite != nil {
		if err := j.failWrite(); err != nil {
			return err
		}
	}
	n, err := j.f.Write(b)
	if err != nil {
		if n > 0 {
			_ = truncateAndSeek(j.f, j.size)
		}
		return err
	}
	j.size += int64(n)
	j.wrote()
	return nil
}

func (j *CallJournal) wrote() {
	if j.afterWrite != nil {
		j.afterWrite()
	}
}

// Open records a call about to start and returns its id; 0 when the line could not be written (the call goes on, and a
// crash during it loses its line, as before P4f).
func (j *CallJournal) Open(ts int64, base UsageRecord) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.size > j.limit {
		_ = j.compact()
	}
	id := j.next
	b, err := lineOf(journalLine{O: id, Ts: ts, R: &base})
	if err != nil || j.write(b) != nil {
		return 0
	}
	j.next++
	j.calls[id] = &journalCall{open: b}
	return id
}

// NoteClose records that call id's usage line takes seq. The call stays in the table until Done: a compaction keeps
// its close with it.
func (j *CallJournal) NoteClose(id, seq int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := lineOf(journalLine{C: id, Seq: seq})
	if err == nil {
		err = j.write(b)
	}
	if err != nil {
		return err
	}
	if c := j.calls[id]; c != nil {
		c.seq = seq
	}
	return nil
}

// Done: call id's line is appended; on disk its o and c lines settle it, and the next compaction drops them.
func (j *CallJournal) Done(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.calls, id)
}

// Reopen undoes a close whose line was not appended: the next start writes the call's line. When the u line cannot be
// written either, the journal is rewritten from the table (which no longer has the close), or the call keeps its noted
// close and the next start counts its line as written: the residual case of a disk that refuses every write.
func (j *CallJournal) Reopen(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := j.calls[id]
	if c == nil {
		return
	}
	c.seq = 0
	b, err := lineOf(journalLine{U: id})
	if err != nil || j.write(b) != nil {
		_ = j.compact()
	}
}

// End: call id ends with no usage line (final review m6: never a spurious 599 at the next start). When its c line
// cannot be written, the journal is rewritten without the call.
func (j *CallJournal) End(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.calls[id] == nil {
		return
	}
	delete(j.calls, id)
	b, err := lineOf(journalLine{C: id})
	if err != nil || j.write(b) != nil {
		_ = j.compact()
	}
}

// Forget drops call id from the journal by rewriting it without the call; an error when the rewrite failed (the call is
// then still in it).
func (j *CallJournal) Forget(id int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := j.calls[id]
	if c == nil {
		return nil
	}
	delete(j.calls, id)
	if err := j.compact(); err != nil {
		j.calls[id] = c
		return err
	}
	return nil
}

// Emit stores a journalled call's usage line in box and sends it (P4f D14): its close is noted under the outbox's send
// lock right before the append, and the call leaves the table once the line is stored. When the close cannot be noted,
// the call is dropped from the journal (rewritten without it) and the real line stored plainly, so a refused note never
// turns the call's real line into a cost-0 599 at the next start (final review m6); only when the journal cannot be
// rewritten either is the line left to the next start (the residual case of a disk that refuses every write).
func (j *CallJournal) Emit(box *Outbox, call int64, typ string, body any, ts int64, send func([]byte) error) (int64, error) {
	seq, err := box.AppendSendNoted(typ, body, ts, func(seq int64) error { return j.NoteClose(call, seq) },
		func() { j.Reopen(call) }, func() { j.Done(call) }, send)
	var ne *NoteError
	if errors.As(err, &ne) {
		if j.Forget(call) == nil {
			if send == nil {
				_, seq, err := box.Append(typ, body, ts)
				return seq, err
			}
			return box.AppendSend(typ, body, ts, send)
		}
	}
	return seq, err
}

// compact rewrites the journal with the calls of the table only, through a temp file renamed over it. Under mu.
func (j *CallJournal) compact() error {
	ids := make([]int64, 0, len(j.calls))
	for id := range j.calls {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var buf bytes.Buffer
	for _, id := range ids {
		c := j.calls[id]
		buf.Write(c.open)
		if c.seq != 0 {
			b, _ := lineOf(journalLine{C: id, Seq: c.seq})
			buf.Write(b)
		}
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Windows renames over an open file only when no handle holds it: the journal is closed first, then opened again.
	_ = j.f.Close()
	renameErr := os.Rename(tmp, j.path)
	f, err := os.OpenFile(j.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return err
	}
	j.f, j.size = f, size
	if renameErr != nil {
		_ = os.Remove(tmp)
		return renameErr
	}
	j.limit = max(journalCompactAt, 4*size)
	j.wrote()
	return nil
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
			func(seq int64) error {
				b, err := lineOf(journalLine{C: id, Seq: seq})
				if err != nil {
					return err
				}
				return j.write(b)
			}, func() {}, func() {}, nil)
		if err != nil {
			return n, err
		}
		n++
	}
	clear(j.calls)
	if err := j.compact(); err != nil || j.size != 0 {
		return n, errors.Join(errors.New("the call journal could not be emptied"), err)
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

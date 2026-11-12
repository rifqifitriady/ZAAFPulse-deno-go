package ipc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Outbox is the gateway's durable event stream (P3 D4). Every usage.report, audit.event and trace.span is appended
// here before it is sent, to append-only segment files named by the first seq they hold (00000000000000000001.jsonl),
// one encoded envelope per line. stream.json holds {stream, next, acked}. A segment is deleted once every event in it
// is acknowledged. The durability target is a killed process, not power loss: a write is enough, there is no fsync.
//
// On open, the next seq is the larger of stream.json's next and the last complete line + 1, so stream.json is written
// on ack and close only. A line cut short by a kill is truncated: Append returns before the event is sent, so a cut
// line was never sent and its seq is free again. A write that fails part-way (disk full) is truncated back at once, so
// no fragment is left in the middle of a segment; when that truncate fails too, the segment is closed and the next
// Append starts a new one, which leaves the fragment at the end of a segment that is never written again (Replay
// stops at a segment's last newline).
//
// The outbox has one writer: OpenOutbox takes an exclusive lock on <dir>/.lock and holds it until Close (P4a E1). Every
// event is sent under one lock with its append and with the replay (E10), so the control plane receives the stream in
// seq order with no gap: a replay never cuts into a run of new events, and no event is sent before an older one.
// Close is the last writer (P4f entry 4): it waits for a send in progress, writes stream.json only while it holds the
// lock, and every call after it returns ErrOutboxClosed without touching a file.
type Outbox struct {
	dir        string
	perSegment int
	lock       io.Closer  // the .lock file; nil once released
	sendMu     sync.Mutex // the event stream: Append, AppendSend and Replay, sends included
	mu         sync.Mutex // the state below; never held across a send
	state      streamState
	closed     bool    // Close ran: nothing is written or sent any more
	segs       []int64 // first seq of each segment, ascending
	cur        *os.File
	curCount   int
	curSize    int64                               // bytes in cur: where a failed write is truncated back to
	remove     func(string) error                  // os.Remove; a test watches the order of writes
	write      func(*os.File, []byte) (int, error) // (*os.File).Write; a test makes it fail part-way
	truncate   func(*os.File, int64) error         // truncateAndSeek; a test makes it fail
}

type streamState struct {
	Stream string `json:"stream"`
	Next   int64  `json:"next"`
	Acked  int64  `json:"acked"`
}

const (
	segmentExt = ".jsonl"
	lockName   = ".lock"
)

var (
	// ErrOutboxInUse: another process (or another Outbox in this one) holds the outbox lock. The gateway exits
	// ExitOutboxInUse.
	ErrOutboxInUse = errors.New("outbox: in use by another gateway")
	// ErrOutboxDamaged: stream.json is unreadable, or missing while segments are present, or a segment holds a line
	// without a seq. A new stream is never started over old events. The gateway exits ExitDamaged.
	ErrOutboxDamaged = errors.New("outbox: damaged")
	// ErrOutboxClosed: Close ran. Nothing is appended, sent, replayed or acknowledged after it, and no file is written.
	ErrOutboxClosed = errors.New("outbox: closed")
)

// OpenOutbox opens (or creates) the outbox in dir and locks it until Close. While another Outbox holds the lock it
// returns an error that wraps ErrOutboxInUse.
func OpenOutbox(dir string) (_ *Outbox, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockFile(filepath.Join(dir, lockName))
	if err != nil {
		return nil, err
	}
	o := &Outbox{dir: dir, perSegment: 1000, lock: lock, remove: os.Remove, write: (*os.File).Write, truncate: truncateAndSeek}
	defer func() {
		if err != nil {
			o.release()
		}
	}()
	data, err := os.ReadFile(filepath.Join(dir, "stream.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &o.state); err != nil || !patterns["hex32"].MatchString(o.state.Stream) {
			return nil, fmt.Errorf("%w: %s is unreadable", ErrOutboxDamaged, filepath.Join(dir, "stream.json"))
		}
	case errors.Is(err, fs.ErrNotExist):
		// A first start has no segments either. Segments without stream.json lost the stream id and the ack: a new
		// stream would send their events again under seqs the control plane may already hold (T4-2).
		if seg, err := anySegment(dir); err != nil {
			return nil, err
		} else if seg != "" {
			return nil, fmt.Errorf("%w: %s holds %s and no stream.json", ErrOutboxDamaged, dir, seg)
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		o.state = streamState{Stream: hex.EncodeToString(b), Next: 1}
	default:
		return nil, err
	}
	o.state.Next = max(o.state.Next, o.state.Acked+1)
	if err := o.loadSegments(); err != nil {
		return nil, err
	}
	if err := o.save(); err != nil {
		return nil, err
	}
	return o, o.prune()
}

func anySegment(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), segmentExt) {
			return e.Name(), nil
		}
	}
	return "", nil
}

// release closes the open segment and the lock file (which drops the lock).
func (o *Outbox) release() {
	if o.cur != nil {
		_ = o.cur.Close()
		o.cur = nil
	}
	if o.lock != nil {
		_ = o.lock.Close()
		o.lock = nil
	}
}

// SetPerSegment sets how many events a segment holds before the next one starts (tests use a small number).
func (o *Outbox) SetPerSegment(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if n > 0 {
		o.perSegment = n
	}
}

func (o *Outbox) segPath(first int64) string {
	return filepath.Join(o.dir, fmt.Sprintf("%020d%s", first, segmentExt))
}

func (o *Outbox) loadSegments() error {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, segmentExt) {
			continue
		}
		first, err := strconv.ParseInt(strings.TrimSuffix(name, segmentExt), 10, 64)
		if err == nil && first >= 1 {
			o.segs = append(o.segs, first)
		}
	}
	slices.Sort(o.segs)
	// The newest segments may end in a line cut by a kill (truncated: it was never sent) or be empty (a kill between
	// creating the file and the first write: removed). The next seq follows the last complete line.
	for len(o.segs) > 0 {
		path := o.segPath(o.segs[len(o.segs)-1])
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if cut := bytes.LastIndexByte(data, '\n') + 1; cut < len(data) {
			if err := os.Truncate(path, int64(cut)); err != nil {
				return err
			}
			data = data[:cut]
		}
		if len(data) == 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
			o.segs = o.segs[:len(o.segs)-1]
			continue
		}
		lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		seq, err := seqOf(lines[len(lines)-1])
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrOutboxDamaged, path, err)
		}
		o.state.Next = max(o.state.Next, seq+1)
		break
	}
	return nil
}

func seqOf(line []byte) (int64, error) {
	var head struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal(line, &head); err != nil || head.Seq < 1 {
		return 0, fmt.Errorf("a line without a seq: %.80s", line)
	}
	return head.Seq, nil
}

// AppendSend stores one event with the stream's next seq, then sends its line (newline included), both under the
// stream's send lock (D4: stored before sent, never the other way round; E10: one lock with the replay). A failed
// send leaves the event stored: it goes out again on the next replay.
func (o *Outbox) AppendSend(typ string, body any, ts int64, send func(line []byte) error) (int64, error) {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	line, seq, err := o.append(typ, body, ts)
	if err != nil {
		return 0, err
	}
	return seq, send(line)
}

// Append stores one event with the stream's next seq and returns its line (newline included). It does not send:
// the gateway uses AppendSend, so that no other send comes between the two.
func (o *Outbox) Append(typ string, body any, ts int64) ([]byte, int64, error) {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	return o.append(typ, body, ts)
}

func (o *Outbox) append(typ string, body any, ts int64) ([]byte, int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, 0, ErrOutboxClosed
	}
	seq := o.state.Next
	line, err := Encode(Envelope{V: Version, Type: typ, Seq: seq, Ts: ts}, body)
	if err != nil {
		return nil, 0, err
	}
	line = append(line, '\n')
	if o.cur == nil || o.curCount >= o.perSegment {
		if err := o.rotate(seq); err != nil {
			return nil, 0, err
		}
	}
	if n, err := o.write(o.cur, line); err != nil {
		if n > 0 {
			if terr := o.truncate(o.cur, o.curSize); terr != nil {
				_ = o.cur.Close()
				o.cur = nil
				return nil, 0, errors.Join(err, terr)
			}
		}
		return nil, 0, err
	}
	o.curSize += int64(len(line))
	o.curCount++
	o.state.Next = seq + 1
	return line, seq, nil
}

// truncateAndSeek cuts f back to size and moves the write position there. The segment is not opened with O_APPEND (the
// outbox has one writer, under its mutex): on Windows an append-only handle cannot be truncated at all, so a failed
// write would leave its fragment in the middle of the segment.
func truncateAndSeek(f *os.File, size int64) error {
	if err := f.Truncate(size); err != nil {
		return err
	}
	_, err := f.Seek(size, io.SeekStart)
	return err
}

func (o *Outbox) rotate(first int64) error {
	if o.cur != nil {
		_ = o.cur.Close()
		o.cur = nil
	}
	// O_TRUNC: the segment named by the next seq holds no complete event (those would have seq >= first), only a
	// fragment that a failed write and a failed truncate left behind, if anything.
	f, err := os.OpenFile(o.segPath(first), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if n := len(o.segs); n == 0 || o.segs[n-1] != first {
		o.segs = append(o.segs, first)
	}
	o.cur, o.curCount, o.curSize = f, 0, 0
	return nil
}

// Replay calls send with every stored line whose seq is at least from, in order, under the stream's send lock: no
// new event is stored or sent until the replay is done. The state lock is not held across a send, so an ack is
// never kept waiting by a slow reader (P3 Task 4 minor).
func (o *Outbox) Replay(from int64, send func(line []byte) error) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return ErrOutboxClosed
	}
	segs := slices.Clone(o.segs)
	o.mu.Unlock()
	for i, first := range segs {
		if i+1 < len(segs) && segs[i+1] <= from {
			continue
		}
		data, err := os.ReadFile(o.segPath(first))
		if errors.Is(err, fs.ErrNotExist) {
			continue // an ack pruned it during the replay: every event in it is stored by the control plane
		}
		if err != nil {
			return err
		}
		for len(data) > 0 {
			nl := bytes.IndexByte(data, '\n')
			if nl < 0 {
				break
			}
			line := data[:nl+1]
			data = data[nl+1:]
			seq, err := seqOf(line[:nl])
			if err != nil {
				return err
			}
			if seq < from {
				continue
			}
			if err := send(line); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ack records that the control plane stored every event with seq <= upTo, and deletes the segments that are done.
func (o *Outbox) Ack(upTo int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutboxClosed
	}
	upTo = min(upTo, o.state.Next-1)
	if upTo <= o.state.Acked {
		return nil
	}
	prev := o.state.Acked
	o.state.Acked = upTo
	// stream.json first: a kill between the two leaves acknowledged segments behind (deleted on the next open), never
	// a stream.json whose next seq is behind events already sent (their seqs would be used twice).
	if err := o.save(); err != nil {
		o.state.Acked = prev // memory stays with what is on disk
		return err
	}
	return o.prune()
}

// prune deletes every segment whose events are all acknowledged (the open one is closed first).
func (o *Outbox) prune() error {
	keep := o.segs[:0]
	for i, first := range o.segs {
		last := o.state.Next - 1
		if i+1 < len(o.segs) {
			last = o.segs[i+1] - 1
		}
		if last > o.state.Acked {
			keep = append(keep, o.segs[i:]...)
			break
		}
		if i == len(o.segs)-1 && o.cur != nil {
			_ = o.cur.Close()
			o.cur = nil
		}
		if err := o.remove(o.segPath(first)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	o.segs = keep
	return nil
}

func (o *Outbox) save() error {
	data, err := json.Marshal(o.state)
	if err != nil {
		return err
	}
	tmp := filepath.Join(o.dir, "stream.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(o.dir, "stream.json"))
}

func (o *Outbox) Stream() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Stream
}

func (o *Outbox) Acked() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Acked
}

// Next is the seq the next Append gets.
func (o *Outbox) Next() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next
}

// Depth is the number of events not acknowledged yet.
func (o *Outbox) Depth() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next - 1 - o.state.Acked
}

// Segments lists the first seq of each segment file (for tests and health).
func (o *Outbox) Segments() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.segs)
}

// Close writes stream.json, closes the open segment and releases the lock. It takes the send lock first, so a send in
// progress finishes before it and nothing is appended or sent after it (P4f entry 4, T1-a). A second Close does
// nothing (T1-b): stream.json is written only while this outbox still holds the lock, which another gateway may hold
// by then.
func (o *Outbox) Close() error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	var err error
	if o.lock != nil {
		err = o.save()
	}
	o.release()
	return err
}

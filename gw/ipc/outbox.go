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
type Outbox struct {
	dir        string
	perSegment int
	mu         sync.Mutex
	state      streamState
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

const segmentExt = ".jsonl"

// OpenOutbox opens (or creates) the outbox in dir.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := &Outbox{dir: dir, perSegment: 1000, remove: os.Remove, write: (*os.File).Write, truncate: truncateAndSeek}
	data, err := os.ReadFile(filepath.Join(dir, "stream.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &o.state); err != nil || !patterns["hex32"].MatchString(o.state.Stream) {
			return nil, fmt.Errorf("outbox: %s is damaged", filepath.Join(dir, "stream.json"))
		}
	case errors.Is(err, fs.ErrNotExist):
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
			return fmt.Errorf("outbox: %s: %w", path, err)
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

// Append stores one event with the stream's next seq and returns its line (newline included), ready to send.
func (o *Outbox) Append(typ string, body any, ts int64) ([]byte, int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
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

// Replay calls send with every stored line whose seq is at least from, in order.
func (o *Outbox) Replay(from int64, send func(line []byte) error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, first := range o.segs {
		if i+1 < len(o.segs) && o.segs[i+1] <= from {
			continue
		}
		data, err := os.ReadFile(o.segPath(first))
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

// Close writes stream.json and closes the open segment.
func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cur != nil {
		_ = o.cur.Close()
		o.cur = nil
	}
	return o.save()
}

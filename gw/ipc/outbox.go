package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Outbox is the gateway's durable event stream. In this first version it holds the stream's identity only
// (stream.json: {stream, next, acked}); Task 4 of the P3 plan adds the segments, Append, Replay and Ack.
type Outbox struct {
	dir   string
	mu    sync.Mutex
	state streamState
}

type streamState struct {
	Stream string `json:"stream"`
	Next   int64  `json:"next"`
	Acked  int64  `json:"acked"`
}

// OpenOutbox opens (or creates) the outbox in dir.
func OpenOutbox(dir string) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := &Outbox{dir: dir}
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
	return o, o.save()
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

func (o *Outbox) Stream() string { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Stream }
func (o *Outbox) Acked() int64   { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Acked }
func (o *Outbox) Next() int64    { o.mu.Lock(); defer o.mu.Unlock(); return o.state.Next }

// Depth is the number of events not acknowledged yet.
func (o *Outbox) Depth() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state.Next - 1 - o.state.Acked
}

func (o *Outbox) Close() error { o.mu.Lock(); defer o.mu.Unlock(); return o.save() }

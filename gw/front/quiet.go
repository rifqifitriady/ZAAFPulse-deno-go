package front

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// noisy are the log lines the public port can be made to print at will: a failed TLS handshake (scanners, plain HTTP
// to the TLS port) and a request the control plane did not answer. Node printed neither, so each kind is written at
// most once per QuietEvery, followed by how many were left out (P4a final review m3, P4b entry condition 6).
var noisy = []string{"TLS handshake error", "front: "}

// QuietEvery is how often one noisy kind of line may be logged.
const QuietEvery = time.Minute

// quietWriter is the io.Writer of the front's loggers (http.Server.ErrorLog, the proxy's 502 line).
type quietWriter struct {
	out  *log.Logger
	now  func() time.Time
	mu   sync.Mutex
	last map[string]time.Time
	held map[string]int
}

// QuietLogger wraps out: noisy lines are rate-limited per kind, every other line goes through as it is.
func QuietLogger(out *log.Logger, now func() time.Time) *log.Logger {
	return log.New(&quietWriter{out: out, now: now, last: map[string]time.Time{}, held: map[string]int{}}, "", 0)
}

func (q *quietWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	kind := ""
	for _, k := range noisy {
		if strings.Contains(line, k) {
			kind = k
			break
		}
	}
	if kind == "" {
		q.out.Print(line)
		return len(p), nil
	}
	q.mu.Lock()
	t := q.now()
	if last, ok := q.last[kind]; ok && t.Sub(last) < QuietEvery {
		q.held[kind]++
		q.mu.Unlock()
		return len(p), nil
	}
	n := q.held[kind]
	q.last[kind], q.held[kind] = t, 0
	q.mu.Unlock()
	if n > 0 {
		line += fmt.Sprintf(" (%d more like this in the last minute were not logged)", n)
	}
	q.out.Print(line)
	return len(p), nil
}

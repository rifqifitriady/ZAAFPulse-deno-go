//go:build !race

package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The gateway's memory per held stream (P4c Decision 11): at most 80 KiB of heap and stack. Measured 2026-11-13 at about
// 67 KiB: ~46 KiB heap (net/http's per-connection bufio buffers on both sides, the 4 KiB read buffer, headers and
// contexts) and ~27 KiB of stacks (4 goroutines: server conn, its background read, the transport's read and write
// loops). The binding target is M4 (500 streams, gateway RSS <= 80 MB). The race detector inflates
// memory, so this file is built without -race; CI runs it in the plain `go test` of the go job.
func TestMemoryPerHeldStream(t *testing.T) {
	if os.Getenv("ZP_MEM_CHILD") != "" {
		memChild(t)
		return
	}
	const streams = 200
	hold := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer provider.Close()
	defer close(hold)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryPerHeldStream$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ZP_MEM_CHILD=1", "ZP_MEM_PROVIDER="+provider.URL)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	rd := bufio.NewReader(stdout)
	readJSON := func(v any) {
		for {
			l, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("child: %v", err)
			}
			if strings.HasPrefix(l, "{") {
				_ = json.Unmarshal([]byte(l), v)
				return
			}
		}
	}
	var ready struct{ URL, Key string }
	readJSON(&ready)
	var before struct{ Bytes uint64 }
	_, _ = io.WriteString(stdin, "measure\n")
	readJSON(&before)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: streams}}
	for i := 0; i < streams; i++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", ready.URL, strings.NewReader(streamBody))
		req.Header.Set("Authorization", ready.Key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
	}
	var after struct{ Bytes uint64 }
	_, _ = io.WriteString(stdin, "measure\n")
	readJSON(&after)
	per := (after.Bytes - before.Bytes) / streams
	t.Logf("gateway heap+stack per held stream: %d bytes", per)
	if per > 80<<10 {
		t.Fatalf("%d bytes per held stream, over the 80 KiB budget", per)
	}
}

// memChild serves chat streams on loopback and answers "measure" on stdin with its heap and stack in use.
func memChild(t *testing.T) {
	g := newStreamRigAt(t, os.Getenv("ZP_MEM_PROVIDER"))
	fmt.Printf("{\"URL\":%q,\"Key\":%q}\n", g.url, g.key)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		time.Sleep(200 * time.Millisecond)
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Printf("{\"Bytes\":%d}\n", m.HeapInuse+m.StackInuse)
	}
}

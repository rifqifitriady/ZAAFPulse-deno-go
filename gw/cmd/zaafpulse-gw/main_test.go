package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

func runGw(t *testing.T, stdin io.Reader, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := run(args, stdin, io.Discard, &stderr)
	return code, stderr.String()
}

// E1: a second gateway on the same outbox exits 5 ("outbox in use") and leaves the first one's outbox alone.
func TestSecondGatewayOnTheSameOutboxExits5(t *testing.T) {
	dir := t.TempDir()
	box, err := ipc.OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	code, log := runGw(t, strings.NewReader(""), "--outbox", dir)
	if code != ipc.ExitOutboxInUse {
		t.Fatalf("exit %d: %s", code, log)
	}
	if !strings.Contains(log, "in use") {
		t.Fatalf("log: %s", log)
	}
}

// Once the first gateway is gone, the next one starts (and exits 0 at the end of its input).
func TestGatewayStartsOnceTheOutboxIsFree(t *testing.T) {
	dir := t.TempDir()
	box, err := ipc.OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = box.Close()
	if code, log := runGw(t, strings.NewReader(""), "--outbox", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, log)
	}
}

// E2: segments without stream.json are a damaged outbox: exit 1, and no new stream is started.
func TestSegmentsWithoutStreamFileExit1(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "00000000000000000001.jsonl")
	if err := os.WriteFile(seg, []byte(`{"v":1,"type":"audit.event","seq":1,"ts":1,"body":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, log := runGw(t, strings.NewReader(""), "--outbox", dir)
	if code != ipc.ExitDamaged {
		t.Fatalf("exit %d: %s", code, log)
	}
	if _, err := os.Stat(filepath.Join(dir, "stream.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a new stream was started: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read /dev/stdin: input/output error")
}

// E11: a stdin read error other than end of file is an I/O failure: exit 1, not the protocol error's 3.
func TestStdinReadErrorExits1(t *testing.T) {
	code, log := runGw(t, failingReader{}, "--outbox", t.TempDir())
	if code != ipc.ExitDamaged {
		t.Fatalf("exit %d: %s", code, log)
	}
	if !strings.Contains(log, "input/output error") {
		t.Fatalf("log: %s", log)
	}
}

// A line that is not JSON stays a protocol error: exit 3.
func TestBadLineExits3(t *testing.T) {
	code, log := runGw(t, strings.NewReader("not json\n"), "--outbox", t.TempDir())
	if code != ipc.ExitProtocol {
		t.Fatalf("exit %d: %s", code, log)
	}
}

func TestBadCommandLineExits2(t *testing.T) {
	if code, _ := runGw(t, strings.NewReader(""), "--protocol", "x", "--outbox", t.TempDir()); code != ipc.ExitRefused {
		t.Fatalf("exit %d", code)
	}
}

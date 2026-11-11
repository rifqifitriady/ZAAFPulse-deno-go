package ipc

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineReaderSplitsLinesSkipsEmptyAndStripsOneCR(t *testing.T) {
	r := NewLineReader(strings.NewReader("a\n\nbc\r\nd\r\r\ne"), 10)
	for _, want := range []string{"a", "bc", "d\r"} {
		line, err := r.Next()
		if err != nil || string(line) != want {
			t.Fatalf("got %q, %v; want %q", line, err, want)
		}
	}
	if _, err := r.Next(); !errors.Is(err, ErrPartialLine) {
		t.Fatalf("a stream ending inside a line: got %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(""), 10).Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("an empty stream: got %v", err)
	}
}

func TestLineReaderLimit(t *testing.T) {
	ok := strings.Repeat("x", 10)
	if line, err := NewLineReader(strings.NewReader(ok+"\n"), 10).Next(); err != nil || string(line) != ok {
		t.Fatalf("a line of exactly the limit: %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(ok+"x\n"), 10).Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("one byte over: got %v", err)
	}
	// A line longer than bufio's 64 KiB buffer but under the limit; and one over the limit with no newline yet.
	long := strings.Repeat("y", 150000)
	if line, err := NewLineReader(strings.NewReader(long+"\n"), 200000).Next(); err != nil || len(line) != 150000 {
		t.Fatalf("a long line: %v", err)
	}
	if _, err := NewLineReader(strings.NewReader(long), 100000).Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("over the limit without a newline: got %v", err)
	}
}

// ipc/v1/framing/crlf.ndjson: a message ended by \r\n and one by \n both decode.
func TestCRLFFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "ipc", "v1", "framing", "crlf.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := NewLineReader(f, MaxLine)
	for seq := int64(1); seq <= 2; seq++ {
		line, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		msg, err := Decode(line, true)
		if err != nil || msg.Seq != seq {
			t.Fatalf("message %d: %v", seq, err)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}

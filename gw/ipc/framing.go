package ipc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxLine is the longest message line either side accepts (D2): 8 MiB, the newline not counted.
const MaxLine = 8 << 20

var (
	// ErrLineTooLong is a protocol error: the channel closes.
	ErrLineTooLong = errors.New("ipc: a message line is longer than 8 MiB")
	// ErrPartialLine: the stream ended inside a line (the other side died while writing).
	ErrPartialLine = errors.New("ipc: the stream ended inside a message line")
)

// LineReader splits a stream into message lines. It removes one trailing \r and skips empty lines.
type LineReader struct {
	br  *bufio.Reader
	max int
}

// NewLineReader reads lines of at most max bytes (MaxLine when max <= 0).
func NewLineReader(r io.Reader, max int) *LineReader {
	if max <= 0 {
		max = MaxLine
	}
	return &LineReader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// Next returns the next non-empty line without its line end; io.EOF at a clean end of the stream.
func (l *LineReader) Next() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := l.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			line := buf[:len(buf)-1]
			if len(line) > l.max {
				return nil, ErrLineTooLong
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				buf = buf[:0]
				continue
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(buf) > l.max {
				return nil, ErrLineTooLong
			}
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, ErrPartialLine
		default:
			return nil, err
		}
	}
}

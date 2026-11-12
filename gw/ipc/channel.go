package ipc

import (
	"errors"
	"io"
	"sync"
	"time"
)

// Transport is the one interface both sides put the IPC behind (D1): stdio today, a network transport later
// without changing a message.
type Transport interface {
	// Send writes a control message with this connection's next seq.
	Send(typ string, body any, id, re string) error
	// SendLine writes an encoded line as it is (an outbox event, which carries its own durable seq).
	SendLine(line []byte) error
	// Recv reads the next message: io.EOF or ErrPartialLine when the other side is gone, *ProtocolError otherwise.
	Recv() (Message, error)
	Close() error
}

// ProtocolError is a line that is too long, not JSON, or not a valid v1 message.
type ProtocolError struct{ Err error }

func (e *ProtocolError) Error() string { return "protocol error: " + e.Err.Error() }
func (e *ProtocolError) Unwrap() error { return e.Err }

// Channel is the stdio Transport: messages in from in, out to out, one per line.
type Channel struct {
	in     *LineReader
	out    io.Writer
	strict bool
	mu     sync.Mutex
	seq    int64
	Now    func() time.Time
}

var _ Transport = (*Channel)(nil)

// NewChannel reads from in and writes to out. strict decodes as the tests do (unknown fields refused).
func NewChannel(in io.Reader, out io.Writer, strict bool) *Channel {
	return &Channel{in: NewLineReader(in, MaxLine), out: out, strict: strict, Now: time.Now}
}

func (c *Channel) Send(typ string, body any, id, re string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	line, err := Encode(Envelope{V: Version, Type: typ, Seq: c.seq, Ts: c.Now().UnixMilli(), ID: id, Re: re}, body)
	if err != nil {
		return err
	}
	_, err = c.out.Write(append(line, '\n'))
	return err
}

func (c *Channel) SendLine(line []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line[:len(line):len(line)], '\n')
	}
	_, err := c.out.Write(line)
	return err
}

func (c *Channel) Recv() (Message, error) {
	line, err := c.in.Next()
	if err != nil {
		if errors.Is(err, ErrLineTooLong) {
			return Message{}, &ProtocolError{err}
		}
		return Message{}, err
	}
	msg, err := Decode(line, c.strict)
	if err != nil {
		return Message{}, &ProtocolError{err}
	}
	return msg, nil
}

func (c *Channel) Close() error {
	if cl, ok := c.out.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}

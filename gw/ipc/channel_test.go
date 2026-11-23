package ipc

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestChannelSendRecv(t *testing.T) {
	r, w := io.Pipe()
	out := NewChannel(strings.NewReader(""), w, true)
	out.Now = func() time.Time { return time.UnixMilli(1767225600000) }
	in := NewChannel(r, io.Discard, true)
	go func() {
		_ = out.Send(TypeHello, Hello{Protocol: []int{1}, GwVersion: "t", Stream: strings.Repeat("a", 32), OutboxFrom: 1}, "h1", "")
		_ = out.Send(TypeReady, Ready{Gen: 1}, "", "")
		_ = out.SendLine([]byte(`{"v":1,"type":"telemetry.v9","seq":7,"ts":0,"body":{}}`))
		_ = out.SendLine([]byte("not json\n"))
		w.Close()
	}()
	m, err := in.Recv()
	if err != nil || m.Type != TypeHello || m.Seq != 1 || m.ID != "h1" || m.Ts != 1767225600000 {
		t.Fatalf("hello: %+v %v", m, err)
	}
	if m.Body.(*Hello).OutboxFrom != 1 {
		t.Fatal("hello body")
	}
	if m, err = in.Recv(); err != nil || m.Type != TypeReady || m.Seq != 2 || m.Body.(*Ready).Gen != 1 {
		t.Fatalf("ready: %+v %v", m, err)
	}
	if m, err = in.Recv(); err != nil || !m.Unknown || m.Type != "telemetry.v9" {
		t.Fatalf("an unknown type is reported, not refused: %+v %v", m, err)
	}
	var pe *ProtocolError
	if _, err = in.Recv(); !errors.As(err, &pe) {
		t.Fatalf("a line that is not JSON: got %v", err)
	}
	if _, err = in.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}

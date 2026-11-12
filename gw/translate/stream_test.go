package translate

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadSSEOneByteAtATime(t *testing.T) {
	text := "event: a\r\ndata: 1\r\n\r\ndata: é😀\ndata:2\n\n: c\n\nevent: b\n\ndata: tail"
	collect := func(r interface{ Read([]byte) (int, error) }) []string {
		var got []string
		if err := ReadSSE(r, func(ev, data string) error { got = append(got, ev+"|"+data); return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	whole := collect(strings.NewReader(text))
	bytewise := collect(iotest.OneByteReader(strings.NewReader(text)))
	if strings.Join(whole, ";") != "a|1;message|é😀\n2" || strings.Join(bytewise, ";") != strings.Join(whole, ";") {
		t.Fatalf("whole %q bytewise %q", whole, bytewise)
	}
}

func TestReadSSECallbackErrorStops(t *testing.T) {
	stop := errors.New("stop")
	n := 0
	err := ReadSSE(strings.NewReader("data: 1\n\ndata: 2\n\n"), func(string, string) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("err %v after %d events", err, n)
	}
	if err := ReadSSE(iotest.ErrReader(stop), func(string, string) error { return nil }); !errors.Is(err, stop) {
		t.Fatalf("read error %v", err)
	}
}

package translate

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"time"
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

// M3 (review): one large event arriving in small reads is scanned once, not from the start after each read, and a
// buffer past MaxSSEEvent is refused.
func TestReadSSELargeEventIsLinearAndBounded(t *testing.T) {
	big := "data: " + strings.Repeat("x", 10<<20) + "\n\n"
	r := iotest.HalfReader(strings.NewReader(big))
	start := time.Now()
	n := 0
	if err := ReadSSE(&smallReads{r: r, n: 32 << 10}, func(_, d string) error { n += len(d); return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 10<<20 || time.Since(start) > 5*time.Second {
		t.Fatalf("%d bytes in %v", n, time.Since(start))
	}
	huge := strings.NewReader("data: " + strings.Repeat("y", MaxSSEEvent+1))
	if err := ReadSSE(huge, func(string, string) error { return nil }); !errors.Is(err, ErrSSETooLarge) {
		t.Fatalf("got %v", err)
	}
}

type smallReads struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (s *smallReads) Read(p []byte) (int, error) {
	if len(p) > s.n {
		p = p[:s.n]
	}
	return s.r.Read(p)
}

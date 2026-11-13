package engine

import (
	"bytes"
	"strings"
	"testing"
)

func clock7() float64 { return 7 } // every ttft reads 7 ms

// feedAll runs the bytes through a fresh ChatStream split at the given offsets, then End when clean is true.
func feedAll(t *testing.T, model string, wants bool, b []byte, cuts []int, clean bool) (string, *ChatStream) {
	t.Helper()
	s := NewChatStream(model, wants, clock7)
	var out bytes.Buffer
	last := 0
	for _, c := range append(cuts, len(b)) {
		w, err := s.Feed(b[last:c])
		if err != nil {
			t.Fatal(err)
		}
		out.Write(w)
		last = c
	}
	if clean {
		out.Write(s.End())
	}
	return out.String(), s
}

const upstreamStream = ": keep-alive\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","model":"m1-upstream","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}],"usage":null}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","model":"m1-upstream","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}],"usage":null}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","model":"m1-upstream","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}` + "\n\n" +
	"data: [DONE]\n\n"

// The contract upstream's stream, without include_usage: comments pass, the model is the requested name, "usage": null
// is gone, the usage-only chunk and its blank line are dropped, [DONE] passes; usage and ttft are read.
func TestChatStreamWithoutUsage(t *testing.T) {
	got, s := feedAll(t, "oa/m1", false, []byte(upstreamStream), nil, true)
	want := ": keep-alive\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","model":"oa/m1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","model":"oa/m1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if s.Input != 10 || s.Output != 2 || s.Cached != 0 || s.TTFT == nil || *s.TTFT != 7 {
		t.Fatalf("usage %d %d %d ttft %v", s.Input, s.Output, s.Cached, s.TTFT)
	}
}

// With include_usage everything passes (usage null kept, the usage chunk kept).
func TestChatStreamWithUsage(t *testing.T) {
	got, _ := feedAll(t, "oa/m1", true, []byte(upstreamStream), nil, true)
	if strings.Count(got, `"usage":null`) != 2 || !strings.Contains(got, `"choices":[],"usage":{"prompt_tokens":10`) {
		t.Fatalf("%s", got)
	}
}

// The output does not depend on how the provider's bytes were split: every single cut and byte-by-byte feeding give
// the one-shot output, for the contract stream and for awkward inputs.
func TestChatStreamSplitIndependent(t *testing.T) {
	inputs := []string{
		upstreamStream,
		"\xef\xbb\xbf" + upstreamStream, // a byte order mark at the start
		"data: {\"model\":\"x\",\"t\":\"caf\xc3\xa9\"}\r\n\r\n", // CRLF and a two-byte character
		"data: {\"model\":\"x\"}\n\n\u00a0\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1}}\n \t\n:c\n",
		"data: not json\n\ndata: [1,2]\n\ndata: {\"choices\":{\"0\":{\"delta\":{\"tool_calls\":[]}}}}\n\ndata: {\"x\":1}",
		"data: {\"a\":\"\xff\xfe\"}\n\ndata: {\"b\":\"\xe2\x82\"}\n",
	}
	for _, in := range inputs {
		for _, wants := range []bool{false, true} {
			one, _ := feedAll(t, "M", wants, []byte(in), nil, true)
			for c := 0; c <= len(in); c++ {
				if got, _ := feedAll(t, "M", wants, []byte(in), []int{c}, true); got != one {
					t.Fatalf("cut at %d of %q (usage %v):\n got %q\nwant %q", c, in, wants, got, one)
				}
			}
			cuts := make([]int, 0, len(in))
			for c := 1; c < len(in); c++ {
				cuts = append(cuts, c)
			}
			if got, _ := feedAll(t, "M", wants, []byte(in), cuts, true); got != one {
				t.Fatalf("byte by byte %q:\n got %q\nwant %q", in, got, one)
			}
		}
	}
}

// TextDecoder's edges: a BOM split across reads is dropped once; a BOM later in the stream is kept; an incomplete
// UTF-8 tail at the end is dropped; an invalid byte is U+FFFD; a final line without \n gets one; a cut stream writes
// no partial line.
func TestChatStreamDecoderEdges(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		clean          bool
	}{
		{"bom split", "\xef\xbb\xbfx\n", "x\n", true},
		{"bom only, partial", "\xef\xbb", "", true},
		{"bom later kept", "a\n\xef\xbb\xbfb\n", "a\n\ufeffb\n", true},
		{"incomplete tail dropped", "a\nb\xe2\x82", "a\nb\n", true},
		{"invalid tail kept as U+FFFD", "a\nb\xff", "a\nb\ufffd\n", true},
		{"final line gets a newline", "a\nb", "a\nb\n", true},
		{"cut: no partial line", "a\nb", "a\n", false},
		{"cr dropped once", "a\r\r\n", "a\r\n", true},
	} {
		got, _ := feedAll(t, "M", false, []byte(c.in), []int{min(1, len(c.in))}, c.clean)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A line past MaxStreamLine is an error (the serve loop ends the stream as a cut).
func TestChatStreamLongLine(t *testing.T) {
	s := NewChatStream("M", false, clock7)
	if _, err := s.Feed(bytes.Repeat([]byte("x"), MaxStreamLine+1)); err != ErrStreamLine {
		t.Fatalf("%v", err)
	}
}

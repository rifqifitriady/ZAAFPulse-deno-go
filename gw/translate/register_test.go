package translate

import (
	"io"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

func arg(a []jsv.Value, i int) jsv.Value { return jsv.Arg(a, i) }

func strList(v jsv.Value) []string {
	a := jsv.Ary(v)
	if a == nil {
		return nil
	}
	out := make([]string, len(a.Items))
	for i, x := range a.Items {
		out[i] = jsv.ToString(x)
	}
	return out
}

func objOut(o *jsv.Object, err error) (jsv.Value, error) {
	if err != nil {
		return nil, err
	}
	return o, nil
}

func init() {
	fixtureFns["anthropicToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		t, err := e.AnthropicToOpenAI(arg(a, 0))
		if err != nil {
			return nil, err
		}
		return t.Value(), nil
	}
	fixtureFns["openAIToAnthropic"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.OpenAIToAnthropic(arg(a, 0), arg(a, 1)))
	}
	fixtureFns["openAIToAnthropicRequest"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.OpenAIToAnthropicRequest(arg(a, 0)))
	}
	fixtureFns["anthropicUsageToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(AnthropicUsageToOpenAI(arg(a, 0)))
	}
	fixtureFns["anthropicResponseToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.AnthropicResponseToOpenAI(arg(a, 0), arg(a, 1)))
	}
	fixtureFns["responsesToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		t, custom, err := e.ResponsesToOpenAI(arg(a, 0))
		if err != nil {
			return nil, err
		}
		return t.Value().Set("custom", strs(custom)), nil
	}
	fixtureFns["openAIToResponses"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.OpenAIToResponses(arg(a, 0), arg(a, 1), strList(arg(a, 2))))
	}
	fixtureFns["geminiToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.GeminiToOpenAI(arg(a, 0), arg(a, 1), arg(a, 2)))
	}
	fixtureFns["openAIToGemini"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return objOut(e.OpenAIToGemini(arg(a, 0), arg(a, 1)))
	}
	fixtureFns["estimateTokens"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		return float64(EstimateTokens(arg(a, 0))), nil
	}
}

// runOps replays a generator op list ([["open"], ["push", chunk], ["finish"], ["ping"], ["error", msg]]).
func runOps(ops jsv.Value, do func(op string, arg jsv.Value) (string, error)) (jsv.Value, error) {
	out := []jsv.Value{}
	for _, o := range jsv.Ary(ops).Items {
		op := jsv.Ary(o)
		s, err := do(jsv.ToString(op.Items[0]), jsv.Arg(op.Items, 1))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return jsv.Arr(out...), nil
}

func init() {
	fixtureFns["anthropicStream"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		s := e.NewAnthropicStream(arg(a, 0))
		return runOps(arg(a, 1), func(op string, x jsv.Value) (string, error) {
			switch op {
			case "open":
				return s.Open(), nil
			case "push":
				return s.Push(x)
			case "finish":
				return s.Finish()
			case "ping":
				return s.Ping(), nil
			}
			return s.Error(x), nil
		})
	}
	fixtureFns["responsesStream"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		s := e.NewResponsesStream(arg(a, 0), strList(arg(a, 1)))
		return runOps(arg(a, 2), func(op string, x jsv.Value) (string, error) {
			switch op {
			case "open":
				return s.Open(), nil
			case "push":
				return s.Push(x)
			case "finish":
				return s.Finish()
			}
			return s.Error(x), nil
		})
	}
	fixtureFns["geminiStream"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		s := e.NewGeminiStream(arg(a, 0))
		return runOps(arg(a, 1), func(op string, x jsv.Value) (string, error) {
			switch op {
			case "push":
				return s.Push(x)
			case "finish":
				return s.Finish()
			}
			return s.Error(x), nil
		})
	}
	fixtureFns["anthropicEventsToOpenAI"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		f := e.NewAnthropicEvents(arg(a, 0))
		out := []jsv.Value{}
		for _, ev := range jsv.Ary(arg(a, 1)).Items {
			cs, err := f.Next(ev)
			if err != nil {
				return nil, err
			}
			out = append(out, jsv.Arr(cs...))
		}
		return jsv.Arr(out...), nil
	}
	fixtureFns["wholeToChunks"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		cs, err := WholeToChunks(arg(a, 0))
		if err != nil {
			return nil, err
		}
		return jsv.Arr(cs...), nil
	}
	fixtureFns["readSse"] = func(e *Env, a []jsv.Value) (jsv.Value, error) {
		var chunks [][]byte
		for _, c := range jsv.Ary(arg(a, 0)).Items {
			if s, ok := c.(string); ok {
				chunks = append(chunks, []byte(s))
				continue
			}
			var b []byte
			for _, x := range jsv.Ary(c).Items {
				b = append(b, byte(x.(float64)))
			}
			chunks = append(chunks, b)
		}
		out := []jsv.Value{}
		err := ReadSSE(&chunkReader{chunks: chunks}, func(ev, data string) error {
			out = append(out, jsv.Arr(ev, data))
			return nil
		})
		return jsv.Arr(out...), err
	}
}

// chunkReader returns one chunk per Read, like the JS ReadableStream in the generator.
type chunkReader struct{ chunks [][]byte }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

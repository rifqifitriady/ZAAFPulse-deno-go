package translate

import "github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"

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

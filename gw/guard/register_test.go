package guard

import "github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"

func hitsOf(v jsv.Value) []Hit {
	var out []Hit
	if a := jsv.Ary(v); a != nil {
		for _, h := range a.Items {
			out = append(out, Hit{Check: jsv.ToString(jsv.Get(h, "check")), Label: jsv.ToString(jsv.Get(h, "label")),
				Count: int(jsv.ToNumber(jsv.Get(h, "count")))})
		}
	}
	return out
}

func actionValue(a string) jsv.Value {
	if a == "" {
		return nil
	}
	return a
}

func init() {
	fixtureFns["cleanPolicy"] = func(a []jsv.Value) (jsv.Value, error) { return CleanPolicy(jsv.Arg(a, 0)).Value(), nil }
	fixtureFns["resolvePolicy"] = func(a []jsv.Value) (jsv.Value, error) {
		return ResolvePolicy(jsv.Arg(a, 0), jsv.Arg(a, 1)).Value(), nil
	}
	fixtureFns["blockMessage"] = func(a []jsv.Value) (jsv.Value, error) { return BlockMessage(hitsOf(jsv.Arg(a, 0))), nil }
	fixtureFns["checkBody"] = func(a []jsv.Value) (jsv.Value, error) {
		r, err := CheckBody(jsv.Arg(a, 0), PolicyOf(jsv.Arg(a, 1)), jsv.ToString(jsv.Arg(a, 2)))
		if err != nil {
			return nil, err
		}
		return jsv.NewObject().Set("body", r.Body).Set("action", actionValue(r.Action)).
			Set("hits", hitsValue(r.Hits)).Set("error", r.Error), nil
	}
	fixtureFns["checkText"] = func(a []jsv.Value) (jsv.Value, error) {
		r, err := CheckText(jsv.Arg(a, 0), PolicyOf(jsv.Arg(a, 1)), jsv.ToString(jsv.Arg(a, 2)))
		if err != nil {
			return nil, err
		}
		return jsv.NewObject().Set("text", r.Text).Set("action", actionValue(r.Action)).
			Set("hits", hitsValue(r.Hits)).Set("error", r.Error), nil
	}
}

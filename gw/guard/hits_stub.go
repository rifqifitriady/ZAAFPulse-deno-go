package guard

import "github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"

// Hit is one finding: the check, a label and how often; never the matched text.
type Hit struct {
	Check, Label string
	Count        int
}

// Block is the GuardrailBlock error.
type Block struct{ Hits []Hit }

func (b *Block) Error() string { return "" }

func hitsValue(hits []Hit) *jsv.Array {
	a := jsv.Arr()
	for _, h := range hits {
		a.Push(jsv.NewObject().Set("check", h.Check).Set("label", h.Label).Set("count", float64(h.Count)))
	}
	return a
}

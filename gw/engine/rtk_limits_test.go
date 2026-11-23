package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

func toolBody(text, hint string) *Value {
	b, _ := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c", "function": map[string]any{
			"name": "Bash", "arguments": `{"command":"` + hint + `"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "c", "content": text}}})
	v, err := Parse(b)
	if err != nil {
		panic(err)
	}
	return v
}

// Spec D11: legitimate texts stay far under the limit. Every fixture text and the big recipes use at most a quarter
// of 64 x units + 2^24. The constant matters for short texts: the head patterns (`^\s*\d+[\t|→]` with m on a
// whitespace-only head) are quadratic in the 2048-unit head in both engines; measured cap 4.24M steps (2048 blank
// lines), a quarter of 2^24 (spec Decision 23).
func TestSaverStepsPerUnit(t *testing.T) {
	worst := 0.0
	for _, c := range loadFixtures(t, "rtk") {
		var in []json.RawMessage
		_ = json.Unmarshal(c.In, &in)
		var text, hint string
		switch c.Fn {
		case "compressText":
			if json.Unmarshal(in[0], &text) != nil {
				continue
			}
		case "compressBig":
			text = buildRecipe(t, in[0])
		default:
			continue
		}
		_ = json.Unmarshal(in[1], &hint)
		if len(text) < rtkMinChars || strings.HasPrefix(c.Name, "goHandoff-") {
			continue
		}
		b := &jsre.Budget{Limit: 1 << 62}
		if err := b.Run(func() { compressText(u16(text), u16(hint), b) }); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if r := float64(b.Steps) / float64(64*int64(units(text))+1<<24); r > worst {
			worst = r
		}
	}
	t.Logf("worst share of the step limit: %.3f", worst)
	if worst > 0.25 {
		t.Fatalf("a fixture uses %.0f%% of its step limit: less than 4x headroom", worst*100)
	}
}

// An ANSI OSC opener repeated without a terminator is quadratic in both engines: the budget hands it off.
func TestSaverOverBudgetHandsOff(t *testing.T) {
	text := strings.Repeat("\x1b]", 300000)
	body := toolBody(text, "x")
	j, why := scanSaver(body, "openai")
	if why != "" || j == nil {
		t.Fatalf("scan: %v %q", j, why)
	}
	if _, why := j.run(j.budget(time.Now())); why != saverOverBudget {
		t.Fatalf("got %q", why)
	}
}

// Spec Decision 23: the quadratic head scan's cap (2048 blank lines) is served, not handed off.
func TestSaverBlankLinesFit(t *testing.T) {
	for _, n := range []int{2100, 5000} {
		j, why := scanSaver(toolBody(strings.Repeat("\n", n), "x"), "openai")
		if why != "" || j == nil {
			t.Fatalf("scan: %v %q", j, why)
		}
		if _, why := j.run(j.budget(time.Now())); why != "" {
			t.Fatalf("%d blank lines: %s", n, why)
		}
	}
}

// The output is always valid UTF-16 (spec D6: a cut pair is written as an escape, never kept).
func TestSaverOutputValidUTF16(t *testing.T) {
	for _, c := range loadFixtures(t, "rtk") {
		if c.Fn != "compressText" || strings.HasPrefix(c.Name, "goHandoff-") {
			continue
		}
		var in []json.RawMessage
		_ = json.Unmarshal(c.In, &in)
		var text, hint string
		if json.Unmarshal(in[0], &text) != nil {
			continue
		}
		_ = json.Unmarshal(in[1], &hint)
		out, hit, _ := compressText(u16(text), u16(hint), nil)
		if hit == nil {
			continue
		}
		for i := 0; i < len(out); i++ {
			switch {
			case utf16.IsSurrogate(rune(out[i])) && out[i] < 0xdc00 && i+1 < len(out) && out[i+1] >= 0xdc00 && out[i+1] <= 0xdfff:
				i++
			case utf16.IsSurrogate(rune(out[i])):
				t.Fatalf("%s: lone surrogate at %d", c.Name, i)
			}
		}
	}
}

func BenchmarkSaveTokens(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 10 << 20} {
		var sb strings.Builder
		for i := 0; sb.Len() < size; i++ {
			sb.WriteString("src/dir/file.go:")
			sb.WriteString(strings.Repeat("9", 1+i%5))
			sb.WriteString(": match here\n")
		}
		text := sb.String()
		b.Run(NumberString(float64(size>>10))+"KiB", func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				body := toolBody(text, "rg match") // the request parse is not the saver's
				b.StartTimer()
				SaveTokens(body, "openai", nil)
			}
		})
	}
}

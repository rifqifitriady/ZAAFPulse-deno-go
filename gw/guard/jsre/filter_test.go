package jsre

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// guardPatterns are every regex of the guard, read from the fixture the JS writes (gw/testdata/guard/patterns-all.json),
// plus the guard's own (guard.go, injection.go, secrets.go, pii.go).
func guardPatterns(t *testing.T) [][2]string {
	raw, err := os.ReadFile("../../testdata/guard/patterns-all.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Out struct {
			Injection []struct {
				Res [][2]string `json:"res"`
			} `json:"injection"`
			Secrets [][3]string          `json:"secrets"`
			Regex   map[string][2]string `json:"regex"`
		} `json:"out"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, c := range fx.Out.Injection {
		out = append(out, c.Res...)
	}
	for _, s := range fx.Out.Secrets {
		out = append(out, [2]string{s[1], s[2]})
	}
	for _, r := range fx.Out.Regex {
		out = append(out, r)
	}
	return append(out,
		[2]string{`^data:[\w.+-]+\/[\w.+-]+(?:;[\w=.+-]+)*;base64,[A-Za-z0-9+/=\s]*$`, "i"},
		[2]string{`-----END [A-Z ]*PRIVATE KEY-----`, ""},
		[2]string{`\S+@\S+|https?:\/\/\S+`, "g"},
		[2]string{`\b[a-z0-9@$]*[a-z][a-z0-9@$]*\b`, "gi"},
		[2]string{`\d`, ""}, [2]string{`[013457@$]`, "g"}, [2]string{`EXAMPLE$`, ""}, [2]string{`[•*]{3}`, ""},
		[2]string{`^\d{4}-\d{2}-\d{2}\b`, ""},
		// Shapes the guard does not use, for the filter's corner cases.
		[2]string{`(a|)b`, ""}, [2]string{`(?:ab)*c`, "i"}, [2]string{`(a)\1x`, ""}, [2]string{`x?y{0,2}z`, ""},
		[2]string{`\bk[^s]`, "i"}, [2]string{`(?=ab)\w\w`, ""}, [2]string{`\b(?:é|ab)c`, "i"},
		[2]string{`(?<![ab]|cd)\w`, "i"}, [2]string{`(?<!a)(?<=b)c`, ""}, [2]string{`(?<!x)(?:y|(?<!z)w)`, ""},
	)
}

// filterTexts: the fixture inputs of the guard and slices of the benchmark corpus.
func filterTexts(t *testing.T) [][]uint16 {
	var out [][]uint16
	files, _ := filepath.Glob("../../testdata/guard/*.json")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var fx struct {
			In []any `json:"in"`
		}
		if json.Unmarshal(raw, &fx) == nil && len(fx.In) > 0 {
			if s, ok := fx.In[0].(string); ok {
				out = append(out, U16(s))
			}
		}
	}
	list, err := os.ReadFile("../testdata/bench-corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(list), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || i%4 != 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(line)))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, U16(string(b[:min(len(b), 64<<10)])))
	}
	return append(out, U16("ab"), U16("b"), U16("kK sſ ééc ÉC"), U16(""), U16("aab ab c abab abc"))
}

// The start-position filters (first unit, word start, first two units) change no match: every guard pattern finds
// the same matches with and without them on the fixture inputs and the corpus.
func TestFiltersChangeNoMatch(t *testing.T) {
	if raceEnabled {
		t.Skip("slow under the race detector")
	}
	texts := filterTexts(t)
	for _, p := range guardPatterns(t) {
		re := MustCompile(p[0], p[1])
		for _, s := range texts {
			noFilter = false
			got := re.FindAll(s)
			noFilter = true
			want := re.FindAll(s)
			noFilter = false
			if !slices.EqualFunc(got, want, slices.Equal) {
				t.Fatalf("/%s/%s on %.60q: %v, want %v", p[0], p[1], Str(s), got, want)
			}
		}
	}
}

// classSet builds the same set as testing classMatch on each code unit.
func TestClassSetMatchesClassMatch(t *testing.T) {
	for _, src := range []string{`[a-z]`, `[^.\n]`, `[\w.+-]`, `[A-Za-z0-9+/=\s]`, `[^\s@'"` + "`" + `/]`, `[kK]`, `[ſs]`, `[K]`,
		`[\u00e9-\u00fc]`, `[\u00ad\u200b-\u200f\u2060\ufeff]`, `[^a-z\u00df]`, `[\d\W]`, `[\u01c5]`, `[\ud800-\udbff]`} {
		for _, icase := range []bool{false, true} {
			re := MustCompile(src, map[bool]string{false: "", true: "i"}[icase])
			n := re.root
			for n.kind == kGroup || n.kind == kSeq {
				n = n.subs[0]
			}
			if n.kind != kClass {
				t.Fatalf("%s: not a class", src)
			}
			var got bitset
			classSet(&got, n.cls, icase)
			for u := 0; u < 1<<16; u++ {
				if got.has(uint16(u)) != classMatch(n.cls, uint16(u), icase) {
					t.Fatalf("%s i=%v: U+%04X", src, icase, u)
				}
			}
		}
	}
}

// Multi finds what ExecB finds for each pattern.
func TestMultiMatchesExec(t *testing.T) {
	if raceEnabled {
		t.Skip("slow under the race detector")
	}
	var res []*Regexp
	for _, p := range guardPatterns(t) {
		if len(res) == 64 {
			break
		}
		res = append(res, MustCompile(p[0], p[1]))
	}
	mu := NewMulti(res)
	var st MultiState
	out := make([][]int, len(res))
	for i, s := range filterTexts(t) {
		active := ^uint64(0) >> (64 - len(res))
		if i%2 == 1 {
			active &= 0x5555_5555_5555_5555
		}
		mu.FirstB(&st, s, active, nil, out)
		for k, re := range res {
			if active&(1<<k) == 0 {
				continue
			}
			if want := re.Exec(s, 0); !slices.Equal(out[k], want) {
				t.Fatalf("/%s/ on %.60q: %v, want %v", re.String(), Str(s), out[k], want)
			}
		}
	}
	mu.Release(&st)
}

// AllB finds what EachB finds for each pattern.
func TestMultiAllMatchesEach(t *testing.T) {
	if raceEnabled {
		t.Skip("slow under the race detector")
	}
	var res []*Regexp
	for _, p := range guardPatterns(t) {
		if len(res) == 64 {
			break
		}
		res = append(res, MustCompile(p[0], p[1]))
	}
	mu := NewMulti(res)
	var st MultiState
	for _, s := range filterTexts(t) {
		got := make([][][]int, len(res))
		mu.AllB(&st, s, ^uint64(0)>>(64-len(res)), nil, func(k int, c []int) int {
			got[k] = append(got[k], slices.Clone(c))
			if c[1] == c[0] {
				return c[1] + 1
			}
			return c[1]
		})
		for k, re := range res {
			if want := re.FindAll(s); !slices.EqualFunc(got[k], want, slices.Equal) {
				t.Fatalf("/%s/ on %.60q: %v, want %v", re.String(), Str(s), got[k], want)
			}
		}
	}
	mu.Release(&st)
}

package jsre

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Measured with Node 24 (RegExp, matchAll): source, flags, text and every match's capture offsets in code units.
const jsTruth = `[{"src":"(?<![\\w.@]|(?<!/)/)\\d+","flags":"g","text":"a1 /2 //3 .4 5 x/6","ms":[[8,9],[13,14]]},{"src":"a[^.]{0,5}?b","flags":"g","text":"axxbxb a.b ab","ms":[[0,4],[11,13]]},{"src":"^(['\"]).*\\1$","flags":"","text":"\"quoted\"","ms":[[0,8,0,1]]},{"src":"^(['\"]).*\\1$","flags":"","text":"\"mixed'","ms":[]},{"src":"(?:a?)*b","flags":"g","text":"aab b","ms":[[0,3],[4,5]]},{"src":"\\b\u00e9|\\bx\\b","flags":"g","text":"\u00e9 x\u00e9 x","ms":[[2,3],[3,4],[5,6]]},{"src":"[a-z]+","flags":"gi","text":"\u00c9coleABC \u00e9b","ms":[[1,8],[10,11]]},{"src":"\u00e9+","flags":"gi","text":"\u00c9\u00e9E","ms":[[0,2]]},{"src":"\\s+","flags":"g","text":"a\u00a0b\ufeffc d\u2028e","ms":[[1,2],[3,4],[5,6],[7,8]]},{"src":".+","flags":"g","text":"ab\u2028cd\ne","ms":[[0,2],[3,5],[6,7]]},{"src":"a{|x{2,}|y{,2}","flags":"g","text":"a{ xxx y{,2}","ms":[[0,2],[3,6],[7,12]]},{"src":"(?:ab|a)(?:bc)","flags":"","text":"abc","ms":[[0,3]]},{"src":"(a)|(b)","flags":"g","text":"ab","ms":[[0,1,0,1,-1,-1],[1,2,-1,-1,1,2]]},{"src":"(?:(a)|b)+","flags":"","text":"ab","ms":[[0,2,-1,-1]]},{"src":"x(?=y)|x(?!z)","flags":"g","text":"xy xz x","ms":[[0,1],[6,7]]},{"src":"(?<=\\$)\\d+(?<!5)","flags":"g","text":"$12 $5 $65 4","ms":[[1,3],[8,9]]},{"src":"[\\w-]+|[^\\s\\w]","flags":"g","text":"a-b c! \u00e9","ms":[[0,3],[4,5],[5,6],[7,8]]},{"src":"\\u0041\\x42\\cJ\\0","flags":"","text":"AB\n\u0000","ms":[[0,4]]},{"src":"[\\b]","flags":"","text":"a\bb","ms":[[1,2]]},{"src":"^\\s*$","flags":"","text":" \t","ms":[[0,2]]},{"src":"\u00df|\u017f","flags":"gi","text":"SS \u00df S s \u017f","ms":[[3,4],[9,10]]},{"src":"k","flags":"gi","text":"K \u212a k","ms":[[0,1],[4,5]]}]`

func TestAgainstJS(t *testing.T) {
	var cases []struct {
		Src, Flags, Text string
		Ms               [][]int
	}
	if err := json.Unmarshal([]byte(jsTruth), &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		re, err := Compile(c.Src, c.Flags)
		if err != nil {
			t.Errorf("/%s/: %v", c.Src, err)
			continue
		}
		got := [][]int{}
		for _, m := range re.FindAll(U16(c.Text)) {
			got = append(got, append([]int(nil), m...))
		}
		want := c.Ms
		if want == nil {
			want = [][]int{}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("/%s/%s on %q: got %v, want %v", c.Src, c.Flags, c.Text, got, want)
		}
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, src := range []string{"*a", "a)", "(a", "[a", "a{2,1}", "[z-a]", "(?<n>a)", "\\1x"} {
		if _, err := Compile(src, ""); err == nil && src != "\\1x" {
			t.Errorf("/%s/ compiled", src)
		}
	}
	if _, err := Compile("a", "m"); err == nil {
		t.Error("flag m accepted")
	}
}

func TestReplaceAll(t *testing.T) {
	re := MustCompile("[aeiou]", "gi")
	out := Str(re.ReplaceAll(U16("hEllo wörld"), func(m []int) []uint16 { return U16("*") }))
	if out != "h*ll* wörld" {
		t.Fatalf("got %q", out)
	}
	if got := len(MustCompile("x*", "g").FindAll(U16("ab"))); got != 3 {
		t.Fatalf("empty matches: %d", got)
	}
}

func TestLongRunDoesNotRecurse(t *testing.T) {
	s := make([]uint16, 2_000_000)
	for i := range s {
		s[i] = 'a'
	}
	if m := MustCompile("^.+$", "").Exec(s, 0); m == nil || m[1] != len(s) {
		t.Fatal("no match")
	}
}

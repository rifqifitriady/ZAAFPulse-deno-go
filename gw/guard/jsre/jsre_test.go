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
	if _, err := Compile("a", "s"); err == nil { // m is supported since the token saver (TestMultilineAgainstJS)
		t.Error("flag s accepted")
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

// The m flag (token saver, spec D4): measured with Node 24.16, matchAll with g added; one entry per match, group 0.
const jsTruthM = `[{"src":"^a","flags":"gm","text":"a\nb\na\rb\u2028a\u2029a","ms":[[0,1],[4,5],[8,9],[10,11]]},{"src":"b$","flags":"gm","text":"ab\nb\rx\u2028b","ms":[[1,2],[3,4],[7,8]]},{"src":"^diff --git ","flags":"m","text":"x\ndiff --git a","ms":[[2,13]]},{"src":"^commit [0-9a-f]{7,40}$","flags":"m","text":"commit 1234567\ncommit 12345678z","ms":[[0,14]]},{"src":"^\\s*\\d+[\\t|→]","flags":"gm","text":"  1\tx\n\n  2|y\n3→z","ms":[[0,4],[6,11],[13,15]]},{"src":"^(?:[\\w.@-]*\\/)*[\\w.@-]+\\.\\w{1,10}:\\d+[:-]","flags":"gm","text":"a/b.js:1:x\nc.ts:22-y\n10:00:05 z","ms":[[0,9],[11,19]]},{"src":"^$","flags":"gm","text":"a\n\nb\n","ms":[[2,2],[5,5]]},{"src":"^","flags":"gm","text":"ab\ncd","ms":[[0,0],[3,3]]},{"src":"$","flags":"gm","text":"ab\ncd","ms":[[2,2],[5,5]]},{"src":"^x","flags":"g","text":"y\nx","ms":[]},{"src":"^.*\\s(?=\\S+$)","flags":"","text":"-rw-r--r-- 1 a b 12 src/x y.js","ms":[[0,26]]}]`

func TestMultilineAgainstJS(t *testing.T) {
	var cases []struct {
		Src, Flags, Text string
		Ms               [][]int
	}
	if err := json.Unmarshal([]byte(jsTruthM), &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		re, err := Compile(c.Src, c.Flags)
		if err != nil {
			t.Fatalf("/%s/%s: %v", c.Src, c.Flags, err)
		}
		got := [][]int{}
		for _, m := range re.FindAll(U16(c.Text)) {
			got = append(got, []int{m[0], m[1]})
		}
		want := c.Ms
		if want == nil {
			want = [][]int{}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("/%s/%s on %q: got %v, want %v", c.Src, c.Flags, c.Text, got, want)
		}
	}
	// Without m, ^ is anchored at 0 only (the prefilter may skip every other start); with m it is not.
	if MustCompile(`^x`, "").Test(U16("y\nx")) || !MustCompile(`^x`, "m").Test(U16("y\nx")) {
		t.Fatal("anchoring under m")
	}
	if _, err := Compile("a", "s"); err == nil {
		t.Fatal("the s flag is still refused")
	}
}

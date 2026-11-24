package engine

// The token saver (lib/gateway/rtk.js) on the go engine (the rtk-go design): long
// tool results are shortened by a filter picked from the tool call or from the output's shape, byte for byte as the
// JS does. Text is UTF-16 code units throughout (D3); the patterns are rtk.js's own sources run on jsre (D4). A nil
// *jsre.Budget means no limit; with one, a run past it panics with a jsre limit error that Budget.Run returns.

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
)

const (
	rtkMinChars = 600
	rtkMaxChars = 10 * 1024 * 1024
	saverJSON   = "token saver: a JSON tool result the engine cannot hold"
)

// rtkHit is one shortened text: the filter that did it and the UTF-16 units it saved.
type rtkHit struct {
	Filter string
	Saved  int
}

func rx(src, flags string) *jsre.Regexp { return jsre.MustCompile(src, flags) }

var (
	rtkANSI       = rx(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(\x07|\x1b\\)`, "g")
	rtkNoiseDirs  = rx(`(^|\/)(node_modules|\.git|dist|build|\.next|\.venv|venv|__pycache__|\.cache|target|coverage)(\/|$)`, "")
	rtkHexID      = rx(`\b[0-9a-f]{7,}\b`, "gi")
	rtkDigits     = rx(`\d+`, "g")
	rtkDiffHead   = rx(`^(diff --git|index |--- |\+\+\+ )`, "")
	rtkLogHead    = rx(`^(commit |Author:|Date:|Merge:)`, "")
	rtkLogMsg     = rx(`^ {4}\S`, "")
	rtkIndent4    = rx(`^ {4}`, "")
	rtkStatusFile = rx(`^\s+(modified|new file|deleted|renamed):|^\s{1,2}[MADRCU?!]{1,2}\s`, "")
	rtkTabFile    = rx(`^\t\S`, "")
	rtkBuildErr   = rx(`\b(error|ERR!|fail(ed|ure)?|✖|✗|panic|exception|traceback|not ok|assert)`, "i")
	rtkBuildWarn  = rx(`\bwarn(ing)?\b`, "i")
	rtkProgress   = rx(`^\s*(Compiling|Downloading|Downloaded|Fetching|Installing|Resolving|Collecting|Building|Progress|\[\d+\/\d+\]|\d+%)`, "i")
	rtkGrepLine   = rx(`^((?:[\w.@-]*\/)*[\w.@-]+\.\w{1,10}):(\d+)[:-]`, "")
	rtkListPrefix = rx(`^.*\s(?=\S+$)`, "")

	hintGitDiff   = rx(`\bgit\b[^|;&]*\bdiff\b|\bgit\b[^|;&]*\bshow\b`, "")
	headGitDiff   = rx(`^diff --git `, "m")
	hintGitLog    = rx(`\bgit\b[^|;&]*\blog\b`, "")
	headGitLog    = rx(`^commit [0-9a-f]{7,40}$`, "m")
	hintGitStatus = rx(`\bgit\b[^|;&]*\bstatus\b`, "")
	headGitStatus = rx(`^On branch |^Changes (not staged|to be committed)`, "m")
	hintBuild     = rx(`\b(npm|pnpm|yarn|bun|pip|poetry|cargo|go (build|test)|mvn|gradle|make|tsc|jest|vitest|pytest|node --test|xcodebuild|flutter)\b`, "")
	headBuild     = rx(`^(npm (ERR|WARN)|> .+@\d|added \d+ packages|\s*Compiling |PASS |FAIL |ℹ tests|={3,} test session|Tests? (passed|failed))`, "m")
	hintGrep      = rx(`\b(grep|rg|ag)\b`, "")
	hintGrepName  = rx(`^grep$`, "i")
	headGrepLines = rx(`^(?:[\w.@-]*\/)*[\w.@-]+\.\w{1,10}:\d+[:-]`, "gm")
	headJSON      = rx(`^\s*[[{]`, "")
	tailJSON      = rx(`[\]}]\s*$`, "")
	hintListing   = rx(`\b(find|ls|tree|fd|glob)\b`, "")
	hintGlobName  = rx(`^glob$`, "i")
	hintRead      = rx(`^(read|cat)\b`, "i")
	headReadLines = rx(`^\s*\d+[\t|→]`, "gm")
)

var rtkFilters = map[string]func([]uint16, *jsre.Budget) []uint16{
	"git-diff": filterGitDiff, "git-log": filterGitLog, "git-status": filterGitStatus, "build": filterBuild,
	"grep": filterGrep, "listing": filterListing, "read": filterRead, "log": filterLog,
}

func omitted(n int, what string) []uint16 {
	return cat(u16("… "), itoa16(n), u16(" "+what+" omitted"))
}

func omittedIn(n int, what []uint16) []uint16 {
	return cat(u16("  … "), itoa16(n), u16(" "), what, u16(" omitted"))
}

func headTail(ls [][]uint16, head, tail int) [][]uint16 {
	if len(ls) <= head+tail+1 {
		return ls
	}
	out := make([][]uint16, 0, head+tail+1)
	out = append(out, ls[:head]...)
	out = append(out, omitted(len(ls)-head-tail, "lines"))
	return append(out, ls[len(ls)-tail:]...)
}

// dedupe: runs of lines alike once digits and hex ids are ignored collapse to one, then a blank line after a blank
// line goes (also a leading one: rtk.js reads a[-1] as the empty string).
func dedupe(ls [][]uint16, b *jsre.Budget) [][]uint16 {
	hash, zero := u16("#"), u16("0")
	toHash := func([]int) []uint16 { return hash }
	toZero := func([]int) []uint16 { return zero }
	// The keys are built in two scratch buffers reused for every line (prev keeps its own copy): a 10 MiB log would
	// otherwise allocate two keys per line (spec D19).
	var bufA, bufB []uint16
	keep := func(buf *[]uint16, k, src []uint16) []uint16 {
		if len(k) > 0 && (len(src) == 0 || &k[0] != &src[0]) {
			*buf = k[:0] // a replacement wrote a new buffer: reuse its storage next time
		}
		return k
	}
	key := func(l []uint16) []uint16 {
		k := keep(&bufA, rtkHexID.ReplaceAllInto(bufA, l, toHash, b), l)
		return trim16(keep(&bufB, rtkDigits.ReplaceAllInto(bufB, k, toZero, b), k))
	}
	var out [][]uint16
	var prev []uint16
	have, run := false, 0
	flush := func() {
		if run > 1 {
			out = append(out, cat(u16("  (× "), itoa16(run), u16(" similar lines)")))
		}
	}
	for _, l := range ls {
		b.Charge(1)
		k := key(l)
		if have && len(k) > 0 && equal16(k, prev) {
			run++
			continue
		}
		flush()
		out = append(out, l)
		prev, have, run = append(prev[:0], k...), true, 1
	}
	flush()
	res := make([][]uint16, 0, len(out))
	for i, l := range out {
		if blank(l) && (i == 0 || blank(out[i-1])) {
			continue
		}
		res = append(res, l)
	}
	return res
}

func filterGitDiff(t []uint16, b *jsre.Budget) []uint16 {
	var out [][]uint16
	hunk := 0
	for _, l := range splitNL(t) {
		b.Charge(1)
		if rtkDiffHead.TestB(l, b) || hasPrefix16(l, "@@") {
			out = append(out, l)
			hunk = 0
			continue
		}
		hunk++
		if hunk <= 60 {
			out = append(out, l)
		} else if hunk == 61 {
			out = append(out, u16("  … rest of hunk omitted"))
		}
	}
	return joinNL(headTail(out, 300, 100))
}

func filterGitLog(t []uint16, b *jsre.Budget) []uint16 {
	var kept [][]uint16
	for _, l := range splitNL(t) {
		b.Charge(1)
		if rtkLogHead.TestB(l, b) || rtkLogMsg.TestB(l, b) {
			kept = append(kept, l)
		}
	}
	var out [][]uint16
	for i, l := range kept {
		if !rtkIndent4.TestB(l, b) || i == 0 || !rtkIndent4.TestB(kept[i-1], b) {
			out = append(out, l)
		}
	}
	return joinNL(headTail(out, 150, 20))
}

func filterGitStatus(t []uint16, b *jsre.Budget) []uint16 {
	var out [][]uint16
	n := 0
	for _, l := range splitNL(t) {
		b.Charge(1)
		if rtkStatusFile.TestB(l, b) || rtkTabFile.TestB(l, b) {
			n++
			if n <= 25 {
				out = append(out, l)
			} else if n == 26 {
				out = append(out, u16("… more files omitted"))
			}
		} else {
			n = 0
			out = append(out, l)
		}
	}
	return joinNL(out)
}

func filterBuild(t []uint16, b *jsre.Budget) []uint16 {
	ls := splitNL(t)
	keep := make([]bool, len(ls))
	warn, progress := 0, 0
	for i, l := range ls {
		b.Charge(1)
		switch {
		case rtkBuildErr.TestB(l, b):
			for k := i; k <= i+3 && k < len(ls); k++ {
				keep[k] = true
			}
		case rtkBuildWarn.TestB(l, b) && func() bool { w := warn; warn++; return w < 5 }():
			keep[i] = true
		case rtkProgress.TestB(l, b): // a warning past the fifth that looks like progress is counted (spec D18)
			progress++
		}
	}
	var body [][]uint16
	for i, l := range ls {
		if keep[i] {
			body = append(body, l)
		}
	}
	var out [][]uint16
	if progress > 0 {
		out = append(out, cat(u16("("), itoa16(progress), u16(" progress lines)")))
	}
	d := dedupe(body, b)
	out = append(out, d[:min(len(d), 300)]...)
	if len(body) > 0 {
		out = append(out, u16("…"))
	}
	out = append(out, ls[max(0, len(ls)-25):]...)
	return joinNL(out)
}

type group struct {
	name  []uint16
	lines [][]uint16 // the first `keep` lines: the filters print no more of a group (spec D19)
	n     int        // every line of the group
}

// groups keeps insertion order, as a JS Map does. keep is how many lines of a group the filter prints.
type groups struct {
	order []*group
	index map[string]*group
	keep  int
}

func (g *groups) add(name, line []uint16) {
	if g.index == nil {
		g.index = map[string]*group{}
	}
	k := key16(name)
	x := g.index[k]
	if x == nil {
		x = &group{name: name}
		g.index[k] = x
		g.order = append(g.order, x)
	}
	if x.n < g.keep {
		x.lines = append(x.lines, line)
	}
	x.n++
}

func filterGrep(t []uint16, b *jsre.Budget) []uint16 {
	by := groups{keep: 10}
	var other [][]uint16 // the first 20: the filter prints no more
	eachLine(t, func(l []uint16) {
		b.Charge(1)
		matched := false
		rtkGrepLine.EachB(l, b, func(m []int) { // ^ without m: at most one match, at 0
			if !matched {
				matched = true
				by.add(l[m[2]:m[3]], l)
			}
		})
		if !matched && !blank(l) && len(other) < 20 {
			other = append(other, l)
		}
	})
	var out [][]uint16
	for i, g := range by.order {
		if i+1 > 40 {
			out = append(out, omitted(len(by.order)-40, "more files"))
			break
		}
		out = append(out, g.lines...)
		if g.n > 10 {
			out = append(out, omittedIn(g.n-10, cat(u16("more matches in "), g.name)))
		}
	}
	out = append(out, other...)
	return joinNL(out)
}

func filterListing(t []uint16, b *jsre.Budget) []uint16 {
	by := groups{keep: 12}
	eachLine(t, func(l []uint16) {
		b.Charge(1)
		p := trim16(l)
		if len(p) == 0 || rtkNoiseDirs.TestB(p, b) {
			return
		}
		if m := rtkListPrefix.ExecB(p, 0, b); m != nil {
			p = cat(p[:m[0]], p[m[1]:])
		}
		dir := u16(".")
		if i := lastIndex16(p, '/'); i >= 0 {
			dir = p[:i]
		}
		by.add(dir, l)
	})
	var out [][]uint16
	for i, g := range by.order {
		if i+1 > 40 {
			out = append(out, omitted(len(by.order)-40, "more folders"))
			break
		}
		out = append(out, g.lines...)
		if g.n > 12 {
			out = append(out, omittedIn(g.n-12, cat(u16("more in "), g.name)))
		}
	}
	return joinNL(out)
}

func filterRead(t []uint16, _ *jsre.Budget) []uint16 { return joinNL(headTail(splitNL(t), 160, 60)) }

func filterLog(t []uint16, b *jsre.Budget) []uint16 {
	return joinNL(headTail(dedupe(splitNL(t), b), 200, 80))
}

// jsonParse is JSON.parse as the saver needs it: the tree; or syntax (JSON.parse throws); or unsupported (JSON.parse
// accepts it but engine.Parse cannot hold it: an escaped lone surrogate, nesting over MaxDepth). engine.Parse stops at
// the first such thing, which may come before a syntax error JS throws on (review m1, m2), so an unsupported text is
// checked with encoding/json's validator. Valid holds lone surrogate escapes and caps nesting at 10000; with more
// brackets than that the answer is unknown and the text stays unsupported (the caller hands off: exact either way).
func jsonParse(text []byte) (v *Value, syntax, unsupported bool) {
	v, err := Parse(text)
	switch {
	case err == nil:
		return v, false, false
	case !errors.Is(err, ErrUnsupported):
		return nil, true, false
	case json.Valid(text):
		return nil, false, true
	case bytes.Count(text, []byte("["))+bytes.Count(text, []byte("{")) <= 10000:
		return nil, true, false // not JSON: JSON.parse throws too
	}
	return nil, false, true
}

// pickFilter is rtk.js pickFilter. For json it also returns the parsed tree (rtkJSON writes from it). why is a hand-off
// reason: JSON.parse accepts the text but the engine cannot hold it (spec D6).
func pickFilter(text, hint []uint16, b *jsre.Budget) (string, *Value, string) {
	h := hintLower(hint)
	head := text[:min(len(text), 2048)]
	switch {
	case hintGitDiff.TestB(h, b) || headGitDiff.TestB(head, b):
		return "git-diff", nil, ""
	case hintGitLog.TestB(h, b) || headGitLog.TestB(head, b):
		return "git-log", nil, ""
	case hintGitStatus.TestB(h, b) || headGitStatus.TestB(head, b):
		return "git-status", nil, ""
	case hintBuild.TestB(h, b) || headBuild.TestB(head, b):
		return "build", nil, ""
	case hintGrep.TestB(h, b) || hintGrepName.TestB(hint, b) || len(headGrepLines.FindAllB(head, b)) >= 3:
		return "grep", nil, ""
	}
	if headJSON.TestB(head, b) && tailJSON.TestB(text[max(0, len(text)-64):], b) {
		b.Charge(len(text))
		v, syntax, unsupported := jsonParse([]byte(jsre.Str(text)))
		switch {
		case unsupported:
			return "", nil, saverJSON
		case !syntax:
			return "json", v, ""
		} // a syntax error: JSON.parse throws, not json
	}
	switch {
	case hintListing.TestB(h, b) || hintGlobName.TestB(hint, b):
		return "listing", nil, ""
	case hintRead.TestB(hint, b) || len(headReadLines.FindAllB(head, b)) >= 10:
		return "read", nil, ""
	case len(text) > 4000:
		return "log", nil, ""
	}
	return "", nil, ""
}

// compressText is rtk.js compressText on one text. The JS try/catch around a filter has no counterpart: no Go filter
// fails on a text it is given (spec D2).
func compressText(raw, hint []uint16, b *jsre.Budget) ([]uint16, *rtkHit, string) {
	if len(raw) < rtkMinChars || len(raw) > rtkMaxChars || hasPrefix16(raw, "[error]") {
		return nil, nil, ""
	}
	clean := rtkANSI.ReplaceAllB(raw, func([]int) []uint16 { return nil }, b)
	name, parsed, why := pickFilter(clean, hint, b)
	if why != "" {
		return nil, nil, why
	}
	text := clean
	switch {
	case name == "json":
		text = rtkJSON(parsed)
	case name != "":
		text = rtkFilters[name](clean, b)
	}
	if len(text) >= len(raw) || blank(text) {
		if len(clean) < len(raw) {
			return clean, &rtkHit{"ansi", len(raw) - len(clean)}, ""
		}
		return nil, nil, ""
	}
	if name == "" {
		name = "ansi"
	}
	return text, &rtkHit{name, len(raw) - len(text)}, ""
}

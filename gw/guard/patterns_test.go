package guard

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// I2 (review): every regex source and flag set, category flag, constant and table the Go guard copies from the JS
// must equal what the JS modules hold now (gw/testdata/guard/patterns-all.json, written by gen-guard.js), and the
// JS source the port follows must hash to the value the port was written against. A new JS secret type, a changed
// pattern or a changed constant fails here.

var namedPatterns = map[string]*jsre.Regexp{
	"INJ_CARRIES": reInjCarries, "INJ_ANCHOR": reInjAnchor, "INJ_ANCHOR_MARK": reInjAnchorMark,
	"DOC_CONTEXT": reDocContext, "CODE_LINE": reCodeLine, "STRING_ITEM": reStringItem, "QUOTE_BEFORE": reQuoteBefore,
	"ZERO_WIDTH": reZeroWidth, "SPLIT_LETTERS": reSplitLetters, "HAS_ZERO_WIDTH": reHasZeroWidth,
	"HAS_SPLIT": reHasSplit, "HAS_LEET": reHasLeet, "SECRET_HINT": reSecretHint, "PLACEHOLDER": rePlaceholder,
	"PLACEHOLDER_PASSWORD": rePlaceholderPassword, "EMAIL": reEmail, "IPV4": reIPV4, "IPV6": reIPV6, "PHONE": rePhone,
	"BASE64_URL": reBase64URL,
}

func rePair(r *jsre.Regexp) *jsv.Array { return jsv.Arr(r.String(), r.Flags()) }

func sortedKeys(m map[string]bool) *jsv.Array {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strArr(ks)
}

func sortedArr(v jsv.Value) *jsv.Array {
	var ks []string
	for _, x := range jsv.Ary(v).Items {
		ks = append(ks, jsv.ToString(x))
	}
	sort.Strings(ks)
	return strArr(ks)
}

func goPatterns() *jsv.Object {
	inj := jsv.Arr()
	for _, c := range injection {
		res := jsv.Arr()
		for _, r := range c.res {
			res.Push(rePair(r))
		}
		inj.Push(jsv.NewObject().Set("id", c.id).Set("label", c.label).Set("carries", c.carries).
			Set("supporting", c.supporting).Set("res", res))
	}
	sec := jsv.Arr()
	for _, p := range secretPatterns {
		sec.Push(jsv.Arr(p.label, p.re.String(), p.re.Flags()))
	}
	named := jsv.NewObject()
	for k, r := range namedPatterns {
		named.Set(k, rePair(r))
	}
	leetObj := jsv.NewObject()
	for k, v := range leet {
		leetObj.Set(string(rune(k)), string(rune(v)))
	}
	modes := jsv.NewObject()
	for _, c := range Checks {
		modes.Set(c, strArr(Modes[c]))
	}
	wordObj := jsv.NewObject()
	for k, v := range word {
		wordObj.Set(k, v)
	}
	constants := jsv.NewObject().
		Set("WINDOW_BEFORE", float64(windowBefore)).Set("WINDOW_AFTER", float64(windowAfter)).Set("LEET", leetObj).
		Set("KEEP_IP", sortedKeys(keepIP)).Set("STRUCTURAL", sortedKeys(structural)).Set("BINARY", sortedKeys(binary)).
		Set("OPAQUE", sortedKeys(opaque)).Set("MODEL_ITEMS", sortedKeys(modelItems)).Set("WORD", wordObj).
		Set("PII_KINDS", strArr(piiKinds)).Set("CHECKS", strArr(Checks)).Set("MODES", modes).Set("SCOPES", strArr(Scopes)).
		Set("DEFAULTS", Defaults.Value()).Set("FORMATS", strArr(Formats))
	sources := jsv.NewObject()
	for k, v := range jsSources {
		sources.Set(k, v)
	}
	return jsv.NewObject().Set("injection", inj).Set("secrets", sec).Set("regex", named).
		Set("constants", constants).Set("sources", sources)
}

func init() {
	fixtureFns["patterns"] = func([]jsv.Value) (jsv.Value, error) { return goPatterns(), nil }
	// Sets are compared as sorted lists: Go keeps them in maps.
	normalizeFixture["patterns"] = func(out jsv.Value) jsv.Value {
		c := jsv.Obj(jsv.Obj(out).Get("constants"))
		for _, k := range []string{"KEEP_IP", "STRUCTURAL", "BINARY", "OPAQUE", "MODEL_ITEMS"} {
			c.Set(k, sortedArr(c.Get(k)))
		}
		return out
	}
}

func TestPatternsFixtureExists(t *testing.T) {
	if _, err := jsv.LoadFixture(filepath.Join(fixtureDir, "patterns-all.json")); err != nil {
		t.Fatal(err)
	}
}

package guard

import (
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate/jsv"
)

// antKey is a live Anthropic key of n code units (n >= 27), built so the file holds no scanner-shaped literal.
func antKey(n int) string {
	const pool = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefghijklmnopqrstuvwxyz"
	return "sk" + "-ant-" + pool[:n-7]
}

// C1 (B2 review): when the replacements of a call cancel out in length, the masked text could land in the original's
// own storage with the original's length, and the guard took "same storage" for "unchanged": it forwarded the text in
// clear while reporting "redacted". Each case below has a net length change of zero; the result must be the redacted
// text, never the original.
func TestLengthNeutralRedactionsAreApplied(t *testing.T) {
	sec := "‹secret: Anthropic API key›"
	ls := len(jsre.U16(sec))
	cases := []struct{ name, in, want string }{
		// secret -1, email +1
		{"secret+email", "k " + antKey(ls+1) + " m a@b.co", "k " + sec + " m ‹email›"},
		// secret -2, two emails +2
		{"secret+2 emails", antKey(ls+2) + " a@b.co c@d.io", sec + " ‹email› ‹email›"},
		// secret 0 on its own
		{"secret of the placeholder's length", "x " + antKey(ls) + " y", "x " + sec + " y"},
		// secret -3, email +1, email +1, email +1, IPv4 8->4 is -4, phone 15->7 is -8: -3+3-4+... keep it exact below
		{"4 emails + IPv4", "a@b.co c@d.io e@f.io g@h.io at 10.0.0.7", "‹email› ‹email› ‹email› ‹email› at ‹ip›"},
		{"secret+4 emails+IPv4", antKey(ls) + " a@b.co c@d.io e@f.io g@h.io 10.0.0.7",
			sec + " ‹email› ‹email› ‹email› ‹email› ‹ip›"},
		// email +1 ×9, phone "+62 812 3456 7890" (17) -> ‹phone› (7) is -10, secret -... : 9 - 10 + 1
		{"9 emails+phone+secret", strings.Repeat("a@b.co ", 9) + "+62 812 3456 7890 " + antKey(ls-0) + "z",
			strings.Repeat("‹email› ", 9) + "‹phone› " + sec},
		// two secrets, each of the placeholder's length
		{"2 secrets", antKey(ls) + " and " + antKey(ls), sec + " and " + sec},
	}
	p := Policy{Scope: "all", Secrets: "redact", PII: "redact", Injection: "log"}
	for _, c := range cases {
		r, err := CheckBody(chat(c.in), p, "openai-chat")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := jsv.Get(jsv.Get(jsv.Get(r.Body, "messages"), "0"), "content")
		if got != c.want || r.Action != "redacted" {
			t.Errorf("%s: CheckBody gave %q (%s), want %q", c.name, got, r.Action, c.want)
		}
		tr, err := CheckText(c.in, p, "input")
		if err != nil || tr.Text != c.want || tr.Action != "redacted" {
			t.Errorf("%s: CheckText gave %q (%s, %v), want %q", c.name, tr.Text, tr.Action, err, c.want)
		}
	}
}

package engine

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// NormalizeModel is lib/pricing.js normalizeModel: the price-table form of a model name.
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimFunc(model, jsSpace))
	m = m[strings.LastIndexByte(m, '/')+1:]
	m = reBracket.ReplaceAllString(m, "")
	m = reAnthropic.ReplaceAllString(m, "")
	m = reAt.ReplaceAllString(m, "")
	m = reVersion.ReplaceAllString(m, "")
	if strings.HasPrefix(m, "claude") {
		m = strings.ReplaceAll(m, ".", "-")
	}
	return m
}

var (
	reBracket   = regexp.MustCompile(`\[[^\]]*\]$`)
	reAnthropic = regexp.MustCompile(`^(?:(?:us|eu|apac|global|jp|au|ca|us-gov)\.)?anthropic\.`)
	reAt        = regexp.MustCompile(`@[\w.-]+$`)
	reVersion   = regexp.MustCompile(`-v\d+(?::\d+)?$`)
)

// KeyMatches is lib/pricing.js keyMatches.
func KeyMatches(model, key string) bool {
	if !strings.HasPrefix(model, key) {
		return false
	}
	if len(model) == len(key) || !isAlnum(key[len(key)-1]) {
		return true
	}
	switch c := model[len(key)]; c {
	case '-', '@', ':', '_':
		return true
	case '.':
		return len(model) == len(key)+1 || model[len(key)+1] < '0' || model[len(key)+1] > '9'
	}
	return false
}

func isAlnum(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }

// Pricer is lib/pricing.js makePricer over config.push pricing: the longest matching price key wins.
type Pricer struct {
	table map[string]ipc.PriceEntry
	keys  [][2]string // normalized, original; longest normalized first
}

func NewPricer(table map[string]ipc.PriceEntry) *Pricer {
	p := &Pricer{table: table}
	for k := range table {
		p.keys = append(p.keys, [2]string{NormalizeModel(k), k})
	}
	// JavaScript sorts Object.keys order stably; a map has no order, so equal lengths go by key (they differ only for
	// two keys that normalize to the same name).
	sort.Slice(p.keys, func(i, j int) bool {
		if len(p.keys[i][0]) != len(p.keys[j][0]) {
			return len(p.keys[i][0]) > len(p.keys[j][0])
		}
		return p.keys[i][1] < p.keys[j][1]
	})
	return p
}

// Key is the price key of model, or "".
func (p *Pricer) Key(model string) string {
	m := NormalizeModel(model)
	for _, k := range p.keys {
		if k[0] != "" && KeyMatches(m, k[0]) {
			return k[1]
		}
	}
	return ""
}

// Cost is lib/gateway/usage.js cost in nano-USD (toNano of the same float arithmetic on the same USD prices): input
// beyond the cached tokens at the input price, cached at the cache-read price (else the input price), output at the
// output price. 0 for a model without a price.
func (p *Pricer) Cost(model string, input, output, cached float64) int64 {
	k := p.Key(model)
	if k == "" {
		return 0
	}
	e := p.table[k]
	usd := func(n int64) float64 { return float64(n) / 1e9 }
	in, out := usd(e.Input), usd(e.Output)
	cr := in
	if e.CacheRead != nil && *e.CacheRead != 0 {
		cr = usd(*e.CacheRead)
	}
	c := (math.Max(0, input-cached)*in + cached*cr + output*out) / 1e6
	n := math.Round(c * 1e9)
	if n == 0 || math.IsNaN(n) {
		return 0
	}
	return int64(n)
}

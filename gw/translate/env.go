// Package translate is the Go port of lib/gateway/translate.js (P4d): format translation between the Anthropic
// Messages, OpenAI Responses, Gemini and OpenAI Chat Completions shapes, whole and streamed. OpenAI chat is the pivot.
// Values are jsv values so key order, undefined and JS string conversions match the JS byte for byte; the fixtures in
// gw/testdata/translate (scripts/fixtures/gen-translate.js) prove it.
package translate

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Env holds the clock and the random source; Default uses the real ones. Tests use fixed ones (the generator's).
type Env struct {
	Now    func() int64 // Unix milliseconds (Date.now())
	Random func(n int) []byte
}

// Default is the real clock and crypto/rand.
var Default = &Env{
	Now: func() int64 { return time.Now().UnixMilli() },
	Random: func(n int) []byte {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return b
	},
}

// rid is `${p}${crypto.randomBytes(12).toString('hex')}`.
func (e *Env) rid(p string) string { return p + hex.EncodeToString(e.Random(12)) }

// seconds is Math.floor(Date.now() / 1000).
func (e *Env) seconds() float64 {
	ms := e.Now()
	s := ms / 1000
	if ms%1000 < 0 {
		s--
	}
	return float64(s)
}

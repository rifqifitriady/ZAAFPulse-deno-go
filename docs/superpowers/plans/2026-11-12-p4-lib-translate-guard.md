# P4 stage 1, stream B: translate and guard libraries in Go — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pure Go ports of `lib/gateway/translate.js` (P4d) and `lib/gateway/guardrails.js` with the parts of `lib/signals.js` and `lib/evals/pii.js` it calls (P4e), proven equal to the JS by fixtures the JS code generates, with a CI step that regenerates the fixtures and fails on a diff.

**Architecture:** Two new packages, `gw/translate` and `gw/guard`, each with a support package: `gw/translate/jsv` (JSON values that keep key order and follow JS semantics: truthiness, `String()`, `Number()`, `JSON.stringify`, property access that throws a `TypeError` like V8) and `gw/guard/jsre` (a backtracking regular-expression engine with JS semantics on UTF-16 code units, because the detectors use lookbehind, lookahead, lazy quantifiers and backreferences that Go's RE2 `regexp` cannot run). Two generator scripts, `scripts/fixtures/gen-translate.js` and `scripts/fixtures/gen-guard.js`, run the real JS functions over a fixed corpus and write one `gw/testdata/<lib>/<case>.json` per case; the Go tests replay every file.

**Tech Stack:** Go 1.27.3, standard library only (`encoding/json` token decoder, `strconv`, `unicode`, `unicode/utf16`, `bufio`, `io`). Node 24 and Deno 2.9.8 for the generators (CommonJS, `node:` built-ins only). GitHub Actions (`.github/workflows/ci.yml`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-12-p4-stage1-design.md`, sections "Equivalence by generated fixtures", "Stream B interfaces" and "Rules for all three streams" (binding). Where the code contradicts the spec, this plan follows the code and says so under "Spec deviations".
- Go is **standard library only**; module `gw/` (`go 1.27`), `GOTOOLCHAIN=local`. The packages are pure: no I/O except `ReadSSE` reading the `io.Reader` it is given, no clock and no randomness except through `translate.Env`.
- Scope (spec table, stream B): `gw/translate/`, `gw/guard/` (new), `scripts/fixtures/`, `gw/testdata/`. Nothing imports `gw/engine/`. The CI step is the one change outside that list (deviation 6).
- Fixture file: `{ "fn": "<name>", "in": [<args>], "out": <value> }` or `{ "fn", "in", "throws": "<message>", "status"?: 400, "hits"?: [...] }`, written by `JSON.stringify(x, null, 2) + "\n"`. Go compares `out` after canonical JSON encoding (keys sorted, JS number formatting) byte-for-byte; strings inside (SSE text, tool arguments) are compared exactly, so key order inside them must match the JS.
- Determinism in the generators: `crypto.randomBytes(n)` returns `n` bytes all equal to a per-case counter (1, 2, 3, …, reset per case, mod 256) and `Date.now()` returns `1767225600000` (2026-01-01T00:00:00Z). The Go tests use `translate.Env{Now, Random}` with the same rules.
- No secret-shaped string is committed: a value the guard generator registers as a secret is written as `<<ZPSECRET:i>>` and listed base64-encoded under `"secrets"`; the Go test substitutes it back before parsing.
- Generators must produce identical bytes on Node and on Deno: `node scripts/fixtures/gen-<lib>.js` and `deno run --allow-read --allow-write=gw/testdata scripts/fixtures/gen-<lib>.js`; each deletes the files of its folder it did not write.
- Every task ends green on: `cd gw && go vet ./... && GOOS=windows go vet ./... && go test -race ./...`, `gofmt -l gw` empty, and the generators leaving `git status --porcelain gw/testdata` empty.
- Commits: one plain English sentence, no AI attribution, never `.superpowers/`. Push `migrate/p4-lib-translate-guard` after each commit. Never touch ports 4777/4787.

## Files

| File | Responsibility |
|---|---|
| `gw/translate/jsv/value.go` | `Value`, `Undefined`, `*Object` (ordered), `*Array`, constructors, `Get`/`Index`/`Len`, `TypeError`, `Catch` |
| `gw/translate/jsv/convert.go` | `Truthy`, `ToString`, `ToNumber`, `NumberString`, `TypeOf`, `StrictEqual` |
| `gw/translate/jsv/json.go` | `Parse` (order-keeping), `Stringify` (exact `JSON.stringify`), `Canonical`, `UTF16Len` |
| `gw/translate/env.go` | `Env`, `Default`, `rid` |
| `gw/translate/request.go` | the request/response functions, stop tables, `BadRequest` |
| `gw/translate/stream.go` | `AnthropicStream`, `ResponsesStream`, `GeminiStream`, `AnthropicEvents`, `WholeToChunks`, `ReadSSE` |
| `gw/translate/fixtures_test.go` | replays `gw/testdata/translate/*.json` |
| `gw/guard/jsre/{parse,match}.go` | JS regex parser and backtracking matcher on `[]uint16` |
| `gw/guard/injection.go`, `secrets.go`, `pii.go` | `ScanInjection`, `SecretSpans`, `MaskPII` |
| `gw/guard/guard.go` | `Policy`, `CleanPolicy`, `ResolvePolicy`, `BlockMessage`, `Block`, `CheckBody`, `CheckText` |
| `gw/guard/fixtures_test.go` | replays `gw/testdata/guard/*.json` |
| `scripts/fixtures/lib.js` | shared writer: determinism hooks, case runner, secret encoding, stale-file removal |
| `scripts/fixtures/gen-translate.js`, `gen-guard.js` | the corpora |
| `.github/workflows/ci.yml` | new `fixtures` job |

## Interfaces (fixed here; every task uses these names)

```go
package jsv
type Value = any // nil (null), Undefined, bool, float64, string, *Object, *Array
type undefinedType struct{}
var Undefined undefinedType
type Object struct{ /* ordered keys */ }
func NewObject() *Object
func (o *Object) Get(k string) Value            // Undefined when absent
func (o *Object) Has(k string) bool
func (o *Object) Set(k string, v Value) *Object  // keeps the first position, like JS
func (o *Object) Delete(k string)
func (o *Object) Keys() []string
func (o *Object) Clone() *Object                 // shallow, like {...o}
type Array struct{ Items []Value }
func Arr(items ...Value) *Array
type TypeError struct{ Message string }
func Get(v Value, key string) Value // JS v[key]: panics TypeError on null/undefined
func Catch(f func()) (err error)    // turns a TypeError panic into an error
func Truthy(v Value) bool; func ToString(v Value) string; func ToNumber(v Value) float64
func Parse(b []byte) (Value, error); func Stringify(v Value) (string, bool); func Canonical(v Value) string
func UTF16Len(s string) int

package translate
type Env struct{ Now func() int64; Random func(n int) []byte } // Now: Unix ms
var Default *Env
type BadRequest struct{ Message string } // Status() == 400, Type() == "invalid_request_error"
type Tagged struct{ Body *jsv.Object; Soft, Dropped []string }
func (e *Env) AnthropicToOpenAI(b jsv.Value) (Tagged, error)
func (e *Env) OpenAIToAnthropic(j, model jsv.Value) (*jsv.Object, error)
func (e *Env) OpenAIToAnthropicRequest(b jsv.Value) (*jsv.Object, error)
func AnthropicUsageToOpenAI(u jsv.Value) *jsv.Object
func (e *Env) AnthropicResponseToOpenAI(j, model jsv.Value) (*jsv.Object, error)
func (e *Env) ResponsesToOpenAI(b jsv.Value) (Tagged, []string, error) // custom tool names in order
func (e *Env) OpenAIToResponses(j, model jsv.Value, custom []string) (*jsv.Object, error)
func (e *Env) GeminiToOpenAI(b, model jsv.Value, stream bool) (*jsv.Object, error)
func (e *Env) OpenAIToGemini(j, model jsv.Value) (*jsv.Object, error)
func EstimateTokens(body jsv.Value) int
var ToAnthropicStop, FromAnthropicStop, ToGeminiFinish map[string]string
func (e *Env) NewAnthropicStream(model jsv.Value) *AnthropicStream   // Open, Push(chunk), Finish, Ping, Error(msg)
func (e *Env) NewResponsesStream(model jsv.Value, custom []string) *ResponsesStream // Open, Push, Finish, Error
func (e *Env) NewGeminiStream(model jsv.Value) *GeminiStream          // Push, Finish, Error
func (e *Env) NewAnthropicEvents(model jsv.Value) *AnthropicEvents    // Next(ev) ([]jsv.Value, error)
func WholeToChunks(j jsv.Value) ([]jsv.Value, error)
func ReadSSE(r io.Reader, onEvent func(event, data string) error) error
// package-level wrappers call Default for each Env method.

package guard
type Policy struct{ Scope, Secrets, PII, Injection string }
type Hit struct{ Check, Label string; Count int }
type Result struct{ Body jsv.Value; Action string; Hits []Hit; Error bool } // Action "" is JS null
type TextResult struct{ Text string; Action string; Hits []Hit; Error bool }
type Block struct{ Hits []Hit } // Error() == BlockMessage(Hits)
func CleanPolicy(cfg jsv.Value) Policy
func ResolvePolicy(global, key jsv.Value) Policy
func BlockMessage(hits []Hit) string
func CheckBody(body jsv.Value, p Policy, format string) (Result, error)
func CheckText(text jsv.Value, p Policy, part string) (TextResult, error)
type Injection struct{ ID, Label string; At int; Match string; Categories, Labels []string }
func ScanInjection(text string) *Injection
type Span struct{ Start, End int; Label string; MatchStart int } // UTF-16 offsets
func SecretSpans(text string) []Span
func MaskPII(text string) (string, map[string]int) // the guardrail's call: home false, counts
```

---

### Task 1: `jsv` — JSON values with JS semantics

**Files:** Create `gw/translate/jsv/value.go`, `convert.go`, `json.go`, `jsv_test.go`.

- [ ] **Step 1: failing tests** in `jsv_test.go`, table-driven, with values measured in Node:
  - `Parse` keeps order and duplicate-key position: `{"b":1,"a":2,"b":3}` → keys `b,a`, `b`=3.
  - `Stringify` equals `JSON.stringify`: `{"a":undefined,"b":[undefined]}` → `{"b":[null]}`; `"< é\u0001"` → `"< é\u0001"` with `\u0001` escaped and the rest raw; numbers `1e21`→`1e+21`, `1e-7`→`1e-7`, `0.000001`→`0.000001`, `-0`→`0`, `123456789012345680000`→`123456789012345680000`, `0.1+0.2`→`0.30000000000000004`, NaN→`null`.
  - `ToString`: `[1,[2,null]]`→`1,2,`, object→`[object Object]`, `undefined`→`undefined`, `1e21`→`1e+21`.
  - `ToNumber`: `" 12 "`→12, `""`→0, `"0x1f"`→31, `"abc"`→NaN, `true`→1, `null`→0, `[5]`→5, `[]`→0, object→NaN.
  - `Get(Undefined,"name")` panics `TypeError{"Cannot read properties of undefined (reading 'name')"}`; `Get(nil,"x")` the same with `null`; `Get("str","x")` → Undefined; `Get(arr,"length")` → float64.
  - `Canonical` sorts keys recursively.
- [ ] **Step 2:** `go -C gw test ./translate/jsv/` → FAIL (undefined symbols).
- [ ] **Step 3: implement.** `Parse` walks `json.Decoder.Token()` with `UseNumber()`, numbers through `strconv.ParseFloat` (±Inf kept, as V8). `NumberString` follows ECMA-262 Number::toString: shortest digits from `strconv.FormatFloat(f,'e',-1,64)`, then fixed notation for exponents −7 < e < 21, otherwise `d[.ddd]e±n`. `Stringify` escapes `"` `\\` `\b\f\n\r\t`, other code units below 0x20 as `\u00xx`, nothing else. `UTF16Len` counts runes ≥ 0x10000 twice.
- [ ] **Step 4:** tests pass, `go vet`, `gofmt -l`.
- [ ] **Step 5:** commit "Add the jsv package: ordered JSON values with JavaScript semantics for the Go ports" and push.

### Task 2: fixture generator and replay harness for translate

**Files:** Create `scripts/fixtures/lib.js`, `scripts/fixtures/gen-translate.js`, `gw/testdata/translate/*.json`, `gw/translate/env.go`, `gw/translate/fixtures_test.go`.

- [ ] **Step 1: `lib.js`** exports `writeCases(dir, cases, { secrets })`: before each case it resets the random counter; `crypto.randomBytes = (n) => Buffer.alloc(n, counter++ & 255)` (counter starts at 1) and `Date.now = () => 1767225600000` are installed once at load, before the library is required. A case is `{ name, fn, args, call }`; `in` is `JSON.parse(JSON.stringify(args))` and `call` receives that copy. A thrown error becomes `throws` (`err.message`), plus `status` when set and `hits` when present. Files are named `<name>.json`; files in `dir` not written this run are deleted; the run prints `<n> fixtures in <dir>`.
- [ ] **Step 2: `gen-translate.js`** builds cases from every input in `test/gateway-compat.test.js` and `test/router9.test.js` that calls a translate function, plus edge cases: empty bodies, `null`/missing fields, string vs array content, documents, `is_error` tool results, server tools, `tool_choice` variants (`any`, `none`, `tool` naming a server tool → 400), thinking `enabled`/`adaptive`, `output_config.effort` `max`/bad, Responses `item_reference` and `previous_response_id` (400), custom tools, `json_object`, Gemini `inline_data`, `function_declarations`, `functionResponse` without id, a property access on a missing `function` (TypeError message), stream op lists (`["open"]`, `["push", chunk]`, `["finish"]`, `["ping"]`, `["error","m"]`) including the parallel-calls-at-index-0 and late-id cases, Anthropic event lists, `readSse` chunk lists (CRLF, split events, `data:` without space, multi-line data, comment lines, trailing partial event), `wholeToChunks`, `estimateTokens` with non-ASCII and astral text. Outputs: tagged bodies become `{ body, soft: body[SOFT] ?? null, dropped: body[DROPPED] }`; `responsesToOpenAI` → `{ body…, custom: [...custom] }`; streams → the array of strings each op returned; `readSse` → `[[event, data], …]`.
- [ ] **Step 3: harness** `fixtures_test.go`: reads every file, `jsv.Parse`s `in`, dispatches on `fn` through a `map[string]func(args []jsv.Value) (jsv.Value, error)` built from an `Env` whose `Random(n)` returns `n` bytes equal to a counter reset per case and `Now()` returns 1767225600000; compares `jsv.Canonical` of the result with `jsv.Canonical(out)`, or the error text with `throws` and, for a `*BadRequest`, `status` 400. In this task the map is empty and unknown functions `t.Skip`; Task 4 makes an unknown function a failure.
- [ ] **Step 4:** `node scripts/fixtures/gen-translate.js` and the `deno run` form produce no diff from each other; `go test ./translate/` passes (all skipped).
- [ ] **Step 5:** commit "Add the translate fixture generator and the Go replay harness" and push.

### Task 3: translate request and response functions

**Files:** Create `gw/translate/request.go`, `request_test.go`; modify `fixtures_test.go` (register the functions).

- [ ] **Step 1:** register `anthropicToOpenAI`, `openAIToAnthropic`, `openAIToAnthropicRequest`, `anthropicUsageToOpenAI`, `anthropicResponseToOpenAI`, `responsesToOpenAI`, `openAIToResponses`, `geminiToOpenAI`, `openAIToGemini`, `estimateTokens` in the harness → the fixtures now fail (functions missing).
- [ ] **Step 2: implement** line by line from `translate.js` with `jsv` helpers: `||` → `or(a, b)` (first truthy), `??` → `nullish`, `x.y` on a possibly-missing value → `jsv.Get` (throws like V8), `${…}` → `jsv.ToString`, `.join` → `join` (null/undefined → ""), `JSON.stringify` → `jsv.Stringify`, regexes `^bash_\d+`, `^text_editor_\d+`, `text$`, `^data:([^;]+);base64,(.*)$` (`.` excludes `\n \r    `) as small hand matchers. Each exported function runs inside `jsv.Catch`.
- [ ] **Step 3:** `request_test.go` adds direct tests for the stop tables and `BadRequest`.
- [ ] **Step 4:** fixtures for these functions pass.
- [ ] **Step 5:** commit "Port the translate request and response functions to Go" and push.

### Task 4: translate stream functions

**Files:** Create `gw/translate/stream.go`, `stream_test.go`; modify `fixtures_test.go`.

- [ ] **Step 1:** register `anthropicStream`, `responsesStream`, `geminiStream`, `anthropicEventsToOpenAI`, `wholeToChunks`, `readSse`; an unknown function now fails the harness → red.
- [ ] **Step 2: implement** the streams as types keeping the JS closure state (`started`, `open`, `index`, tool map by stream index, `usage`, `seq`, `output`), each method returning the text the JS method returns. `ReadSSE` keeps a byte buffer, finds `\r?\n\r?\n`, splits lines on `\r?\n`, takes `event:` (trimmed) and `data:` (one leading space removed), and calls `onEvent` when data lines exist; a trailing partial event is dropped, as in JS.
- [ ] **Step 3:** `stream_test.go`: `ReadSSE` with a reader that returns one byte at a time gives the same events as one read; a callback error stops the read.
- [ ] **Step 4:** all translate fixtures pass; `go test -race`.
- [ ] **Step 5:** commit "Port the translate stream transforms and the SSE reader to Go" and push.

### Task 5: `jsre` — JS regular expressions

**Files:** Create `gw/guard/jsre/parse.go`, `match.go`, `jsre_test.go`.

- [ ] **Step 1: failing tests**: `MustCompile(src, flags)`; `Exec(s []uint16, from int) []int` (capture offsets, −1 unset); helpers `ReplaceAll(s, func(m []int) string)`, `Test`. Cases measured in Node: lookbehind `(?<![\w.@]|(?<!/)/)\d+` on `"a1 /2 //3 .4 5"`; lazy `a[^.]{0,5}?b`; backreference `^(['"]).*\1$`; empty-iteration rule `(?:a?)*`; `\b` next to non-ASCII; `i` flag on `[a-z]` and `É`/`é`; `\s` matching ` ` and `﻿`; `.` not matching ` `; `{` literal (`a{`, `x{2,}`); quantified alternation backtracking `(?:ab|a)(?:bc)` on `abc`.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: implement** a parser to a node tree (literal/class/any, `^ $ \b \B`, groups capturing and not, lookahead and lookbehind (positive/negative), alternation, `* + ? {n} {n,} {n,m}` greedy and lazy, backreference, escapes `\d\D\w\W\s\S\n\r\t\v\f\0\xHH\uHHHH\cX` and identity escapes) and a continuation-passing backtracking matcher. `i` uses the non-unicode Canonicalize (simple upper case, no non-ASCII→ASCII mapping); class membership checks the case-fold orbit. Lookbehind is matched forward from each start that could end at the current position (no captures inside lookbehinds are used). The ES empty-check ends a loop iteration that matched nothing.
- [ ] **Step 4:** pass; `go vet`, `gofmt`.
- [ ] **Step 5:** commit "Add jsre, a backtracking regular-expression engine with JavaScript semantics" and push.

### Task 6: detectors — injection, secrets, PII

**Files:** Create `gw/guard/injection.go`, `secrets.go`, `pii.go`, `scripts/fixtures/gen-guard.js`, `gw/testdata/guard/*.json`, `gw/guard/fixtures_test.go`.

- [ ] **Step 1: generator** `gen-guard.js` (detector part): `scanInjection`, `secretSpans`, `maskPii(text, {home:false, counts})` → `{ text, counts }` over every text of `loadCorpus('all')` from `scripts/eval-signals.js` (injection, secrets, corrections, hallucination; `risky` uses `cmd`), the texts in `test/guardrails*.test.js`, and edge cases (short text, zero-width and split-letter obfuscation, leetspeak, IPv4/IPv6/phone/date/version boundaries, image-extension emails, PEM blocks, DB URLs with placeholder passwords, astral characters before a hit). Secret values are recovered by aligning each corpus text with its `<<SECRET:type>>` source and registered for encoding.
- [ ] **Step 2: harness** replays these three functions → FAIL.
- [ ] **Step 3: implement** with the JS pattern sources copied verbatim into `jsre.MustCompile`, the same anchor windows (`WINDOW_BEFORE` 260, `WINDOW_AFTER` 320, the 200-character look-back cap, `lastIndex` skip), doc-context checks, deobfuscation (zero-width drop, split letters, leet map), `isLiveSecret` (distinct code units of the tail ≥ 10), span merge, and `maskPii` order email → IPv4 → IPv6 → phone with `ipv6()` and `phone()`. Offsets are UTF-16.
- [ ] **Step 4:** pass.
- [ ] **Step 5:** commit "Port the injection, secret and personal-data detectors the guardrails call" and push.

### Task 7: guardrails API

**Files:** Create `gw/guard/guard.go`, `guard_test.go`; modify `gen-guard.js`, `fixtures_test.go`.

- [ ] **Step 1: generator** adds `cleanPolicy`, `resolvePolicy`, `blockMessage`, `checkBody` (every body of `test/guardrails.test.js`, `guardrails-gateway.test.js` and `guardrails-mcp.test.js` that reaches `checkBody`/`checkText`, in each mode and format, unknown format, non-object bodies, top-level strings and arrays, `BINARY` keys at top level, prefill, Responses model items) and `checkText` (parts `input`, `arguments`, `result`, a non-string text). A `GuardrailBlock` is written as `throws` + `hits`.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: implement** `makeRun`/`walk`/`lastAssistant` copy-on-write over `jsv` values (a changed string copies its parent object or array; unchanged values are shared), hits in first-seen order, `Block` error. The fail-closed path (a check that throws) is kept for panics from a detector: `log` mode marks `Error`, other modes return the error.
- [ ] **Step 4:** pass; `guard_test.go` checks that `CheckBody` never changes its input.
- [ ] **Step 5:** commit "Port the gateway guardrails to Go" and push.

### Task 8: CI regenerate-and-diff

**Files:** Modify `.github/workflows/ci.yml` (new job `fixtures`).

- [ ] **Step 1:** add a job on `ubuntu-latest` (all events): checkout, setup-node 24.21.0, setup-deno 2.9.8, then `node scripts/fixtures/gen-translate.js && node scripts/fixtures/gen-guard.js`, `git status --porcelain gw/testdata` must be empty (print the diff otherwise), then the same with `deno run -q --no-prompt --allow-read --allow-write=gw/testdata`, same check. The `go` job already replays the fixtures.
- [ ] **Step 2:** prove it fails: locally change one fixture byte, run the job's script, expect exit 1; restore.
- [ ] **Step 3:** commit "Regenerate the translate and guard fixtures in CI and fail on a diff" and push; dispatch `ci.yml` on the branch and watch.

## Spec deviations

1. **Two support packages inside the stream's folders**: `gw/translate/jsv` and `gw/guard/jsre`. The JS key order reaches bytes (tool arguments are `JSON.stringify` output, SSE `data:` lines), so Go needs ordered objects; and the detectors' regexes need lookaround, lazy quantifiers and a backreference, which `regexp` (RE2) lacks. Rewriting them by hand would not be provably equal; an engine with JS semantics runs the JS sources unchanged. `guard` imports `translate/jsv`.
2. **Streams are stateful types, not functions over a slice.** The JS transforms are incremental (P4c will feed them chunk by chunk); a slice function would have to be rewrapped. Fixtures replay an op list, which covers the "events in, events out" view.
3. **Signatures take `jsv.Value`**, not `json.RawMessage` or `map[string]any`, because `map` loses key order. Callers parse with `jsv.Parse` and write with `jsv.Stringify`.
4. **`readSse` is `ReadSSE(io.Reader, func(event, data string) error) error`.** Invalid UTF-8 is passed through as bytes (JS's `TextDecoder` writes U+FFFD); the corpus covers valid UTF-8 split across reads only.
5. **Strings are Go UTF-8 strings.** A JSON lone surrogate (`"\ud800"`) becomes U+FFFD in Go and stays a lone surrogate in JS; not in the corpus. Prototype-chain keys (`"constructor"` as a stop reason) are plain misses in Go.
6. **The CI step edits `.github/workflows/ci.yml`**, which the spec's table gives to no stream but the spec requires ("A CI step regenerates the fixtures"). It is a separate job so it does not touch the lines the other streams may change.
7. **More fixture functions than the spec lists:** `scanInjection`, `secretSpans`, `maskPii` and `cleanPolicy` are replayed too; they test the engine against the whole labelled corpus. `scopeTier`, `cleanSettings`, `cleanKeyPolicy` and `inScope` are not ported (settings and routing, not the request path's checks).
8. **TypeErrors:** Go reproduces V8's message only for property reads on `null`/`undefined` (`Cannot read properties of undefined (reading 'x')`); other malformed shapes (a number where an array is expected) return a `jsv.TypeError` with a Go message and are not in the corpus.

## Execution notes (recorded after the run)

9. **Signatures settled during the run:** `AnthropicUsageToOpenAI` returns `(*jsv.Object, error)` (a `null` usage
   throws in JS); the stream methods are `Push(chunk) (string, error)` / `Finish() (string, error)` on the stream
   value (the `Env` is held by the stream); `ResponsesToOpenAI` returns the custom names as `[]string`.
10. **Scanner prefixes are escaped in every fixture file** (`sk_` → `sk_`, also `rk_ pk_ sk- AKIA ASIA gh?_
    github_pat_ xox?- AIza hf_ eyJ -----BEGIN`), as the signals corpus does, besides the `<<ZPSECRET:i>>` encoding:
    GitHub push protection rejected two placeholder Stripe keys from the corpus otherwise.
11. **Speed:** `jsre` scans with a first-character filter. A 1 MB chat body with every check on takes about 0.8 s and a
    1 MB minified JS line about 2.3 s on an M-series Mac (the JS takes well under 1 s); the Go test bounds them at
    20 s. P4e wiring (stage 2) should measure again before the guard runs on the request path.

## Fix round 1 (review of 2026-11-12, `.superpowers/sdd/p4s1/stream-b-review.md`)

12. **C1: `jsre` is iterative.** The pattern compiles to a small program (`gw/guard/jsre/compile.go`) that `vm.go`
    runs with an explicit backtrack stack on the heap; counted repeats of groups are expanded, `*` loops keep the
    ECMA-262 empty-iteration check in registers that are restored on backtracking. Go's stack depth depends only on
    lookaround nesting in the pattern. An unbounded lookbehind is refused by `Compile` (it would be O(n²)).
13. **I1: a step budget per check, failing closed (Go only).** Each check on each string gets
    `min(20,000,000 + 600 × length, 3,000,000,000)` steps (`gw/guard/guard.go`; about 20× what the corpus and large
    legitimate bodies need: 198 steps per code unit worst on the corpus, 32 on a 1 MB agent log). Past it,
    `CheckBody` and `CheckText` return a `*Block` with `Overrun` set to the check, in every mode, `log` included,
    with the message "Blocked by ZAAFPulse guardrails: the <check> check did not finish within its limit. …". The JS
    has no budget, so on such inputs Go blocks where JS (slowly) answers; no fixture reaches the budget.
    The doc-context path (`inDocContext`) keeps each line's CODE_LINE / STRING_ITEM answer and finds line bounds by
    binary search over the newline offsets, so it is linear where the JS is quadratic, with the same results; the
    `around` text is built without copying the line. `jsre` also precomputes each atom's code-unit set, dispatches
    an alternation on the first code unit, and lets a single-character repeat skip the counts after which what
    follows cannot start.
14. **I2: drift.** `gen-guard.js` writes `gw/testdata/guard/patterns-all.json`: every regex source and flag set, the
    category metadata, the constants and tables of `lib/signals.js` (injection and secrets part), `lib/evals/pii.js`
    and `lib/gateway/guardrails.js` (read from the modules with `vm`), and a SHA-256 of the JS source the port
    follows. `gw/guard/patterns_test.go` compares it with the Go tables and the hashes in `patterns.go`. Proven: a
    new secret type added to `lib/signals.js` fails the Go test.
15. **M3: `ReadSSE`** resumes its separator search where it stopped and returns `ErrSSETooLarge` when one event
    passes `MaxSSEEvent` (16 MiB; the JS has no limit).
16. **M5:** the fixtures job runs every generator with `--allow-read --allow-write=gw/testdata` and explicit
    `--deny-env --deny-net --deny-run --deny-sys`; checked against stream C's `gen-provauth.js` (PR #6).
17. **M6:** the synthetic keys in `gen-guard.js` and `guard_test.go` are built by concatenation.
18. **Left as documented deviations (owner's call):** M1, invalid UTF-8 decodes to one U+FFFD per byte in Go and
    per maximal subpart in Node (stage 2 should decode with WHATWG rules or refuse invalid UTF-8); M2, duplicate JSON
    keys keep the last value, as `JSON.parse` does (stage 2 should forward the re-serialized body or refuse
    duplicates); M4, `jsre` truncates a non-BMP pattern literal and uses simple upper case for `i` (no ported pattern
    is affected).

### Timings: Go vs JS (`checkBody`), one user message, M-series Mac

Go: `go test -run TestAdversarialInputs ./guard/` (gw/guard/adversarial_test.go). JS: the same inputs through
`lib/gateway/guardrails.js` on Node 24.16. The machine was shared with two other agents (load about 4.7), so Go
numbers vary by up to 2× between runs; an earlier run measured exfil at 266 ms (1 MB) and 5.4 s (20 MB).

| Input | Policy | 256 KB Go / JS | 1 MB Go / JS | 20 MB Go / JS |
|---|---|---|---|---|
| `"token " + "a "×N` (split letters) | default | 176 ms / 22 ms | 142 ms / 33 ms | 4.0 s / 785 ms |
| `" " + "1:"×N + "x"` (IPv6) | pii redact | 86 ms / 4 ms | 99 ms / 13 ms | 2.9 s / RangeError (stack) |
| `" x@" + "a."×N + "1"` (email) | pii redact | 75 ms / 5 ms | 448 ms / 21 ms | 14.4 s / RangeError (stack) |
| `"send api key leak token "×N` (exfil) | all block | 170 ms / 32 ms | 266–655 ms / 109 ms | 5.4–15.7 s / 2.2 s |
| `"the ai said you "×N` | all block | 102 ms / 11 ms | 408 ms / 41 ms | 7.5 s / 794 ms |
| doc-context, one line | default | 23 ms / 173 ms | 99 ms / 2.5 s | 1.8 s / not run (about 17 min extrapolated, quadratic) |

Before this round Go crashed (`fatal error: stack overflow`) on the first three at 1 MB, took 2.5 s on exfil at 1 MB
and 1 min 54 s on doc-context at 1 MB. Exfil is now 2.4–6× the JS time; the 2× target is not met under load. All
outcomes equal the JS's where the JS finishes; at 20 MB V8 itself throws a RangeError on the IPv6 and email inputs.

## Fix round 2 (re-review 1)

19. **I3: the backtrack stack is capped.** `jsre.MaxFrames` = 2,097,152 frames of 40 bytes (80 MiB). Past it a run
    panics with `jsre.ErrStack`, which `Budget.Run` returns like `ErrBudget` and the guard turns into
    `*Block{Overrun}` in every mode (fail closed). The cap applies with or without a budget: a nil-budget call
    (`Exec`, `ScanInjection`, …) panics with `ErrStack` (see M7 below). To keep legitimate long repeats under it, a
    repeat whose atom cannot match empty no longer pushes the empty-check register frame (one frame per iteration
    instead of two), and `jsre.U16` no longer goes through a rune slice. Peak heap growth of one 20 MB body through
    `CheckBody` (`TestAdversarialInputs`, sampled every millisecond, bound 512 MB):

    | Input | Before | After | Outcome after |
    |---|---|---|---|
    | `"token "+"a "×N` | 2,838 MB | 358 MB | blocked: injection over its limit (JS returns `null` in 0.8 s) |
    | `" "+"1:"×N+"x"` (IPv6) | 2,456 MB | 399 MB | blocked: pii over its limit (JS throws a RangeError) |
    | `" x@"+"a."×N+"1"` (email) | 2,818 MB | 338 MB | blocked: pii over its limit (JS throws a RangeError) |
    | exfil | 360 MB | 248 MB | blocked (a hit, as in JS) |
    | `"the ai said you "×N` | 360 MB | 252 MB | no action, as in JS |
    | doc-context, one line | 330 MB | 174 MB | no action, as in JS |

    At 256 KB and 1 MB every outcome is unchanged and equal to the JS.
20. **M8: the cap lowers the per-character allowance above about 5M code units.** The budget is
    `min(20M + 600 × length, 3G)` steps; above about 5M code units the 3G cap gives fewer than 600 steps per code
    unit (150 at 20M). The heaviest crafted text found needs 208 steps per code unit, so a crafted string of 14M+
    code units overruns and **blocks even in `log` mode**. Legitimate text measured 8–40 steps per code unit and
    stays under the budget up to about 75M code units.

## P4e entry conditions

The reviewer's performance entry condition for wiring the guard into the request path (P4e, stage 2), verbatim:

These must be measured on the ubuntu CI runner with a committed benchmark: real text from the guard corpus, code and docs, and agent transcripts, plus the review's adversarial set. All three checks on.

1. **Typical latency for one request's guard pass:**
   - 32 KB: p50 ≤ 5 ms, p99 ≤ 15 ms.
   - 128 KB: p50 ≤ 30 ms, p99 ≤ 60 ms.
   - 1 MB: p99 ≤ 300 ms.
   - At each size, no more than 3× the JS on the same runner.

   Today's laptop numbers meet the absolute targets: 4.6 ms, 28 ms and 285 ms max.
2. **A bounded worst case per request:**
   - One budget shared across the whole `CheckBody` call (I4), plus a wall-clock deadline of at most 2 s. Running out of either returns a `*Block` (fail closed).
   - Any body up to `MAX_BODY` (20 MB) finishes or fails closed within 2.5 s of CPU.
   - Extra heap per request stays at or below 256 MB (I3 fixed).
3. **No false blocks:** no budget overrun on the real-text benchmark at any size up to 20 MB.
4. **The wiring treats any error from `CheckBody` that is not a `*Block` as a rejection, never as a pass.** Errors from a detector failing in `redact` or `block` mode are such non-`*Block` errors.

Shorter version: guard p99 ≤ 15 ms at 32 KB and ≤ 60 ms at 128 KB, within 3× of the JS, with a per-request budget and a deadline of 2 s or less that fails closed.

**Amended by the owner, 2026-11-13 (B2 review I1).** A 2 s wall-clock deadline blocked legitimate 20 MB bodies when
the CPU was contended, so condition 2 now reads:

- The per-call step budget is the primary bound. It is deterministic and does not depend on machine load. Running
  out of it returns a `*Block{Overrun}` in every mode.
- The wall-clock deadline is only a coarse backstop of 10 s per call, also failing closed.
- Every adversarial input of the reviews is stopped by the step budget (or the backtrack stack cap), never by the
  clock; the 2.5 s CPU bound for any body up to 20 MB applies on an idle runner.
- Condition 3 (no false blocks) is also checked with legitimate 20 MB bodies under concurrent CPU load.
- The heap bound (256 MB) is the extra Go heap of one call, sampled `HeapAlloc`, not process RSS (B2 review M5).

Also carried into P4e (not fixed in stage 1):

- **I4: one budget per `CheckBody` call, plus a 2 s deadline.** Today each check gets its own budget per string, so
  a 20 MB body split into 1,000 crafted strings runs 17–18 s with no overrun (JS 5.2 s). Share one `jsre.Budget`
  across the call and add a wall-clock deadline; running out of either returns `*Block{Overrun}`.
- **M7: budgets for the exported scanners.** `ScanInjection`, `SecretSpans` and `MaskPII` run with a nil budget (no
  step limit; the 80 MiB stack cap still applies and panics with `jsre.ErrStack`). Either unexport them (the fixtures
  can use the package-internal forms) or give them a `*jsre.Budget` parameter.
- **Heap per request:** condition 2 asks for at most 256 MB; one 20 MB body measures 174–399 MB today (note 19),
  mostly the UTF-16 copies of the text.

## Fix round 3 (re-review 2)

21. **I5: no limit can panic out of `CheckBody` or `CheckText`.** The walker's `data:` URL test (`BASE64_URL`) now
    runs under its own budget (`budgetFor(length)`); past it, or past `jsre.MaxFrames`, the call returns
    `*Block{Overrun: "data-url"}` ("the data URL check did not finish within its limit"). `catchCheck` also turns a
    raw `jsre.ErrBudget` or `jsre.ErrStack` from anywhere inside the call into `*Block{Overrun: "limit"}`, so a future
    unbudgeted path fails closed instead of panicking. A grep of every jsre call reachable from `CheckBody` and
    `CheckText` found no other unbudgeted one. Repro (now a regression test): default policy, one 4.4 MB user message
    `"data:a/b" + ";x"×2,200,000 + ";base64,AAAA"` panicked with `ErrStack`; JS returns null. Go now blocks it.

## P4e guard readiness (plan `2026-11-13-p4e-guard-ready.md`, branch `migrate/p4e-guard-ready`)

The library now meets the P4e entry conditions; it is still not wired into the request path (stage 2).

22. **I4: one budget per call, the primary bound; a 10 s backstop (owner's decision, 2026-11-13).** `CheckBody`
    and `CheckText` hold one `jsre.Budget` for the whole call, shared by every string, check and the data-URL test:
    20,000,000 steps, plus `600 × length` each time a check runs on a string, capped at 220,000,000 per call. Steps
    are weighted to cost about the same time whatever the input: a matcher instruction, a character a repeat
    consumes or an alternation choice is one step, a start attempt four, a start position the filters skip half of
    one, a visited string 16 (every string leaf, skipped ones too, so a body of tiny strings counts: B2 review M4).
    One step costs 3–7 ns on an M-series Mac and 2–2.5 times that on the Ubuntu runner, so the cap bounds a call to
    about 2 s of CPU there. Legitimate 20 MB bodies need 78M (the corpus), 112M (this repository's code) and 121M
    steps (numeric CSV, the heaviest measured): at least 1.8× below the cap, at any machine load. The budget also
    carries a wall-clock backstop of 10 s (`requestDeadline`, read every 65,536 steps and at the first step of a
    call; `jsre.ErrDeadline`). Past either the call returns `*Block{Overrun: <check>}` in every mode. M8 (note 20)
    now applies per call. Compiling the patterns (about 0.2–0.5 s, once) happens in `guard.Prepare`, before the
    first budget starts; stage 2 may call it at startup. `TestOneBudgetPerCall` takes the clock out of play and
    shows every adversarial input stopped by its steps (table below); `TestLegitimateBodiesUnderLoad` checks 20 MB
    code, corpus and CSV bodies, two at a time next to `GOMAXPROCS` CPU-burning goroutines: no block.
23. **M7: the exported scanners take a budget.** `ScanInjection(text, b)`, `SecretSpans(text, b)` and
    `MaskPII(text, b)` run under `b` (nil: `NewBudget(len)`, with the deadline) and return a limit as
    `jsre.ErrBudget`, `ErrStack` or `ErrDeadline`, never as a panic.
24. **N1: failures in `log` mode are reported.** `Result.Failures` and `TextResult.Failures` list each check whose
    detector failed in the call (`{Check, Count, Err}`); `Error` stays the JS boolean. Stage 2 should log or count
    them.
25. **Heap.** Peak extra heap for a 20 MB body is now 40–122 MB (bound 256 MB, asserted for the adversarial inputs,
    the 1,000-string bodies and the 20 MB corpus). The cuts: `ReplaceAll` returns its input when nothing matched;
    the PII passes alternate two buffers (and reuse the input once injection, which now runs first, has read it; its
    hits are still added last, in the JS order); `Str` writes UTF-8 without a rune slice; `deobfuscate` keeps a bit
    per dropped unit and rewrites leetspeak in place, only in runs that hold a digit; the backtrack stack grows in
    chunks of 16,384 frames of 20 bytes (`MaxFrames` is now 40 MiB) instead of by doubling.
26. **Speed.** Start positions are filtered by the first two code units, by word start for patterns that begin with
    `\b`, by a leading negative lookbehind's single characters, and by a trie of literal prefixes; a pattern that is
    a class run ended by a disjoint unit (the email pattern) is tried only before such a unit; a `^` pattern only at
    0. The injection patterns, the two anchor patterns and the secret patterns each run in one scan of the text
    (`jsre.Multi`), VMs are pooled. A pattern whose every match holds a unit of a set R among its first K units (a
    ':' within five for IPv6, a '.' within four for IPv4, '+', '(' or '-' within five for phones) is tried only where
    one is (`requiredWithin`), so numbers in CSV or code cost no matcher run. Every change is checked against the
    previous behaviour: `TestFiltersChangeNoMatch` (every guard pattern, with and without the filters, on the fixture
    inputs and the corpus; a deliberately broken filter fails it), `TestMultiMatchesExec`, `TestMultiAllMatchesEach`,
    `TestMatchInjectionMatchesReference`, `TestDeobfuscateMatchesReference`, and the fixtures, unchanged. The step
    cap is the only new behaviour on output: an adversarial body past 220M steps now blocks where the JS answers
    slowly (`"the ai said you "×N` and the doc-context line at 20 MB, which JS answers in 0.8 s and 17 min; recorded
    overrun deviations like note 13).
27. **C1 (B2 review): a redaction bypass, fixed.** The heap work let the PII passes write their result over the
    original buffer, and `text()` took "same buffer, same length" for "unchanged": with secrets and PII on `redact`,
    `"k " + <28-unit Anthropic key> + " m a@b.co"` (the key shrinks by one, the email grows by one) went upstream in
    clear while the result said `redacted`. "Changed" now comes from whether a check redacted in this text, and the
    returned string is compared by content; the PII passes record a replacement as it happens; no slice identity
    stands for "changed" anywhere (`sameSlice` is gone). Tests: `TestLengthNeutralRedactionsAreApplied`
    (secret+email, secret+2 emails, 4 emails+IPv4, secret+4 emails+IPv4, 9 emails+phone+secret, 2 secrets) and 18
    JS fixtures `checkBody-length-neutral-*` / `checkText-length-neutral-*` that `gen-guard.js` builds from
    length-cancelling combinations of keys, emails, IPv4 addresses and phones; 16 of them failed before the fix.
28. **B2 review minors.** M1: `TestGuardBench` measures each size three times and judges the median attempt (all
    three reported). M3: the 2.5 s bounds of `TestOneBudgetPerCall` apply only in `guard-bench` (Ubuntu); the 4-OS
    `go` job keeps a 30 s bound. M4: see note 22 (a step per visited string). Left as notes: M2, the deobfuscation
    path is now measured by `TestLegitimateBodiesUnderLoad` (code text) but not by the latency benchmark; M5, the
    heap bound is per call and `HeapAlloc`, not RSS (entry-condition text above).

### Benchmark (CI job `guard-bench`, ubuntu-latest)

`scripts/bench-guard.js` times the JS `checkBody`, `TestGuardBench` the Go `CheckBody`, on the same slices of the
corpus in `gw/guard/testdata/bench-corpus.txt` (guard corpus, `lib/` code, plans and specs, agent transcripts), one
user message, secrets and pii `redact`, injection `log`; each size judged on the median of three attempts. Run
43351126229 (c65d391; the final cap of 220M steps changes nothing here, no benchmark body comes near it):

| Size | Go p50 | Go p99 | JS p50 | JS p99 | Go/JS p50 | Go/JS p99 | Target |
|---|---|---|---|---|---|---|---|
| 32 KB | 2.52 ms | 4.71 ms | 3.36 ms | 8.60 ms | 0.75x | 0.55x | p50 ≤ 5, p99 ≤ 15 ms: met |
| 128 KB | 12.51 ms | 17.65 ms | 21.45 ms | 34.01 ms | 0.58x | 0.52x | p50 ≤ 30, p99 ≤ 60 ms: met |
| 1 MB | 63.40 ms | 128.21 ms | 91.05 ms | 260.35 ms | 0.70x | 0.49x | p99 ≤ 300 ms: met |

Runner speed varies between runs by up to 1.5x (an earlier run, 43343102925: 1.70/3.07, 8.61/11.94 and 42.6/82.5
ms). Before this work (M4 laptop): 32 KB 4.93 / 11.28 ms (2.8x the JS), 128 KB 26.2 / 42.7 ms, 1 MB 150 / 300 ms.
The corpus runs 106–247 ms at 2 MB, 229–241 ms at 5 MB and 925–945 ms at 20 MB on the runner, with no overrun.

Adversarial inputs, clock taken out of play (`TestOneBudgetPerCall`, same run, cap then 250M; at 220M the blocked
ones stop about 12% sooner). Every input ends by its steps or the stack cap, well before the 10 s backstop:

| Input (20 MB) | One string | 1,000 strings |
|---|---|---|
| `token `+`a `×N | 308 ms CPU, 40M steps, blocked (stack) | 947 ms, 90M, ok |
| IPv6 | 400 ms, 63M, blocked (stack) | 1.28 s, 143M, ok |
| email | 592 ms, 42M, blocked (stack) | 1.74 s, 215M, ok |
| exfil | 2.10 s, budget, blocked | 1.99 s, budget, blocked |
| `the ai said you `×N | 2.42 s, budget, blocked | 2.04 s, budget, blocked |
| doc-context line | 1.65 s, budget, blocked | 1.60 s, budget, blocked |

Legitimate 20 MB bodies under load (`TestLegitimateBodiesUnderLoad`, two at a time next to `GOMAXPROCS` burning
goroutines): corpus 1.9–2.0 s, CSV 2.3 s, code 2.4–2.6 s wall, no block. Heap per call: 7–122 MB.

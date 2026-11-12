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

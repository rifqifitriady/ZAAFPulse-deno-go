# The token saver on the Go engine (`lib/gateway/rtk.js` → `gw/engine`): design

Date: 2026-11-17. Branch `migrate/p4-rtk-go`, based on `migrate/deno-go` at `636944a` (P4f merged). Status: design,
no code. Owner decision of 2026-11-17 (P4d spec, owner answer 28): the token saver is its own slice, done before
gate G3; the program plan lists it as a G3 prerequisite (P4d branch, `8e7b1fd`).

Inputs read:

- `lib/gateway/rtk.js` (313 lines, the whole saver), its call sites in `lib/gateway/index.js` (`tokenSaverOn`,
  `proxyChat`, `anthropicPassthrough`, the settings API), `lib/gateway/usage.js summary`, `lib/otel.js` (the span
  attribute), `public/gatewayview.js` (the tiles), `test/router9.test.js` (the saver's tests).
- P4b, P4c, P4e, P4f plans and results: the hand-off rule (`handoff("token saver: tool results")` in
  `gw/engine/chat.go PlanChat`), P4b minor m6' (JS throws on some bodies with the saver on; the engine forwards them),
  the shadow (`gw/engine/shadow.go Compare`, `usageFields` already holding `savedTokens`), the switch rule
  (`lib/front/shadow-log.js gate`, 1 clean day and 20 compares), P4e Decision 4 (planning stays pure; the expensive
  step runs in `Serve` and in the compare goroutine), P4f's journal and spans (`spans.go` already writes
  `zaafpulse.token_saver.saved_tokens`), the fixtures convention (`scripts/fixtures/lib.js writeCases`, the CI fixtures
  job regenerating on Node and on Deno with `--allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run
  --deny-sys`).
- The P4d spec and plan on `migrate/p4d-translate-wire` (Decision 16: the token saver is handed off where it would act;
  `xlate.go translatedPlan`, `passthrough.go planAnthropicNative`, `hasToolResultBlock`, `plan.go tokenSaverOn`).
- The program plan §5 P4 and its G3 prerequisites; `gw/guard/jsre` (the JS regex engine of P4e); `gw/engine/jsjson.go`
  (JSON as JS reads and writes it).

## What `rtk.js` does to a request (the reference)

| # | Behaviour | Where |
|---|---|---|
| R1 | Runs only when `gateway.tokenSaver !== false` and the header `x-zp-token-saver` is not `off` (any case; repeated headers joined with `, `). The settings API turns it on or off; the config push already carries `tokenSaver`. | `index.js tokenSaverOn` |
| R2 | **Chat** (`proxyChat`, also every translated call: `/v1/messages`, `/v1/responses`, Gemini go through `translated()` → `proxyChat`): `saveTokens(body, 'openai')` after `authorize` (403), `targetsFor` (404, price 402), `allowedTargets` (budget 402); before the guardrails and the provider call. In place: the body object itself is changed. | `index.js:911-997` |
| R3 | **Anthropic passthrough**: `saveTokens(body, 'anthropic')` after the same checks, before `guard({...body, model: t.model}, 'anthropic')`. The Responses passthrough, embeddings and both count paths have no saver. | `index.js:1374-1400` |
| R4 | OpenAI walk: for each message, each `tool_calls` entry gives a hint `"<function.name> <a.command‖a.cmd‖a.pattern‖a.file_path‖a.path‖a.input‖''>"` with `a = JSON.parse(function.arguments ‖ '{}')` (a parse error leaves `a = {}`), kept in a `Map` by `tc.id`; a `role: "tool"` message's `content` (a string, or the `text` of each `{type: "text"}` item) is compressed with the hint of its `tool_call_id`. | `rtk.js:270-302` |
| R5 | Anthropic walk: each `tool_use` block gives a hint `"<name> <input.command‖input.pattern‖input.file_path‖input.path‖''>"` by `id`; each `tool_result` block that is not `is_error` has its `content` (string, or the `text` of `{type: "text"}` items) compressed with the hint of its `tool_use_id`. One pass: a result before its `tool_use` gets no hint. | `rtk.js:243-269` |
| R6 | `compressText(raw, hint)`: nothing for a non-string, fewer than 600 or more than 10 MiB UTF-16 units, or a text starting `[error]`. ANSI escapes are removed, a filter is picked, applied (a filter that throws leaves the clean text), and the result kept only when shorter than the raw text and not blank; else the ANSI-stripped text when that alone is shorter (`filter: 'ansi'`). | `rtk.js:220-236` |
| R7 | `pickFilter(text, hint)`, first match wins, on the lower-cased hint and the first 2048 units: `git-diff`, `git-log`, `git-status`, `build`, `grep`, `json` (starts with `[`/`{`, ends with `]`/`}` in the last 64 units, `JSON.parse` succeeds), `listing`, `read`, `log` (over 4000 units), else none. | `rtk.js:182-217` |
| R8 | The nine filters (lossy on purpose; thresholds are code, not config): git-diff (hunks to 60 lines, 300+100 lines), git-log (headers and first message line, 150+20), git-status (25 files per block), build (errors with 3 lines after, 5 warnings, progress counted, last 25 lines), grep (10 matches per file, 40 files, 20 other lines), listing (noise folders dropped, 12 per folder, 40 folders), json (arrays cut to 20 items, strings over 2000 units cut, objects below depth 12 rebuilt), read (160+60 lines), log (deduped, 200+80 lines). | `rtk.js:49-179` |
| R9 | Result: `{ savedChars, savedTokens: Math.round(savedChars / 4), hits }`, or `null` when no text was shortened. `savedTokens` goes into `base` (`...(rtk ? { savedTokens } : {})`): every `proxyChat` line built from `base` (the answer, a provider non-2xx, a stream's end) and every `anthropicPassthrough` line (also its guardrail lines, which spread `base`). The guardrail lines of `proxyChat` and its 599 and fallback lines carry none. | `index.js:918, 993, 1386, 1396` |
| R10 | The dashboard: `usage.summary` adds `savedTokens`, `saverHits` (lines with a non-zero `savedTokens`) and `savedCost = cost(e.model, { input: savedTokens })` per month; the Gateway page and its overview card show them; OTel adds `zaafpulse.token_saver.saved_tokens` when non-zero. | `usage.js:160-164`, `gatewayview.js:920, 1160-1169`, `otel.js:1341` |
| R11 | Throws (JS answers 500, no provider call): a `null` message; `tool_calls` truthy and not an array (a non-empty string iterates characters, which have no `function`); a `tool_calls` entry that is `null`, a primitive, an array, or has a nullish `function`; `arguments` that parse to `null` (`a.command` outside the `try`); a `null` item of a tool message's `content` array or of an Anthropic message's `content`; a hint value that is an object with an own `toString` key, or an array holding one (template-literal `ToString` throws). | `rtk.js` walk |
| R12 | No LLM, no network, no file, no clock, no randomness, no state across requests: the only state is the per-call `Map` of hints. Nothing is cached. | `rtk.js` (requires nothing) |

## Decisions

Each one line, with the evidence that settled it.

1. **Port all of `rtk.js` into `gw/engine`, and remove the hand-off in all three places**: chat (`PlanChat`), translated
   calls (`translatedPlan`) and the Anthropic passthrough (`planAnthropicNative`). Evidence: owner answer 28; R2, R3;
   the program plan's G3 prerequisite ("until then every request it would act on is handed to js").
2. **New files in `gw/engine`, not a new package.** The json filter needs `engine.Parse`, `Stringify`, `QuoteJS` and
   `NumberString`; a package under `engine` that engine imports cannot import engine back (cycle). Files:
   `jsstr.go` (JS string helpers), `rtk.go` (text layer), `rtk_json.go` (json filter), `rtk_body.go` (the walks).
3. **All text work in UTF-16 code units** (`jsre.U16`, `[]uint16`): every length, threshold and cut in R6–R8 is
   `String.length` (600, 10 MiB, 2048, -64, 2000, 4000, `savedChars`). Converted back once per shortened text.
4. **The regexes run on `gw/guard/jsre` with the sources of `rtk.js` unchanged, and `jsre` gains the `m` flag.**
   Six patterns use `/m` (`^diff --git `, `^commit …$`, `^On branch …`, the build head, the grep and read line counts);
   `listing` uses a lookahead `(?=\S+$)` that RE2 cannot express; `\s`, `\b` and `/i` must be JS's. Nothing else in
   `jsre` changes; the guard's patterns use no `m`, so its fixtures prove it unchanged.
5. **JS string semantics ported exactly where they are observable**: `trim` (JS WhiteSpace and LineTerminator, which
   is `jsre`'s `\s` set, exported as `jsre.IsSpace`); `toLowerCase` of the hint reduced to what the hint patterns can
   see (A–Z, U+212A KELVIN SIGN → `k`, U+0130 → `i` + U+0307; every other mapping stays non-ASCII, which no hint
   pattern distinguishes: they test ASCII letters, `\b` and `[^|;&]`); template-literal `ToString` of JSON values
   (`undefined`, `null`, numbers as `NumberString`, arrays joined by `,` with nullish items empty, `[object Object]`,
   a TypeError for an own `toString` key); the hint `Map` by SameValueZero (a number id never finds a string id,
   `-0` finds `0`, a missing id finds a missing id, an object id finds nothing).
6. **The json filter is one pass over `engine.Parse`'s tree** that cuts and writes at once (`JSON.stringify(cut(v))`):
   object keys in JS order, numbers as `NumberString`, a string cut at 2000 units whose last unit is a high surrogate
   written as `\udXXX` (lower-case hex) as `JSON.stringify` writes a lone surrogate. Inner JSON that `engine.Parse`
   reports `ErrUnsupported` (an escaped lone surrogate, nesting over 512) is handed off; `ErrSyntax` is "not json", as
   `JSON.parse` throwing is.
7. **Throw parity by hand-off**: every body of R11 is handed off with "token saver: a message it cannot read", so JS
   answers it as today. This also closes P4b minor m6' (the engine forwarded such bodies to the provider). Arguments
   that `engine.Parse` cannot hold hand off with "token saver: a tool call the engine cannot read".
8. **Planning stays pure and cheap (P4e Decision 4)**: `PlanChat` (and P4d's planners) run the structural walk only
   (hints, throws, the list of texts the saver will look at, with pointers into the plan's pending body).
   `State.Save(p)` runs the filters. The shadow plans on the forwarding path of a js-served request, where filters on
   10 MiB must not run.
9. **Order on the go path**: plan (structural walk; hand-offs of D7) → modes → `Save` → `Take` (429) → price (402) →
   budget (402) → `Guard` (400/500) → provider. `Save` runs before `Take` so that its own hand-offs (D6, D11) are exact:
   nothing is counted yet. Its result is invisible to a 429 or 402 (neither writes a usage line). The guardrails check
   the shortened body, as JS does (`saveTokens` before `guard`): a secret in an omitted middle is not seen, in both.
10. **Translation (P4d)**: on translated calls `Save` runs on the translated chat body (`openai` format) before
    `p.build` (which, for an Anthropic target, translates the chat body again); on the Anthropic passthrough it runs
    on `{...body, model: t.model}` (`anthropic` format) before the guard. Same pointers, same pending body.
11. **A step budget**: `Save` runs under a `jsre.Budget` of `64 × units + 2^24` steps (units: the tool-result texts it
    looks at) and a 2 s deadline; an overrun hands off with "token saver: over its step budget". JS has no limit; the
    hand-off keeps the answer exact (JS answers, slowly, as today). The ANSI pattern on a text full of `ESC ]` without a
    terminator is quadratic in both engines. Owner question 1.
12. **Usage exactly as JS writes it (R9)**: `savedTokens = ⌊(savedChars + 2) / 4⌋` (`Math.round` of a non-negative
    quarter), present (also as `0`) whenever at least one text was shortened. It goes into `Plan.Base`; `refusal()`
    and `Failed()` drop it for chat-format plans (`proxyChat`'s guard callback, 599 and fallback lines have none) and
    keep it for the Anthropic passthrough (its guard lines spread `base`). No IPC change: `UsageRecord.SavedTokens
    *int64` (`omitzero` on a pointer writes `0`) and `usage.report.schema.json savedTokens ≥ 0` exist; the ingest
    stores the line through the JS writer, so `summary`'s `savedTokens`, `saverHits`, `savedCost` and the OTel
    attribute (already in `spans.go`) need nothing new. The call's cost stays the provider's tokens at list price.
13. **A switch of its own, `token-saver`**, with an engine entry, a shadow entry and a switch-rule record: a call the
    saver will look at is served on go only when its sub-class **and** `token-saver` are on go; else it is forwarded
    (compared when either is shadowed). Evidence: the rule is per class (P4b Decision 10), and a class already on go
    (`chat`) would otherwise start rewriting tool results without a single compare (P4c Decision 2, P4d Decision 2 use
    sub-classes for the same reason). One record instead of a saver variant of each of the 13 sub-classes; the
    record counts only compares in which a saver shortened something (D14). Owner question 2.
14. **The shadow compares rewritten calls with what it already captures**: the provider request body (JS sends the
    shortened body) and `usage.savedTokens` (already in `usageFields`). The compare goroutine runs `Save` before
    `Guard`; a `Save` hand-off is a `handoff` line. The line gains `saver: true` when either engine's saver shortened a
    text; `shadow-log.js` counts such a line for its class and for `token-saver`. No prompt text in the line, as now.
15. **Proof by fixtures from the real JS, two layers.** Function level: `scripts/fixtures/gen-rtk.js` →
    `gw/testdata/rtk/` (`compressText`, `pickFilter`, `saveTokens` with the body after and the result, or `throws`),
    from hand-written cases per filter and edge, a seeded corpus (mulberry32: 160 texts and 40 JSON documents) and big-input recipes.
    Route level: `saver-*` cases added to `scripts/fixtures/gen-chat-guard.js` → `gw/testdata/chat-guard/` (its
    in-process createGateway harness already has keys, guardrails, budgets and streams; it gains a request header, a
    `tokenSaver` setting and a provider that throws): provider request, client answer, usage line, replayed by
    `TestChatGuardFixtures`. Translated and passthrough cases join P4d's `gen-xlate.js` after P4d merges. Both
    generators are pure and run under the fixtures job's deny flags.
16. **Fixture size**: no fixture file over 512 KiB. Inputs over 64 KiB are recipes (`[[text, times], …]`, the Go test
    builds them); outputs over 64 KiB are `{ length, sha256 }` of the UTF-8 text (`crypto.createHash`, no permission).
17. **Implementation order, riskiest first**: (1) the JS truth, (2) `jsre` `m` and the string helpers, (3) the text
    layer byte for byte, (4) the walks and throw parity, (5) budget, memory and benchmark: none touches a P4d file and
    all can start now. Then (6) chat wiring, (7) shadow and switch, (8) translated and passthrough, (9) contract suite,
    (10) results: each needs P4d merged and is marked "re-check after P4d merges".
18. **JS quirks are copied, not fixed** (P4d owner answer 31): `savedTokens: 0`, chat guard lines without
    `savedTokens` but passthrough guard lines with it, the `[error]` test on the raw text, `dedupe` dropping a leading
    blank line (`a[-1] || ''`), a warning past the fifth counted as progress when it also looks like progress, JS's
    `ms` starting after the saver (not compared).
19. **Memory**: per text, the UTF-16 copy, the ANSI-stripped copy and the output; a test bounds the allocation of
    `Save` on a 10 MiB tool result at 8 × its UTF-8 size. P4e's `gateway.memLimitMB` guidance is unchanged.
20. **M3/M4 stay owed** to the final quiet-machine step (owner deferral): the bench gains `ZP_BENCH_TOOL_RESULT=<bytes>`
    (a tool message of that size in each call) and the owed list adds "chat with a 64 KiB tool result". A Go benchmark
    of `Save` on 64 KiB, 1 MiB and 10 MiB records numbers in the results doc.

## Owner answers (2026-11-20)

21. **A step-budget overrun hands the call to JS** (question 1). Go never sends a text unshortened that JS would have
    shortened: exact parity over speed. Decision 11 stands.
22. **One shared `token-saver` switch** for every class (question 2), not one per class. Decision 13 stands.

23. **The step budget's constant is 2^24, not 2^20** (measured while building Task 5, 2026-11-20). Three head
    patterns run on the first 2048 units, and the `m` ones are quadratic there in both engines:
    `^\s*\d+[\t|→]` lets `\s*` run across every following line. A tool result of 2048 or more blank lines costs
    4.24M steps in Go (≈ 25-48 ms; V8 takes 38 ms on the same text). Under `64 × units + 2^20` every such result
    would be handed to JS. With 2^24 the measured cap uses a quarter of the limit, the same headroom as the rest.
    The owner's rule (answer 21: an overrun hands off) and the 2 s deadline are unchanged.

## Design

### D1. String helpers (`gw/engine/jsstr.go`) and `jsre` (`gw/guard/jsre`)

`jsre.Compile` accepts `m`: `^` also matches after a line terminator (`\n`, `\r`, U+2028, U+2029), `$` also before
one; the "anchored: only position 0" optimization is off under `m`. `jsre.IsSpace` exports the existing `\s` test.
`jsstr.go` holds `u16`, `cat`, `splitNL`, `joinNL`, `jsTrim`, `hintLower`, `jsToString`, `mapKey`, `units`.

### D2. Text layer (`gw/engine/rtk.go`, `rtk_json.go`)

```
compressText(raw, hint []uint16, b *jsre.Budget) (text []uint16, hit *rtkHit, why string)
pickFilter(text, hint []uint16, b *jsre.Budget) (name string, parsed *Value, why string)
rtkFilters[name](text []uint16, b *jsre.Budget) []uint16     // git-diff … log
rtkJSON(v *Value) []uint16                                   // json, from the tree pickFilter parsed
```

The JS `try { FILTERS[f](clean) } catch { text = clean }` has no Go counterpart: no Go filter can fail on a text it is
given (the json filter's only failure, a stack overflow deep in `cut`, cannot happen at depth ≤ 512, the most
`engine.Parse` accepts; a fixture at depth 512 proves V8 does not throw there on Node or Deno).

### D3. Walks (`gw/engine/rtk_body.go`)

```
scanSaver(body *Value, format string) (*saverJob, string)   // structural walk: targets with hints, or a hand-off reason
(*saverJob).run(b *jsre.Budget) (chars, hits int, why string)
SaveTokens(body *Value, format string) (*SaveResult, string) // scan + run, for the function fixtures
```

A target is `{holder *Value, key string, hint []uint16}`: `holder[key]` is a string. `run` replaces it with
`Str(shortened)` (`Set` keeps the key's place).

### D4. Planning and serving (`chat.go`, `serve.go`, P4d's `xlate.go`, `passthrough.go`, `guard.go`)

`Plan` gains `saver *saverJob`, `saved bool` (Save ran), `saverHits int`. With the saver on:
`job, why := scanSaver(out, format)`; `why` → hand-off; a job with no candidate text (none of 600 to 10 MiB units
outside `[error]`) is dropped (nothing can change; the hand-off-free path stays as today). With a job, the body is
built by `Save` (or by `Guard`, when a check applies, from the same pending body). `Serve`: the mode of a plan with a
job is `mode(sub) ∧ mode("token-saver")`; then `Save` before `Take`; its hand-off forwards to js.

### D5. Switch (`lib/front/index.js`, `lib/front/shadow-log.js`, `gw/front/front.go`, `gw/cmd/zaafpulse-gw/front.go`, `ipc/v1/config.push.schema.json`)

`token-saver` joins the go classes on both sides (`GO_CLASSES`, `GoClasses`) and the schema's engine-class enum, so
`gateway.engines["token-saver"]` and `gateway.shadow: ["token-saver"]` validate. It is not a path class: the front
routes as now; only the engine reads its mode. `routing` gates it as any class; `/api/gateway/status` shows its note.

### D6. Shadow (`gw/engine/shadow.go`, P4d's `shadow_xlate.go`)

The compare goroutine: `if why := s.Save(p); why != "" { handoff line }` before `Guard`; `ShadowLine.Saver` set from
`p.saverHits > 0 || capture usage has savedTokens`. `CompareTranslated` does the same before its replay.

## Tests and fixtures

| Level | What | Where |
|---|---|---|
| Fixtures, function (JS truth) | every filter on its hand-written cases; every `pickFilter` branch by hint and by head; thresholds 599/600, 4000/4001, 2000/2001-unit strings, 20/21 items, depth 11/12/512; ANSI CSI and OSC (BEL and ST), OSC without terminator; Unicode whitespace in `trim`, `İ`/`K`/`Σ`/fullwidth hints; surrogate pair at unit 2000; `-0`, `1e21`, `1e400`, duplicate and array-index keys, `__proto__`; Map keys `1` vs `"1"`, missing ids, `tool_use` after its result; every R11 throw; `is_error`, `[error]`; 160 seeded random texts and 40 JSON documents; recipes of 1 MiB and 10 MiB (+1 unit) | `scripts/fixtures/gen-rtk.js`, `gw/testdata/rtk/`, `gw/engine/rtk_test.go` |
| Fixtures, route (JS truth) | chat whole and stream with a shortened tool result; header `off`; `tokenSaver: false`; a result under 600; guard redact on the shortened body; a secret only in the omitted middle (no hit in both); guard block (line without `savedTokens`); budget and price 402 (no line); provider 500 (line with `savedTokens`); provider throw (599 without); `savedTokens: 0` | `scripts/fixtures/gen-chat-guard.js` (`saver-*`), `gw/testdata/chat-guard/`, `gw/engine/chat_guard_test.go` |
| Fixtures, route, after P4d | `/v1/messages` translated with a `tool_result` (whole, stream), Anthropic passthrough with a `tool_result` (whole, guard block line with `savedTokens`), Responses with `function_call_output`, Gemini with `functionResponse` | P4d's `gen-xlate.js` cases, `gw/testdata/xlate/` |
| `jsre` | the `m` flag against Node's matches (added to `jsTruth`), the anchored optimization off under `m`, every guard fixture unchanged | `gw/guard/jsre/jsre_test.go`, `gw/guard` |
| Go unit | hand-off reasons per R11 shape; `Save` idempotent; budget overrun hands off; output always valid UTF-16; allocation bound on 10 MiB; benchmark 64 KiB/1 MiB/10 MiB | `gw/engine/rtk_*_test.go` |
| Shadow | a shortened call compares `same`; a one-byte change in the JS body is a `diff`; `saver: true` counted for `token-saver`; a `Save` hand-off is a `handoff` line | `gw/engine/shadow_rtk_test.go`, `test/front-shadow.test.js` |
| Contract | tool-result cases served by go under `ZP_ENGINE=go` with `token-saver` on go; `ZP_ENGINE=shadow` ends with diff 0 | `test/contract/saver.test.js` |

## Exit criteria

- Every `gw/testdata/rtk` and `gw/testdata/chat-guard/saver-*` fixture passes byte for byte (text, filter, saved, body after,
  throws; provider request, client status and bytes, usage line); the P4d translated and passthrough saver cases pass;
  both generators give the same bytes on Node and Deno under the fixtures job's flags.
- `jsre` with `m` passes its JS truth; every `gw/testdata/guard` and `chat-guard` fixture is unchanged and green.
- No `token saver: tool results` hand-off remains; the only saver hand-offs are D6, D7 and D11.
- Under `ZP_ENGINE=go` the contract tool-result cases are served by go; `ZP_ENGINE=shadow` ends with diff 0.
- The full CI matrix is green; `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean.
- The results doc lists the benchmark numbers and the owed M3/M4 tool-result scenario; the program plan's G3
  prerequisite line for the token saver is marked met.

## Out of scope

- Changing what the saver keeps (filters, thresholds): equality first; a change goes into both engines after G3.
- Routes (`router/*`) and the model cache: still handed off (P4d owner answers 29, 30).
- A token saver for the Responses passthrough, embeddings or counts: JS has none.
- Model-output shortening: JS only shortens request tool results.

## Open questions for the owner

Answered on 2026-11-20 (Decisions 21–22); kept as asked.

1. **Step-budget overrun.** A tool result that runs the saver past `64 × units + 2^20` regex steps (2^24 since Decision 23) or 2 s is handed to
   JS (exact, and as slow as today). The alternative is to send that one text unshortened from Go (fast, but the body
   then differs from what JS would send, and the shadow reports it). Default: hand off.
2. **One `token-saver` switch for every class.** The saver's switch rule is one record fed by every class's compares
   where a saver shortened something; turning it on lets chat, translated and passthrough calls rewrite on go at once
   (each still needs its own class on go). The alternative is a saver record per class (13 more entries). Default: one.

## Files that overlap P4d (`migrate/p4d-translate-wire`, in progress)

Tasks 1–5 touch none of these. Tasks 6–10 do, and each such step is marked **re-check after P4d merges**:

- `gw/engine/chat.go` (`PlanChat`'s hand-off, `Plan` fields, `Failed`): P4d moves the checks into `plan.go admit`,
  adds `upstreamBody`, `tokenSaverOn`, `Plan.build`, `Shape`, `Class`.
- `gw/engine/serve.go` and P4d's new `dispatch.go`: P4d dispatches by class; `Save` goes into each serve path before
  `Take`.
- `gw/engine/guard.go` (`refusal`): P4d gives it the plan's `Shape`.
- `gw/engine/shadow.go`, P4d's `shadow_xlate.go`: the compare goroutines.
- P4d's `xlate.go` (`translatedPlan`), `passthrough.go` (`planAnthropicNative`, `nativePlan`) and `hasToolResultBlock`
  (replaced by `scanSaver`).
- `lib/front/index.js` (`GO_CLASSES`, `routing`), `gw/front/front.go` (`GoClasses`), `scripts/fixtures/gen-xlate.js`,
  `test/contract/harness.js`, `bench/run.js`, and the program plan's G3 prerequisite lines (written on the P4d branch).

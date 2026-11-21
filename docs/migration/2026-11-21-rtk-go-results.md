# Token saver results: `lib/gateway/rtk.js` on the go engine (G3 prerequisite)

Date: 2026-11-21. Branch `migrate/p4-rtk-go`, cut from `migrate/deno-go` at `636944a` (P4f merged), with P4d merged in
at `1cef2ae` (from `61ec0aa`, PR #13). Spec the rtk-go design (Decisions 1–20, owner
answers 21–22, Decision 23 from Task 5), plan the rtk-go plan ("Plan deviations" lists
every change from the plan, with evidence).

## What ships

- **The token saver in Go** (`gw/engine/jsstr.go`, `rtk.go`, `rtk_json.go`, `rtk_body.go`):
  - `compressText`, `pickFilter`, the nine filters and both body walks, byte for byte as `rtk.js`;
  - text in UTF-16 code units, rtk.js's own regex sources run on `gw/guard/jsre`, which gained the `m` flag.
- **Where it runs**: everywhere JS runs `saveTokens`, on the same body, before the guardrails:
  - chat (`/v1/chat/completions`, also to an Anthropic-format provider: before the leg's translation);
  - every translated call (`/v1/messages`, `/v1/responses`, Gemini) on the translated chat body;
  - the Anthropic native passthrough on `{...body, model}`.

  The Responses passthrough, count_tokens, Gemini countTokens and embeddings have no saver, as in JS.
- **Order on the go path**: plan (the walk only) → modes → saver → rate limit (429) → price and budget (402) →
  guardrails → provider. The saver runs before the rate limit takes the call, so its hand-off is exact. A key already
  at its limit gets its 429 without the saver's work, as JS refuses it before `saveTokens`.
- **`savedTokens` on exactly the lines JS writes it**:
  - yes: the answer, a provider non-2xx, a stream's end, and every Anthropic passthrough line (its guard lines too);
  - no: chat and translated guard lines, a 599, the Anthropic leg's build refusal, a line recovered from the journal.

  The dashboard's tokens saved, saved cost and saver hits, and the OTel attribute, read the ingested line: nothing new
  on the JS side.
- **The `token-saver` switch** (owner answer 22: one for every class), with its own engine entry, shadow entry and
  switch-rule record, default `js`:
  - a call the saver looks at is served on go only when its sub-class and `token-saver` are both on go;
  - it is compared when either is shadowed, also while its class is on js (the front routes chat, messages, responses
    and gemini to the engine for a shadowed `token-saver`);
  - shadow lines in which a saver shortened something carry `saver: true`, and `lib/front/shadow-log.js` counts them
    for `token-saver` too.
- **Hand-offs kept** (JS answers, exactly as today):
  - `token saver: a message it cannot read`: every body on which `saveTokens` throws. This closes P4b minor m6': the
    engine used to forward such bodies to the provider.
  - `token saver: a tool call the engine cannot read`: arguments with an escaped lone surrogate.
  - `token saver: a JSON tool result the engine cannot hold`: an escaped lone surrogate, or nesting over 512.
  - `token saver: over its step budget`: `64 × units + 2^24` steps or 2 s (owner answer 21).
  - `token saver: a tool call id the engine cannot match`: a translated body whose tool ids are objects, which JS
    matches by identity.
  - `token saver: the engine's saver panicked`: a bug of the port, logged with the panic's value; never a crash.

  The shadow log shows every saver hand-off under `token-saver` too (as hand-offs, never as compares).

  The hand-off `token saver: tool results` is gone from chat, translated calls and the passthrough.

## Evidence

| What | Result |
|---|---|
| Function fixtures from the real JS (`scripts/fixtures/gen-rtk.js` → `gw/testdata/rtk`) | 352 cases: 53 hand-written `compressText`, 35 `pickFilter`, 58 `saveTokens` (17 where JS throws), 6 big-input recipes (1 MiB, exactly 10 MiB, 10 MiB + 1), 160 seeded random texts and 40 random JSON documents. Every case equal; Node and Deno write the same bytes under the fixtures job's deny flags |
| Route fixtures from the real JS | 13 `saver-*` in `gw/testdata/chat-guard` (whole, stream, header off, setting off, short result, `savedTokens: 0`, redact after, secret in the omitted middle, guard block, budget 402, price 402, provider 500, provider throw); 14 `*saver*` in `gw/testdata/xlate` (translated whole, stream, header off, guard block, guard redact, throw; passthrough whole, stream, guard block; chat → Anthropic whole and stream; Responses translated and native; Gemini) |
| Independent reviews | Final whole-branch review: 1 Important (a panic in the saver was not recovered: the shadow's goroutine would crash the gateway), fixed; Minors fixed or noted (a rare double rate-limit count when the window frees between the look and the take, documented in `serve.go`). Tasks 1–5: 26,000 random texts and 24,000 random bodies against the JS, 0 mismatches (2 Minor over-hand-offs fixed). Tasks 6–7: 1 Important (a shadowed `token-saver` compared nothing while chat was on js), 4 Minor, all fixed. Tasks 8–9: 1 Important (Gemini tool ids shared by identity), 4 Minor, all fixed |
| Contract suite | `test/contract/saver.test.js`: 7 cases (chat whole and stream, header off, the 502 of an unreadable message, translated messages, the Anthropic passthrough, a Responses-native passthrough left unchanged) in js, go (each line from the gateway) and shadow (diff 0) modes. The whole suite (205 tests) passes in all three modes |
| Step budget | Worst fixture uses 4.3% of its limit. The quadratic head scan (`^\s*\d+[\t\|→]` with `m` on a whitespace-only head, in V8 too) is capped at 4.24M steps for 2048 blank lines, a quarter of the `2^24` constant (spec Decision 23) |
| Memory | Save on a 10 MiB tool result allocates 5.96× the text for a log and 3.04× for a grep output (bound 8×). Calls that run the saver at once are not limited: owners who send multi-megabyte tool results should size `gateway.memLimitMB` for about 6× those texts per concurrent call, next to P4e's guidance for guarded calls |
| Speed (`BenchmarkSaveTokens`, the owner's laptop, saver only, one run of 3 while no suite ran; indicative, not a quiet-machine number) | 64 KiB 0.60–0.69 ms, 1 MiB 9.5–9.9 ms, 10 MiB 90–110 ms; V8 on the same input 0.32 / 5.3 / 57.5 ms (Go about 1.7–2× V8). Under load (the final review's run, load average about 5) the same benchmark gave 3.0 / 50 / 458 ms, on this commit and on Task 5's alike |
| Go | `gofmt` clean; `go vet` (also `GOOS=windows`); `go test -race ./...` green |

## Known differences from the JS gateway

None in the fixtures or the reviews' random runs. By design:

1. A tool result that runs past the step budget, or a value the engine cannot hold, is handed to JS: slower than Go,
   never a different answer.
2. JS's `ms` starts after `saveTokens`, the engine's before the plan; `ms` is not compared.

## JS quirks copied (P4d owner answer 31: fixed in both engines after G3)

- `savedTokens: 0` is written when a single unit was saved (`Math.round(0.25)`).
- Chat and translated guard lines carry no `savedTokens`; Anthropic passthrough guard lines do.
- The `[error]` test reads the raw text. `dedupe` drops a leading blank line (`a[-1] || ''`).
- A warning past the fifth that also looks like progress is counted as progress.
- `messages: [null]` with the saver on is a 502 `upstream_error` carrying V8's TypeError text.

## Owed at the final quiet-machine step

M3 and M4 with the token saver at work: `ZP_BENCH_TOOL_RESULT=65536` (each gateway call carries a 64 KiB tool result),
external tier, default guardrail policy, next to the chat and translated scenarios P4e and P4d listed. The knob was run
once at small scale to prove it works; no number from it is recorded.

## Switching it on (owner)

`gateway.shadow: ["token-saver"]` compares saver calls whatever their class's engine. Once the switch-rule record meets
the rule (1 clean day, 20 compares in which a saver shortened something), set `gateway.engines["token-saver"] = "go"`:
calls with tool results then move to go wherever their class is already on go.

# P4d: translation on the Go engine's request path (slice S5): design

Date: 2026-11-17. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4d-translate-wire`, cut from `migrate/deno-go` at
`e41e2ea` ("Merge P4e: guardrails and the budget guard on the Go engine's chat path"). P4b, P4c, P4e and stream B
(`gw/translate`, `gw/guard`) are merged. P4f (`migrate/p4f-ipc-events`: spans, the call journal, one rate-limit window)
is being implemented in parallel; the overlaps are listed at the end. Documents only: nothing is implemented here.

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md` §5 P4, row S5: "Translate (Anthropic, OpenAI,
  Gemini, routers, Ollama/local). Exit: fixtures from `translate` tests pass byte-for-byte"; §8 risk "translate or
  guardrail behaviour drift JS → Go" (mitigation: byte-for-byte fixtures from the JS, shadow diff 0); §9 (Go stdlib
  only); §11.7 (owner tests on the preview).
- Stream B plan `docs/superpowers/plans/2026-11-12-p4-lib-translate-guard.md`: the `gw/translate` API (Interfaces,
  notes 9–10), `jsv`, `translate.Env`, deviations 1–8, `ReadSSE` (`MaxSSEEvent` 16 MiB, note 15).
- P4b, P4c, P4e results and plans: the hand-off rule, the shadow and its switch rule (1 clean day, 20 compares), the
  sub-class pattern (`chat-stream`), the stream rules, the guard-before-provider order, usage lines and cost.
- P4f spec `docs/superpowers/specs/2026-11-16-p4f-ipc-events-design.md` (branch `migrate/p4f-ipc-events`): D1 (spans
  for "the translated calls of P4d"), D5 (the observer reads the four formats), D14 (the call journal wraps any served
  call), D16 (the replicated rate window), and its overlap table.
- Code at `e41e2ea`: `lib/gateway/index.js` (`mapModel`, `callChat`, `proxyChat`, `pumpChunks`, `chatCompletion`,
  `translated`, `countTokens`, `messagesApi`, `anthropicHeaders`, `anthropicPassthrough`, `responsesApi`,
  `responsesPassthrough`, `geminiApi`, `embeddings`, `route` and its error shaping, `anthropicError`, `fail`, `json`,
  `guard`, `targetsFor`, `allowedTargets`, `readJson`, `authed`), `lib/gateway/translate.js`, `lib/gateway/providers.js`
  (`PRESETS`, `tierOf`, `formatOf`, `tokenParamOf`, `takesReasoning`, `nativeResponses`, `adaptChat`, `headers`,
  `resolve`, `resolveAll`, `request`, `TIMEOUT_MS`), `lib/gateway/rtk.js saveTokens`, `lib/gateway/usage.js record`,
  `lib/gateway/shadow.js`, `lib/front/{index,push}.js`, `gw/engine/{chat,serve,stream,guard,budget,shadow,providers,
  upstream}.go`, `gw/front/{front,route}.go`, `gw/translate/*`, `scripts/fixtures/{lib,gen-chat,gen-chat-guard,
  gen-translate,gen-route}.js`, `test/contract/{formats,guardrails,harness}.js`.

All Deno work follows the `deno` and `migrate-to-deno` skills; all Go work follows `go-stack-defaults` with the
program's rule on top: **Go is standard library only**. No owner was available: every choice is under "Decisions" with
its evidence; the real questions are under "Open questions for the owner".

## What the JS gateway does today (the reference)

| # | Fact | Where |
|---|---|---|
| J1 | `/v1/messages`: `authed(req,'llm')` (401/403, then the key's rate limit 429) → `readJson` (413, 400; an empty body is `{}`) → `messagesApi`: `messages` not an array → 400 (Anthropic shape) → `model = mapModel(body.model)`, `first = resolveAll(model)[0]` → **native passthrough** when `first` is an Anthropic-format provider and `model === first.name`, else **translated**: `anthropicToOpenAI({...body, model: body.model})` (may throw a 400 `BadRequest`) → `translated()`. | `index.js` `route`, `messagesApi` |
| J2 | `translated()` → `proxyChat(format)`: model 400 → `mapModel` again → `authorize` 403 → `targetsFor` (404; price rule 402) → `allowedTargets` (budget 402) → token saver `saveTokens(body,'openai')` → per target: `guard(upstreamBody, 'openai-chat')` on the **translated chat body** with the stream-usage merge → `callChat`. A non-2xx answer: one line `{…base, status, error: mask(msg.slice(0,300))}` and `out.error(status, msg)` in the client's format. Whole: `r.json()` (`{}` when not JSON), one line with `usageOf(j)`, `json(200, out.whole(j), x-zp-dropped?)`. Stream: SSE headers (+ `x-zp-dropped`), `s.open()` first, a `ping` every 10 s (Anthropic only), chunks from `wholeToChunks` (a JSON answer) or `pumpChunks` (SSE; `c.error` throws), `s.finish()`; any throw → `s.error(mask(err.message))`; one line, status 200, `ttft` and the tokens seen. | `translated`, `proxyChat`, `pumpChunks` |
| J3 | `callChat` to an **Anthropic-format** provider: `openAIToAnthropicRequest({...rest, model: t.model})` (no `stream_options`, no `adaptChat`) to `/messages` with `x-api-key` and `anthropic-version: 2023-06-01`; a non-2xx is returned as it came; whole: `anthropicResponseToOpenAI(r.json(), body.model)` (a non-JSON 200 throws: a 599 line and a 502); stream: `readSse` → `anthropicEventsToOpenAI` → `data: <JSON>\n\n` per chunk, then `data: [DONE]\n\n`; an upstream error ends it quietly. Used by chat, messages, responses and gemini alike. | `callChat` |
| J4 | Native Anthropic passthrough: `authorize` 403 → `targetsFor` (price) → `allowedTargets` (`modelOf` = model) → token saver `saveTokens(body,'anthropic')` → `guard({...body, model: t.model}, 'anthropic')` → `/messages` with the client's `anthropic-version`/`anthropic-beta` (≤ 1000 chars, no CR/LF). Whole or non-2xx: one line (`requested` always set, tokens from `anthropicUsageToOpenAI(j.usage)` only when `j.usage`), the JSON with `model` replaced on a 2xx. Stream: each SSE event re-emitted as `event: <ev>\ndata: <data>\n\n`, `message_start` with the requested model; usage from `message_start` and `message_delta`. | `anthropicPassthrough` |
| J5 | `/v1/responses`: model 400 → `mapModel` → **native passthrough** when every target is native Responses (`openai`/`azure-openai` presets, or `responses: true`) → per target `guard({...body, model: t.model}, 'responses')` → `/responses` unchanged; whole: `model` replaced; stream: events with `response.model` replaced, the `event:` line left out when the event is `message`; usage `responsesUsage`. Else **translated** (`responsesToOpenAI`, custom tool names). No token saver on the native path. | `responsesApi`, `responsesPassthrough` |
| J6 | Gemini: `POST /v1beta/models/<m>:(generateContent|streamGenerateContent|countTokens)`; `model = decodeURIComponent(m)` (greedy `(.+)`); `countTokens` → `{ totalTokens: estimateTokens(body.contents || body.generateContentRequest || body) }`, no provider call, no line; else `geminiToOpenAI(body, model, stream)` → `translated()`. `GET /v1beta/models` lists models from the model cache. | `route`, `geminiApi` |
| J7 | `count_tokens`: the estimate unless the mapped model's first target is a native Anthropic provider the key may use; then `/messages/count_tokens` with the picked fields, `anthropicHeaders`, a 15 s timeout, `guard(…, 'anthropic')` first (block or failure → the estimate); a finite `input_tokens` answer is returned re-serialized, anything else is the estimate. Never a usage line. | `countTokens` |
| J8 | `/v1/embeddings`: `authorize` 403 → first of `allowedTargets(targetsFor(model))` (`modelOf` = `body.model`) → `guard({...body, model: target.model}, 'embeddings')` → `/embeddings`; one line `{keyId, provider, model: body.model, kind: 'embeddings', client?, local?, ms, status, input, output: 0, guard?}` (no `format`, no `stream`, **no `error`** on a non-2xx); headers without `cache-control`; body unchanged on a 2xx, masked otherwise. A thrown fetch writes **no** line (the route's 502). No abort on client close. | `embeddings` |
| J9 | Refusal shapes by the path's format: `/v1/messages*` → `anthropicError` (`{type:'error', error:{type by status, message, code?}}`, `retry-after` on 429); `/v1beta/*` → `{error:{code, message, status: 401/403/429 or INVALID_ARGUMENT, details?: ErrorInfo}}` (a 502 too is `INVALID_ARGUMENT`; no `retry-after`); others → `fail` (OpenAI shape, `retry-after` on 429, `www-authenticate` on 401). A translated provider error uses `out.error` instead (Gemini: 404 `NOT_FOUND`, else `UNAVAILABLE`). | `route` catch, `fail`, `geminiApi` |
| J10 | `mapModel(name)`: the name itself when it is a route or `providers.resolve` finds it; else the first `modelMap` entry whose `match` allows it; else `<p.id>/<n>` of the first usable provider whose **cached model list** holds it (`modelCache` from `/models`, else `p.models`); else `defaults.codeModel || defaults.model || n`. The model cache lives only in the control plane. | `mapModel`, `cachedModels` |
| J11 | Routes (`router/<name>`): `resolveAll` rotates a round-robin counter and moves targets **cooling down** after a 429/503/529 (from provider headers, `noteResponse`) to the back; both are in-memory JS state. | `providers.js` |
| J12 | Usage cost: `cost(routedTo || model, tokens)` at list price; `requested` when the mapped model differs (`String(body.model).slice(0,80)`); Ollama and other local presets are `tier: private`. | `usage.js record` |
| J13 | `adaptChat` (OpenAI targets only): `max_tokens` → `max_completion_tokens` by `tokenParamOf`; a `reasoning_effort` the translation added (`body[SOFT]`) is dropped for a model that does not take it (`takesReasoning`: `reasoning` flag, `o*`/`gpt-5`/`gpt-oss`, Gemini 2.5+ on the `gemini` preset) and `reasoning` is pushed onto `body[DROPPED]`, which `x-zp-dropped` reports. | `providers.js adaptChat` |
| J14 | `translate.js` draws ids from `crypto.randomBytes(12)` (`msg_`, `toolu_`, `chatcmpl-`, `resp_`, `fc_`, `call_`, `ctc_`) and times from `Date.now()`; the Go port takes both from `translate.Env`. | `rid`, `translate.Env` |
| J15 | The go engine today hands off every request of these classes (the front forwards `messages`, `responses`, `gemini`, `embeddings` unread) and chat calls to an Anthropic-format provider ("provider: not the OpenAI format (P4d)"). | `gw/front/front.go`, `gw/engine/chat.go` |

## Decisions

Each one line, with the evidence that settled it.

1. **Scope: four path classes and one chat leg move.** `/v1/messages` (with `count_tokens`), `/v1/responses`,
   `/v1beta/models/<m>:{generateContent,streamGenerateContent,countTokens}`, `/v1/embeddings`, and chat calls to an
   Anthropic-format provider. `GET /v1beta/models` and `GET /v1/models` stay on js: both list the model cache (J6, J10),
   which the gateway does not have (owner question 2).
2. **Eleven new sub-classes, each with its own engine entry, shadow entry and switch-rule record**, as `chat-stream`
   (P4c): `messages`, `messages-stream`, `messages-count`, `responses`, `responses-stream`, `gemini`, `gemini-stream`,
   `gemini-count`, `embeddings`, `chat-anthropic`, `chat-anthropic-stream`. Default `js`. The owner's switch rule
   (1 clean day, 20 compares, `lib/front/shadow-log.js gate`) applies to each. Evidence: the rule is per class
   (P4b results, difference 1); adding translated calls to the already-switched `chat` record would bypass the gate,
   so the Anthropic leg of chat gets records of its own. A native passthrough is served in its client sub-class (the
   shadow compares it there): the client format is the user-visible contract.
3. **Switch order for the owner**: `messages`/`messages-stream` (Claude Code; also proves the Anthropic leg both
   ways) → `chat-anthropic`/`chat-anthropic-stream` → `responses`/`responses-stream` (Codex) →
   `gemini`/`gemini-stream` → `messages-count`, `gemini-count` → `embeddings`. Each moves once its own record meets the
   rule; the order only says which to turn on first.
4. **Implementation order, riskiest first**: (1) the route-level fixture generator, (2) the Anthropic provider leg,
   (3) translated whole answers, (4) translated streams, (5) native passthroughs, (6) count and embeddings, (7) the
   shadow, (8) the front's classes and `Serve` dispatch, (9) the contract suite, (10) results. Byte equality across
   two translations and streams is the risk the program's §8 names; the `Serve` wiring is last because it is where
   P4f changes `serve.go` (re-checked once P4f merges).
5. **The proof is route-level fixtures from the real JS**, on top of stream B's function fixtures
   (`gw/testdata/translate`, unchanged): `scripts/fixtures/gen-xlate.js` runs `createGateway` in process (as
   `gen-chat.js`) for every served path and writes `gw/testdata/xlate/<n>-<name>.json` with the provider request(s),
   the client's status, headers and bytes, and the usage line. The Go test plans and answers each case from the pushed
   config and must give the same bytes. This is S5's exit ("fixtures from translate tests pass byte-for-byte") at the
   level the client sees.
6. **Determinism by `translate.Env`**: the generator uses `scripts/fixtures/lib.js` (`crypto.randomBytes` gives a
   counter reset per case, `Date.now()` is 1767225600000); the Go test gives the engine an `Env` with the same rules.
   In production the engine uses `translate.Default`; every plan carries its `Env` so the shadow can replay a tape
   (Decision 21).
7. **Translated bodies cross into the engine as bytes**: `jsv.Stringify(translated)` then `engine.Parse`, which is
   `JSON.parse(JSON.stringify(x))`: `undefined` members dropped, `undefined` array items `null`, as the JS request
   finally reads on the wire. From there the P4b/P4e chat pipeline runs unchanged (stream-usage merge, `State.Guard`,
   `finishBody`). The client's body is parsed by `engine.Parse` (duplicate keys, depth, lone surrogates as P4b) and
   handed to `gw/translate` through `toJSV`. Answers going back are parsed with `jsv.Parse` and written with
   `jsv.Stringify`. One conversion each way; no second JSON model.
8. **`mapModel` ported up to `modelMap`**: the name itself when it is a route or resolves (JS `resolve`: alias of a
   usable provider, or `<id>/…` of a usable provider), else the first `modelMap` entry whose `match` allows it
   (`Allows`, the P4b port). When neither decides, the cache and `defaults` would: the engine hands off ("model:
   mapping needs the model list"). Exact by construction; owner question 2 asks whether to push the cache.
9. **Routes (`router/<name>`) stay handed off in every class.** Their round-robin counter and cooldowns are JS memory
   fed by provider headers (J11); the engine reads no quota headers (P4b follow-up: "provider quota readings: G3").
   A second, Go-side rotation would diverge from JS on every call. Owner question 3.
10. **Router presets are ordinary providers and are served**: OpenRouter, Requesty, Portkey, Vercel AI Gateway (OpenAI
    format, external), 9router and LiteLLM (local, `forwards`: `ScopeTier` counts them external for the guardrails,
    unchanged since P4e).
11. **Ollama and local presets**: private tier (no check under scope `external`, free under the price rule), a model
    ending in `cloud` is external (`ScopeTier`), no credential, `tokenParamOf`, the `reasoning_effort` soft drop
    (`takesReasoning`, J13: ported into `AdaptChat` with the plan's `Soft` and `Dropped`, which `x-zp-dropped` reads),
    stream usage asked, and a JSON answer to a stream request goes through `WholeToChunks` (J2). No preset special case
    beyond what `providers.js` has.
12. **Both native passthroughs are served**, in their client sub-class (J4, J5): an Anthropic provider whose target
    name is the mapped model; a Responses-native target. Routes are handed off (Decision 9), so a passthrough has one
    target.
13. **Guardrails run where JS runs them, on the form JS checks**: translated calls on the translated chat body with
    the stream-usage merge, format `openai-chat` (J2); Anthropic passthrough on `{...body, model: t.model}`, format
    `anthropic` (J4); Responses passthrough the same with `responses` (J5); embeddings with `embeddings` (J8);
    `count_tokens` on the picked fields with `anthropic`, where a block or failure answers the estimate and writes no
    line (J7). Order on every path: rate limit 429 → price rule 402 → budget 402 → guardrails 400/500 → provider (P4e
    Decision 3). `State.Guard` gains the plan's format and its provider-body builder; nothing else in P4e changes.
14. **Refusals**: the engine answers only what it answers today (429, 402, guard 400/500, provider answers, the 502 of
    a failed call), each in the client's shape (J9: OpenAI `fail`, `anthropicError`, Google error, or the translated
    `out.error` for a provider's non-2xx). Every other refusal (400 shape and translation errors, 403, 404, 413) is
    handed off; JS answers it as today. Evidence: these refusals precede any provider call, so a hand-off is exact,
    and porting them adds byte surface without load.
15. **Usage and cost per format** (all from fixtures; cost `Pricer.Cost(routedTo || model)`, nano-USD):

    | Path | `format` | Tokens from | `requested` | Notes |
    |---|---|---|---|---|
    | messages, translated | `anthropic` | `usageOf(j)` / last chunk usage | when mapped | 2xx stream: status 200 even after an error event |
    | messages, passthrough | `anthropic` | `anthropicUsageToOpenAI(j.usage)` / `message_start` + `message_delta` | always | no tokens when `j.usage` is missing |
    | responses, translated | `responses` | as messages translated | when mapped | |
    | responses, passthrough | `responses` | `responsesUsage(j.usage)` / `response.usage` in events | when mapped | `routedTo` when it differs |
    | gemini | `gemini` | as messages translated | when mapped | model from the path |
    | chat → Anthropic | `openai` | `anthropicUsageToOpenAI` via the translated answer | – | as P4b/P4c otherwise |
    | embeddings | – | `usage.prompt_tokens`, output 0 | – | no `format`, `stream`, `error` |
    | count (both) | – | – | – | no line |

16. **Token saver: handed off where it would act**, as P4b: translated calls when the translated chat body holds a
    `role: "tool"` message; Anthropic passthrough when a message holds a `tool_result` block; the Responses passthrough,
    embeddings and count have none (J4, J5, J7, J8; `rtk.js saveTokens` returns null without such content, so no
    `savedTokens`). Owner question 1 (most Claude Code turns carry tool results).
17. **OAuth2 and service accounts stay handed off (P4g)** through one helper, `credential(s, t)`, used by every new
    plan, so P4g changes one place. The Anthropic leg's credential goes in `x-api-key` (J3).
18. **Tools and MCP stay on their path (P4g/S9)**: `/mcp` is untouched; tool definitions and tool calls inside a
    request are translated as fields; Anthropic server tools are dropped and named in `x-zp-dropped` as JS does.
19. **Streams follow P4c**: flush per provider read, synchronous writes, no body timeout, one usage line however the
    stream ends; translated streams write the opening event first, Anthropic pings every 10 s (a ticker, writes under
    one mutex), error events with the masked message; the `chat-anthropic-stream` leg converts Anthropic events to the
    synthesized OpenAI text (`data: …\n\n`, then `data: [DONE]\n\n`) and feeds the existing `ChatStream`.
20. **Decoding**: provider text on the new paths is decoded as WHATWG does (P4c's `validText`, one U+FFFD per maximal
    subpart). P4b's whole-chat `decodeUTF8` difference stays as recorded (out of scope).
21. **Shadow**: the JS capture grows for every P4d class: a tee in `providers.request` keeps each provider answer's
    status, content type and decoded text (4 MiB cap, `ended`, `truncated`); `withShadow` keeps the client's bytes
    (same cap); the last usage line of the request; and a **tape** of the random bytes and clock reads `translate.js`
    made. The engine replays the provider text through the same plan with an `Env` that reads the tape, and compares
    the provider request(s), the client status, the content type and `x-zp-dropped`, the client bytes (Anthropic
    `ping` events removed from both: they are timing) and the usage fields. A count estimate compares the client
    body only; a budget lag and a rate-limit answer are `skipped` as in P4e.
22. **No IPC change.** `config.push` already carries what the engine reads (`format`, `responses`, `reasoning`,
    `tokenParam`, `models`, `aliases`, `modelMap`, `defaults`, `routes`, the guard policy); the ipc/v1 schemas stay at
    their count.
23. **The front classifies percent-escaped paths** that WHATWG leaves unchanged: a well-formed `%XX` no longer makes a
    path non-canonical, unless the segment is a dot segment once decoded (`%2e`), proven by new `gen-route.js` cases;
    the Gemini model is `decodeURIComponent` of the segment (a malformed escape is handed off). Evidence: Gemini
    clients send `provider/model` as `provider%2Fmodel` (`test/contract/formats.test.js gemini()`); without this every
    such call stays on js.
24. **M3/M4 are owed, not measured.** The owner deferred every quiet-machine benchmark to the program's final step:
    the results doc adds a translated scenario (`messages-stream` at 500 streams; `messages` whole for the overhead)
    to the owed list, and the bench gains a `ZP_BENCH_FORMAT` knob so that step can run it. A Go test bounds the memory
    of one held translated stream at 80 KiB (P4c Decision 19's budget).
25. **P4f's hooks are used, not duplicated**: every new serve path goes through P4f's call journal (`open`, the
    journalled `emitLine`), its answer/span helper and its rate-limit `Take`; the steps touching `gw/engine/serve.go`,
    `gw/engine/chat.go` (`State`), `lib/front/index.js`, `scripts/fixtures/lib.js` users and `test/contract/harness.js`
    are marked "re-check after P4f merges".
26. **Known differences, recorded, not fixed**: (a) a non-JSON 2xx from an Anthropic provider writes a 599 line and a
    502 whose text is V8's `SyntaxError` message, which Go does not reproduce (it writes its own); (b) a provider that
    cuts a translated stream: JS writes the runtime's error text (`terminated` on Node; Deno's differs), Go writes
    `terminated`; (c) an SSE event over 16 MiB ends a translated stream with an error event (`ReadSSE`, note 15); JS
    has no limit; (d) stream B deviations 4, 5 and 8 (invalid UTF-8 inside `ReadSSE`, lone surrogates, non-V8
    `TypeError` text) apply. The shadow reports each as a diff; none is in the fixtures.
27. **JS quirks are copied, not fixed** (owner question 4): a Gemini 502 shaped `INVALID_ARGUMENT`; the embeddings
    line without `error` on a non-2xx and without any line on a thrown fetch; the Anthropic passthrough's `requested`
    on every line. Equality first; a fix goes into both engines later.

## Design

### D1. Planning (`gw/engine/xlate.go`, `mapmodel.go`, `anthropic.go`, `passthrough.go`, `count.go`, `embeddings.go`)

One planner per path, each pure (no I/O, no clock beyond `now`), each returning a `Plan`:

```
PlanMessages(r, now)   → passthrough plan | translated plan (format anthropic) | hand-off
PlanResponses(r, now)  → passthrough plan | translated plan (format responses) | hand-off
PlanGemini(r, now)     → translated plan (format gemini) | count answer | hand-off
PlanCount(r, now)      → count plan (provider) | count answer (estimate) | hand-off
PlanEmbeddings(r, now) → embeddings plan | hand-off
PlanChat(r, now)       → as today; an Anthropic-format target now plans the Anthropic leg
```

They share `admit` (the access, method, key, body and `Authorize` checks of `PlanChat`, factored out unchanged) and
`target(model, key)` (route hand-off, `Resolve`, private-only, `credential`). A translated plan holds the translated
chat body as `pending` (Decision 7), `Soft`/`Dropped`/`Custom`, `ClientModel` (the name the client used), the client
`Format`, the `Shape` of refusals, the guard format and a `build` function for the provider body:

- OpenAI target: `finishBody` (model, `AdaptChat` with `Soft`/`Dropped`, `Stringify`);
- Anthropic target: drop `stream_options`, `model: t.model`, `OpenAIToAnthropicRequest`, `jsv.Stringify`; URL
  `<baseUrl>/messages`, `x-api-key`, `anthropic-version: 2023-06-01`.

### D2. Answers (`gw/engine/xlate_answer.go`)

`State.RespondTranslated(p, status, raw, ms)`: a non-2xx → `out.error` shape and the line with the 300-unit masked
message; a 2xx → for an Anthropic target first `AnthropicResponseToOpenAI` (a parse error is the 599/502 path), then
`OpenAIToAnthropic` / `OpenAIToResponses` / `OpenAIToGemini` with the plan's `Env`, the line with `usageOf`, headers
with `x-zp-dropped`. A `TypeError` out of the translation (J2: `out.whole` throwing) is the JS 502 with V8's message
(`jsv` reproduces it for reads on `null`/`undefined`), after the 200 line, as JS records it.

### D3. Streams (`gw/engine/xlate_stream.go`)

`TranslatedStream` holds the client-format writer (`translate.AnthropicStream`, `ResponsesStream`, `GeminiStream`),
the upstream reader (OpenAI SSE via `ReadSSE`, Anthropic SSE via `ReadSSE` + `AnthropicEvents`, or a whole JSON via
`WholeToChunks`), `ttft`, the last usage and the error text. `serveTranslatedStream` writes headers, `Open()`, starts
the ping ticker for Anthropic, pushes each chunk, writes `Finish()` or `Error(mask(msg))`, and emits one line.

### D4. Passthroughs, count, embeddings

`anthropicPassthrough` and `responsesPassthrough` rewrite the model in the whole answer and in the
`message_start`/`response.*` events exactly as J4/J5 (an event's `data` is passed byte-for-byte unless its JSON is
rewritten). `count` answers the estimate itself or calls `/messages/count_tokens` with a 15 s limit and falls back to
the estimate. `embeddings` passes the provider's bytes through.

### D5. Guard (`gw/engine/guard.go`)

`Plan.GuardFormat` (default `openai-chat`) and `Plan.build` replace the two constants in `check` and `Guard`;
`refusal` takes the plan's `Shape`. A count plan whose check blocks or fails answers the estimate (no line).

### D6. Serving (`gw/engine/serve.go`, `gw/engine/dispatch.go`; re-check after P4f merges)

`Serve` dispatches by class to `serveChat` (today's body), `serveMessages`, `serveResponses`, `serveGemini`,
`serveEmbeddings`; each runs P4e's sequence (`Take` → `Budget` → `Guard` → provider) and P4f's journal/span helpers.
`subOf` becomes `SubClass(class, plan, req)`.

### D7. Front (`gw/front/front.go`, `route.go`; `lib/front/index.js`)

`GoClasses` and `GO_CLASSES` list the eleven sub-classes; each path class has its sub-class list (`chatSubs`
generalized to `subs[class]`); `Canonical` accepts well-formed `%XX` (Decision 23).

### D8. Shadow (`lib/gateway/shadow.js`, `providers.js`, `translate.js`, `index.js`; `gw/engine/shadow.go`)

JS: `shadow.teeProvider(r)` in `providers.request`; `withShadow` wraps the response's `write`/`end` when capturing;
`usage.record` notes each line of a captured request; `translate.js rid` and clock reads go through
`shadow.tapeRandom(bytes)` / `shadow.tapeNow(ms)` (no-ops outside a capture). The P4c `noteAnswer`/`noteStream` path of
`chat`/`chat-stream` is unchanged. Go: `Capture` gains `upstream`, `tape`; `CompareTranslated` replays and compares.

## Tests and fixtures

| Level | What | Where |
|---|---|---|
| Fixtures (JS truth) | every served path: whole and stream, OpenAI/Anthropic/Responses-native/local/router-preset targets, double translation, tools, thinking, dropped features, provider non-2xx, provider throw, JSON answer to a stream, mid-stream `error` chunk, provider cut, `out.whole` TypeError, guard redact/log/block per format, budget and price 402 per shape, count native/estimate/fallback, embeddings 2xx/non-2xx, Gemini `%2F` model | `scripts/fixtures/gen-xlate.js`, `gw/testdata/xlate/`, `gw/engine/xlate_test.go` |
| Fixtures | `%XX` paths, `%2e` dot segments | `scripts/fixtures/gen-route.js`, `gw/front/route_test.go` |
| Go unit | hand-off list per planner, `MapModel`, `AdaptChat` soft drop, ping ticker (fake interval), one line per stream end, memory per held translated stream ≤ 80 KiB | `gw/engine/*_test.go` |
| Shadow | tape replay, ping removal, count, refusal shapes, a deliberate one-byte change is a diff | `gw/engine/shadow_xlate_test.go`, `test/front-shadow.test.js` |
| Contract | `formats.test.js`, `guardrails.test.js`, `budgets.test.js` format cases served by go under `ZP_ENGINE=go`; `ZP_ENGINE=shadow` ends with diff 0 | `test/contract/` |

## Exit criteria (P4d, slice S5)

- Every `gw/testdata/xlate` fixture passes byte for byte (provider request, client status/headers/bytes, usage line),
  and the stream B fixtures (`gw/testdata/translate`) stay green; the generator gives the same bytes on Node and Deno
  under the fixtures job's flags.
- The contract format, guardrail and budget cases of the eleven sub-classes are served by the go engine under
  `ZP_ENGINE=go`; `ZP_ENGINE=shadow` ends with diff 0.
- The full CI matrix is green; `gofmt`, `go vet` (also `GOOS=windows`), `go test -race` clean.
- The M3/M4 translated scenario is recorded as owed (Decision 24).

## Out of scope

- Token saver in Go (owner question 1), the model cache and `/v1/models` (question 2), routes (question 3).
- OAuth2 and service accounts (P4g); `/mcp` (S9); spans, journal and the replicated rate window (P4f).
- Model-output guardrails (owner answer 26 of P4e: input only).

## Open questions for the owner

1. **Token saver in Go.** It is on by default and acts on every request with tool results, which is nearly every
   Claude Code and Codex turn after the first; P4d hands those to JS, so most coding-agent traffic stays on js until
   `rtk.js` (313 lines of text filters) is ported. Default: a follow-up slice before G3, proven by fixtures as here.
2. **The model cache.** `mapModel`'s fallback and both model lists read a cache only the control plane has. Push it
   (per provider, on change) so the engine can map every name and serve `GET /v1/models` and `/v1beta/models`?
   Default: hand off in P4d; decide on the shadow's hand-off counts ("model: mapping needs the model list").
3. **Routes on go.** A route needs its round-robin counter and the cooldowns from provider headers in one place. Make
   the engine the owner for routes it serves (and send cooldowns to JS), replicate both ways as P4f does for the rate
   window, or leave routes on js until G3? Default: on js until G3, with the quota readings.
4. **JS quirks copied** (Decision 27): keep copying for equality and fix them in both engines after G3? Default: yes.

## Files that overlap other P4 branches

- **P4f (in progress, `migrate/p4f-ipc-events`)**: `gw/engine/serve.go` (journal open/close, `emitLine` with the call
  id, span emission, `Take`'s replication hook), `gw/engine/chat.go` (`State.spans`), `gw/ipc/messages.go`
  (`ProviderConfig.Span`, `ConfigPush.Trace`), `gw/cmd/zaafpulse-gw/main.go`, `lib/front/index.js` (`routing`'s OTLP
  note), `lib/front/push.js` (provider `span`; it changes every pushed config, so `gw/testdata/xlate` is regenerated
  after the merge), `scripts/fixtures/lib.js`, `test/contract/harness.js`, `test/front-shadow.test.js`. P4d touches
  `serve.go` only for the dispatch (Task 8) and puts the new serve paths in new files.
- **P4g (provider auth wiring)**: the `credential` helper (Decision 17) and `Upstream` headers; the 401
  refresh-and-replay of `providers.request`; the Vertex and Azure presets. `/mcp` (S9) is not touched.

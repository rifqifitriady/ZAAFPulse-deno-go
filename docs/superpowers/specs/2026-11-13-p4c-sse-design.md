# P4c: streaming chat on the Go engine (slice S3): design

Date: 2026-11-13. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p4c-sse-plan`, cut from
`origin/migrate/p4b-passthrough` at `9f6b004` ("CI: the contract suite with chat shadowed by and served by the go
engine ..."). P4b is **not merged**: it is still taking final-review fixes on its branch. Every part of this design that
leans on P4b code names the file, and the plan marks those steps "re-check against the merged P4b".

Inputs:
- Program plan `docs/migration/2026-11-10-deno-go-migration-plan.md`: §4 metrics M3 and M4, §5 P4 slice S3 ("M4 met at
  500 streams"), §9 dependency policy (Go stdlib only), §11 owner decisions.
- The P4 split in `docs/superpowers/specs/2026-11-12-p4a-go-front-design.md` (P4c = S3) and its owner decision 2
  (shadow without calling the provider twice).
- The stage-1 spec `docs/superpowers/specs/2026-11-12-p4-stage1-design.md` (stage 2: P4c and the wiring of P4d–P4g).
- P4b: plan `docs/superpowers/plans/2026-11-12-p4b-passthrough.md` (Decisions 1–16, owner answers 1–3, spec deviations
  1–12) and results `docs/migration/2026-11-12-p4b-results.md` (known minors m1, m5'–m8', follow-up "streaming chat:
  P4c").
- P4a results `docs/migration/2026-11-12-p4a-results.md` (the front's proxy overhead, the kill test).
- Code at `9f6b004`: `lib/gateway/index.js` (`chatCompletion`, `proxyChat`, `agentCompletion`, the format streams),
  `lib/gateway/providers.js` (`streamUsage`, `TIMEOUT_MS`), `lib/gateway/shadow.js`, `lib/front/index.js`,
  `lib/front/shadow-log.js`, `gw/engine/{serve,chat,upstream,shadow,providers,jsjson}.go`, `gw/front/front.go`,
  `gw/translate/stream.go`, `test/contract/{formats,usage,harness,upstream}.js`, `bench/{run,mock-provider}.js`.

All Deno work follows the `deno` and `migrate-to-deno` skills; all Go work follows `go-stack-defaults` (stdlib first; the
program's §9 rule makes it stdlib only). No owner was available while this was written: every choice is under
"Decisions" with its evidence, and the few real questions are under "Open questions for the owner".

## What the JS gateway does today (the reference)

Read from `lib/gateway/index.js` at `9f6b004`. These facts fix the behaviour the Go engine must reproduce.

| # | Fact | Where |
|---|---|---|
| J1 | `chatCompletion` streams when `body.stream` is truthy and the provider answered 2xx with a body; any other answer (a 4xx/5xx, no body) goes through the whole-answer branch, with `stream: true` in the usage line. | `chatCompletion`, `if (!body.stream \|\| !r.ok \|\| !r.body)` |
| J2 | `proxyChat` asks the provider for usage: when `providers.streamUsage(p)` (the preset does not say `streamUsage: false` and the format is not `anthropic`), the upstream body is `{ ...body, stream_options: { ...(body.stream_options \|\| {}), include_usage: true } }`. The `anthropic-openai` preset says `streamUsage: false`. | `proxyChat`; `providers.js:641`, preset table line 67 |
| J3 | The OpenAI-format stream is **not** parsed with `readSse`: it is split on `\n` and each line is rewritten by `line()`: a trailing `\r` is dropped; a line that does not start with `data:` (comments such as `: keep-alive`, blank lines, `event:` lines) passes as `<line>\n`; a `data:` line whose JSON does not parse, or is not an object or array, passes as is; otherwise the chunk is re-serialized as `data: <JSON.stringify(c)>\n` with `c.model` replaced by the requested name when it is a string. | `chatCompletion` `line` |
| J4 | Without `stream_options.include_usage` from the client, a usage-only chunk (`choices` an empty array, `usage` truthy, no `error`) is dropped together with the blank line after it, and `"usage": null` is deleted from the other chunks. With it, everything passes. | `line`, `skipBlank` |
| J5 | Usage is the last chunk with a truthy `usage` (`usageOf`: prompt, completion, cached tokens, `\|\| 0`). Time to first token is taken at the first chunk whose `choices[0].delta` has a truthy `content` or `tool_calls`. | `line` |
| J6 | Bytes are written once per provider read (`out` of all complete lines of that read); a final partial line is written with a `\n` added, **only** when the provider ended cleanly. | the read loop, `if (buf) res.write(line(buf) \|\| '')` |
| J7 | Headers: `200`, `content-type: text/event-stream`, `cache-control: no-store`, `connection: keep-alive`, the CORS set. Through the P4a front, `ReverseProxy` removes `Connection` and `Keep-Alive` (hop-by-hop), so a client of the product in front mode sees no `Connection` header. | `res.writeHead`; `gw/front/front.go` (P4a D2) |
| J8 | **No heartbeat** on the chat pass-through. Only `agentCompletion` (`: ping` every 10 s) and the translated format streams (`s.ping`, Anthropic `/v1/messages` clients) send their own. A provider's own comments pass through (J3). | `agentCompletion`, the format stream branch |
| J9 | **No idle timeout on the body.** The provider has 120 s to start its answer (`TIMEOUT_MS`, cleared once `fetch` resolves); the body read has no timer. The server sets no `requestTimeout` on responses. | `providers.js:508`; P4b fact F5 |
| J10 | **No backpressure.** `res.write` ignores its return value: a slow client makes Node buffer without limit. | the read loop |
| J11 | **One usage line per stream, always.** Client gone (`abortOnClose` aborts the provider fetch) or provider cut (the read throws or ends): the loop ends in `catch`, `res.end()`, then `usage.record({ ...base, status: 200, ms, ttft, ...u })` with the tokens seen so far (usually 0: the usage chunk comes last). The line carries no `error`. | `chatCompletion` tail |
| J12 | A stream the shadow marks today gets a `handoff` line ("stream: P4c"): `PlanChat` hands every streaming request off. | `gw/engine/chat.go PlanChat` |

## Decisions

Each one line, with the evidence that settled it.

1. **First streaming class: `POST /v1/chat/completions` with a truthy `stream`, to one OpenAI-format provider, passed
   through without translation: exactly the P4b serving set (loopback client and Host, plain or no key, no guardrail
   check in scope, no blocking budget, no key budget, no route, no tool results for the token saver) plus `stream`.**
   Evidence: P4b serves exactly that set non-streaming (`PlanChat`); J3 shows the OpenAI stream is a line rewrite with no
   translation; the translated streams (`AnthropicStream`, `ResponsesStream`, `GeminiStream`, `WholeToChunks`) need
   the P4d wiring.
2. **Streaming gets its own switch: a sub-class `chat-stream`, decided by the engine after it reads the body, with its
   own engine entry (`gateway.engines["chat-stream"]`, default `js`), its own shadow entry (`gateway.shadow`
   `["chat-stream"]`) and its own record under the owner's switch rule (1 clean day, 20 compares).** Evidence: P4b
   Decision 10 enforces the switch rule in code per class; without a sub-class, a `chat` already on `go` would start
   serving streams the day P4c merges, with no shadow record of a single stream.
3. **The front hands every `chat` request to the go engine when either sub-class is on `go` or shadowed; the engine
   reads the body, picks `chat` or `chat-stream`, and serves, shadows or forwards per that sub-class.** Evidence:
   `gw/front/front.go ServeHTTP` routes by path only; the stream flag is in the body, which only the engine reads
   (`ReadRequest`, re-attached for a hand-off).
4. **The rewriter is a port of J3–J6, not `translate.ReadSSE`: `gw/engine/stream.go ChatStream` splits on `\n` bytes,
   rewrites each line with the P4b `jsjson` (`Parse`, `Stringify`), and is checked against fixtures the real
   `chatCompletion` writes (`scripts/fixtures/gen-chat.js`, new stream cases).** Evidence: `readSse` joins `data:`
   lines and drops comments, which `chatCompletion` passes through; the contract case "chat → OpenAI provider, stream"
   asserts the text starts with `: keep-alive\n\n`.
5. **Usage: the engine adds `stream_options.include_usage: true` upstream exactly as J2 (spread order kept: an existing
   `stream_options` key keeps its place, a new one goes last; inside it `include_usage` keeps its place or goes last),
   reads the last truthy `usage` as `tokens()` does, and prices it with the P4b `Pricer` on the routed model.** A
   `stream_options` that is present and neither `null` nor a plain object is handed off (JavaScript spreads a string
   into indexed keys). The preset table gains `streamUsage` (`gw/engine/providers.go Presets`, checked by the
   `00-presets` fixture). Evidence: J2, `providers.js:67` and `:641`; P4b `Respond` and `Pricer.Cost`.
6. **Exactly one usage line per served stream, written after the stream ends for any reason (provider EOF, provider
   error, client gone, write error, panic), with status 200, the tokens seen so far, `ttft` or null, and no `error`;
   the engine's emit runs once (`sync.Once`), off the client's path (a goroutine counted by the engine's wait group),
   and the gateway's shutdown waits for it before the outbox closes.** Evidence: J11; P4b deviation 5 (go lines arrive
   a few ms after the answer); `gw/cmd/zaafpulse-gw` `defer box.Close()` and `closeFront` (Shutdown does not cancel
   running handlers, so a stream's emit could otherwise race the outbox close).
7. **Client abort and provider cut-off follow J11 exactly: a client that goes away cancels the provider request (the
   request context), and the stream's line still goes out once; a provider that cuts off ends the client's response
   cleanly (no chunked-encoding abort), as the JS path does in front mode.** Evidence: J11; P4a results kill test
   ("the client sees the cut, nothing double-counted").
8. **Flush per provider read: every read that yields complete lines is written and flushed at once
   (`http.ResponseController.Flush`); headers are flushed when the provider's 2xx arrives.** Evidence: J6 (one write per
   read, Node sends it at once); P4a front `FlushInterval: -1` for js-served streams. Flushing headers before the first
   line changes only timing, not bytes.
9. **Heartbeat and timeouts match JS: no heartbeat of our own on chat (J8), 120 s for the provider to start, no timer on
   the body (J9), no write deadline toward the client, `ReadHeaderTimeout` 10 s and `IdleTimeout` 120 s on the public
   port as in P4a.** Evidence: J8, J9; `gw/engine/upstream.go Timeout`; `gw/front/front.go`.
10. **Backpressure is natural and bounded: the engine writes synchronously, so a slow client stops the read from the
    provider (TCP backpressure) instead of buffering.** This differs from J10 (Node buffers without limit) in the
    engine's favour; the bytes the client gets are the same. Evidence: J10; `net/http` `Write` blocks on a full socket.
11. **Memory per stream is budgeted and freed early: an 8 KiB read buffer per stream, the line buffer compacted after
    each read, the client request body and the provider request body released once the provider's headers are in
    (`onceBody` drops its slice at EOF; `PlanChat`'s copies are cleared), a line longer than 16 MiB ends the stream as a
    cut (JS has no limit; recorded as a difference).** Budget: at most **64 KiB of heap and stack per held stream**
    (measured by a Go test, built without `-race`), and the M4 gateway RSS target (≤ 80 MB at 500 streams).
    Evidence: coding clients send prompts of hundreds of KB (a held 500 KB request × 500 streams is 250 MB);
    `translate.MaxSSEEvent` is 16 MiB; M4.
12. **Shadow without a second provider call: while `chat-stream` is on `js` and shadowed, the control plane serves the
    stream and its capture keeps the provider's decoded text and the client's text (each capped at 4 MiB of UTF-16
    units; past that the capture is marked `truncated`) plus how the stream ended (`eof` or `cut`); the engine
    replays the captured provider text through the same `ChatStream` in one `Feed` (and `End` only after `eof`) and
    compares the provider request, the client text and the usage line.** The shadow line's class is `chat-stream`.
    Evidence: P4a owner decision 2; P4b Decision 4 (capture over the internal port, `GET /_zp/shadow/<id>`); J6 (the
    final partial line only after a clean end); `ChatStream` output does not depend on how the bytes were split (a
    property test proves it).
13. **Hand-off rules for a stream, in order: every P4b hand-off of `PlanChat`; `stream_options` present and not null or
    a plain object; `chat-stream` on `js` (forwarded, marked when shadowed and a compare slot is free).** The engine
    never hands off after it has called the provider. Evidence: P4b Decision 2 (serve exactly or hand off unchanged).
14. **M4 is measured with `bench/run.js` against the product with chat and chat-stream on `go`, the bench provider on
    the `private` tier (a new knob `ZP_BENCH_TIER`, default `external` so P0 runs stay comparable), the gateway's RSS
    sampled from its own pid (`/api/gateway/status` `pid`), and the mock counting which engine called it (by
    `User-Agent`), so a run that silently handed off cannot pass. The JS reference is re-recorded in the same session
    with the same knobs.** Targets (program plan §4, M4): **≥ 500 concurrent streams, 0 errors, overhead p95 ≤ 10 ms,
    gateway RSS ≤ 80 MB.** M3 is re-measured in the same run (target p95 ≤ 2.84 ms, which is 50% of 5.67 ms and under
    5 ms). Evidence: the bench seeds an external provider with the default guardrails (`bench/README.md`), which the
    engine hands off until P4e (P4b deviation 3); P4b results "M3 is re-measured in P4c".
15. **Windows: same code path, proven in CI, not benchmarked.** The go-mode contract job already runs on Node on Windows
    (`9f6b004`), so the stream cases (including new abort and cut cases) run there; `go test` runs on both Windows
    runners with the stream, abort and 200-stream tests; M4 stays a macOS measurement (the owner's laptop; `rssMB`
    reads `ps`, which Windows lacks). Evidence: `ci.yml` jobs `go`, `contract go engine`; `bench/run.js rssMB`.
16. **A gateway crash mid-stream loses the usage line of each in-flight go-served stream (none is ever written twice).**
    JS has the same exposure when its own process dies; a provisional line would need an amend in the ledger, which
    does not exist. Recorded as a known gap; see owner question 2. Evidence: J11; `lib/gateway/usage.js` (append-only).

## Design

### D1. Routing a chat request (front and engine)

```
client ─► front.ServeHTTP ─ class "chat" ─┬─ chat or chat-stream on go or shadowed ─► engine.Chat(w, r, rawPath, js, own)
                                          └─ otherwise ─► proxy to the control plane (as today)

engine.Chat: read body (≤ 20 MB + 1) ─► stream := Truthy(body.stream) ─► sub := "chat" | "chat-stream"
   mode[sub] == go                ─► plan; hand-off ⇒ js; else serve (whole: P4b; stream: D3)
   mode[sub] == js && shadowed    ─► P4b Shadow path with the sub-class (D5)
   otherwise                      ─► js (the buffered body re-attached)
```

`gw/front/front.go GoEngine` keeps `Serve` and `Shadow`, or folds them into one `Chat` entry (the plan picks after
re-checking the merged P4b; the behaviour above is fixed either way). The control plane's `routing()`
(`lib/front/index.js`) learns `chat-stream` as a go class that is **not** a path class: `GO_CLASSES` gains it, `CLASSES`
(path classes) does not, and the IPC enum `RouteClass` (`gw/ipc/messages.go`, `ipc/v1/config.push.schema.json` for
`front.engines` keys and `front.shadow.classes`) gains the value, with a valid and an invalid fixture. That is additive
under the `ipc/v1` version rule: both binaries ship together, and an old gateway refuses the push with exit 2, which the
supervisor already treats as `failed` (fallback to js, P4a E8).

### D2. The rewriter (`gw/engine/stream.go`)

```go
type ChatStream struct { /* model, wantsUsage, elapsed, buf, bomDone, skipBlank */ TTFT *float64; Input, Output, Cached int64; Unsupported int }
func NewChatStream(model string, wantsUsage bool, elapsed func() float64) *ChatStream
func (s *ChatStream) Feed(p []byte) ([]byte, error) // bytes for the client now; ErrStreamLine past 16 MiB
func (s *ChatStream) End() []byte                   // the final partial line, only after a clean end
```

- Lines are split on the byte `\n` (it never occurs inside a UTF-8 sequence), so a split character across reads needs
  no decoder state; each complete line is decoded with the P4b rule (`decodeUTF8` without the BOM step: invalid runs
  become one U+FFFD, P4b difference 9).
- A leading byte order mark is dropped once, at the start of the stream (TextDecoder's default), even when it arrives
  split across reads. At `End`, an incomplete but valid UTF-8 prefix at the tail is dropped (TextDecoder in stream mode
  holds it and `chatCompletion` never flushes it).
- The `data: [DONE]` test needs no code: `JSON.parse("[DONE]")` throws, and both branches of J3 pass the line as is.
- `jsBlank` is JavaScript's `trim() === ''` (WhiteSpace and LineTerminator, U+FEFF included), used for `skipBlank`.
- A chunk `jsjson.Parse` refuses as unsupported (an escaped lone surrogate, nesting deeper than 512) passes unchanged
  and is counted in `Unsupported`; JS would rewrite it. Same class of difference as P4b minor m1.

### D3. Serving a stream (`gw/engine`)

1. `PlanChat` (P4b) builds the plan; for a stream it also sets `Plan.Stream`, `Plan.WantsUsage` (client's
   `stream_options.include_usage` truthy) and the upstream body with J2's `stream_options` (Decision 5); the usage base
   has `stream: true`.
2. Rate limit as P4b (`limits.Take`); a 429 is the P4b answer.
3. `Upstream.Start(ctx, plan)` is P4b's `Call` up to the provider's headers (redirects, 120 s, error names); `Call`
   becomes `Start` plus `io.ReadAll`. The returned body's `Close` cancels the call's context.
4. Not 2xx (J1): read the whole body and answer with P4b `Respond` (usage `stream: true`), as today.
5. 2xx: release the request bodies (Decision 11); set the J7 headers without `Connection`; `WriteHeader(200)`; flush.
   Loop: read ≤ 8 KiB → `Feed` → write and flush when non-empty; stop on a write or flush error, on the provider's
   error, or on EOF (then write `End()`).
6. Close the provider body (cancels the upstream request if still open), then emit the usage line once (Decision 6).

### D4. Exactly once, abort, cut and shutdown

| Event | Client sees | Provider | Usage line |
|---|---|---|---|
| Normal end | all lines, the final partial line, end of body | read to EOF | one, status 200, tokens of the last usage chunk, `ttft` |
| Client goes away | (gone) | request cancelled through the request context | one, status 200, tokens so far (usually 0), `ttft` if seen |
| Provider cuts off or errors mid-body | the lines so far, then a clean end (no partial line) | closed | one, status 200, tokens so far |
| Provider answers non-2xx | P4b whole answer | read whole | one, P4b `Respond` line with `stream: true` |
| Call fails before headers | P4b 502 | none | one, P4b `Failed` line (599) with `stream: true` |
| Gateway shutdown (stdin closed, `shutdown`) | the stream is cut after the grace | cancelled | one per stream, appended before the outbox closes |
| Gateway killed | cut | dropped by the OS | **none** (Decision 16) |

Shutdown order (`gw/cmd/zaafpulse-gw`): `front.Shutdown(grace)`; if the grace ends with streams still open, `Close()`
(cancels their contexts); `engine.Wait(2 s)` for the pending emits; then the outbox closes.

### D5. Shadow for streams

- **Control plane** (`lib/gateway/index.js chatCompletion` stream branch, `lib/gateway/shadow.js`): only when
  `shadow.capturing()` (an `AsyncLocalStorage` store exists for a request Go marked): keep `providerText` (each decoded
  read) and `clientText` (each write), each capped at 4 MiB of UTF-16 units (past it: `truncated: true`, stop
  appending), and `ended: 'eof' | 'cut'`; after `usage.record` (its return value kept), call
  `shadow.noteStream({ status, body: providerText, ended, truncated }, { status: 200, body: clientText }, entry)`.
  `providerText` is the provider's bytes as JS decoded them, so invalid UTF-8 is already replaced there and the shadow
  does not exercise P4b difference 9 (the fixtures do).
  The capture keeps the P4b limits (TTL 60 s after the stream ends, 1,000 captures, 64 MiB in total).
- **Engine** (`gw/engine/shadow.go`): for `chat-stream`, `Compare` replays `ProviderResponse.Body` through a fresh
  `ChatStream` (one `Feed`, `End` only when `ended == "eof"`), builds the usage line as D3 does, and compares the
  provider request (URL, headers with the credential hashed, body), the client text, and the usage fields P4b compares
  (not `ms`, `ttft`, `ts`). A truncated capture is `skipped` ("capture over its limit"). A stream that ended with
  `cut` (the client left or the provider broke off) is compared like any other: `clientText` holds every write the
  JS path made, and the replay without `End` gives the same text. The P4b slot limit (8 compares at once) holds a slot
  for the whole stream.
- The reverse shadow (JS building from Go's provider stream) stays out of scope, as in P4b deviation 2.

### D6. Measurement (M4, and M3 again)

`bench/run.js` gains:
- `ZP_BENCH_TIER` (`external` default, `private` for the go engine), recorded under `config`;
- the gateway's pid from `GET /api/gateway/status` (`pid`), and `gatewayPeakRssMB` / `gatewayIdleRssMB` sampled from
  it every 250 ms during the top level, next to the server's;
- the mock provider counts calls by `User-Agent` family (`go` for `Go-http-client/*`, `js` otherwise) and the result
  records `servedBy` per step; a go run fails when any stream step has a `js` call.

Run on the owner's MacBook (Apple M4) on power, other apps closed, `ulimit -n 8192`, three runs in one session:
`ZP_BENCH_TIER=private node bench/run.js` (js mode), `ZP_ENGINE=front ZP_BENCH_TIER=private node bench/run.js`, and
`ZP_ENGINE=go ZP_BENCH_TIER=private node bench/run.js`. The go run must meet M4 (≥ 500 streams, 0 errors, overhead p95
≤ 10 ms, gateway peak RSS ≤ 80 MB) and is the M3 re-measure (non-streaming overhead p95 ≤ 2.84 ms).

If the go run misses a target, the levers, in order, are: the read buffer (8 → 4 KiB), `GOGC`, a memory limit
(`debug.SetMemoryLimit`), and the upstream transport's per-connection buffers (`ReadBufferSize`, `WriteBufferSize`); each
change is re-measured, and the result doc records which were used.

### D7. Windows

- Client-gone detection: `net/http` cancels the request context when the client's connection closes; on Windows the
  close surfaces as `WSAECONNRESET` (10054) or EOF, both of which end the background read. A Go test closes the client
  mid-stream and asserts one line, on both Windows runners.
- The stream line carries no `error` text (J11), so the Winsock names of P4b (Decision 9) are only used before the
  provider's headers, as today.
- No file-descriptor limit to raise (Go raises `RLIMIT_NOFILE` itself on Unix since 1.19; Windows has none of that
  kind). The 200-stream Go test runs on every OS; the 500-stream benchmark is macOS only.
- Timer resolution (about 15.6 ms on Windows by default) makes `ms` and `ttft` coarser; neither is compared.

## Testing

- **Fixtures:** new stream cases in `scripts/fixtures/gen-chat.js` (the real `chatCompletion` with a provider whose body
  is a `ReadableStream` of given chunks): keep-alive comments, usage with and without `include_usage`, `usage: null`,
  CRLF, a chunk split inside a UTF-8 character, a BOM split across reads, an incomplete tail at EOF, invalid UTF-8, a
  final line without `\n`, a non-JSON `data:` line, an array chunk, `choices` as an object, tool-call deltas,
  `streamUsage: false`, an existing `stream_options` with other keys, a non-2xx answer to a stream, a failed call.
- **Go:** `ChatStream` output does not depend on the split (every split of each fixture body); abort, cut, write error,
  panic and shutdown each give one line; a slow reader stops the provider read (backpressure); heap and stack per held
  stream ≤ 64 KiB at 200 streams (`//go:build !race`); 200 concurrent streams with 0 errors on every OS.
- **Contract:** the stream cases of `formats.test.js` and `usage.test.js` run in `ZP_ENGINE=go` and `shadow` (the
  harness puts `chat-stream` on go / in the shadow list); new cases: a client that drops mid-stream gets one usage line,
  a provider that cuts off gets one line and a clean end, the response headers of a go-served stream equal those of a
  front-mode js stream, and the kill test with a go-served stream (one line at most, never two).
- **Bench:** D6.

## Exit criteria (P4c, slice S3)

- [ ] `chat-stream` can run on `go` behind its own shadow gate; contract suite green in js, front, shadow and go modes
      on Node and Deno (macOS and Ubuntu) and Node on Windows; shadow diff 0 on the contract corpus.
- [ ] The stream fixtures match the JS `chatCompletion` byte for byte; the regenerate-and-diff CI step is green.
- [ ] Exactly one usage line per stream for normal end, client abort, provider cut, shutdown; tests on 4 OS.
- [ ] M4 met on the owner's laptop: ≥ 500 streams, 0 errors, overhead p95 ≤ 10 ms, gateway RSS ≤ 80 MB, with the JS
      and front references re-recorded in the same session; M3 re-measured.
- [ ] Full-matrix CI dispatch green on the branch before the PR.

## Out of scope

- Translated streams (`/v1/messages`, `/v1/responses`, Gemini, Anthropic-format providers): P4d wiring.
- Guardrails and budgets on streams (any check in scope hands off): P4e. M4 is re-checked once P4e serves external
  providers.
- Usage, audit and trace spans beyond the usage line (P4f), agents, routes with fallback, OAuth2 providers (P4g).
- The reverse shadow.

## Open questions for the owner

1. **M4 with guardrails out of the path.** The bench provider must be on the `private` tier for the go engine to serve
   it until P4e (the default guardrails scan external calls, and the engine hands those off). Is M4 met at P4c on that
   basis, with a re-measure on the external tier after P4e? (Default applied in the plan: yes.)
2. **A gateway crash loses the usage line of in-flight go streams** (Decision 16). Accept until P4f, or should P4c add a
   "stream started" record that the ingest completes or closes as unfinished? (Default applied: accept and record it in
   the results as a known gap.)

## Depends on the P4b final fixes

These parts are written against `9f6b004` and must be re-checked once P4b merges into `migrate/deno-go`:
`gw/engine/chat.go PlanChat` (the hand-off list and the "stream: P4c" line), `gw/engine/serve.go Serve`,
`gw/engine/upstream.go Call`, `gw/engine/shadow.go` (`Capture`, `Compare`, slots), `gw/front/front.go` (`GoEngine`,
`GoClasses`, `ServeHTTP`), `gw/cmd/zaafpulse-gw` (shutdown order, `emitUsage`), `lib/gateway/shadow.js`,
`lib/gateway/index.js chatCompletion`, `lib/front/index.js` (`routing`, `GO_CLASSES`, `frontSection`),
`lib/front/shadow-log.js`, `gw/ipc/messages.go RouteClass` and `ipc/v1/config.push.schema.json`,
`scripts/fixtures/gen-chat.js`, `test/contract/harness.js` (`ZP_ENGINE` configs), and the `contract go engine` CI job.
P4e (worktree `p4e-guard-ready`) is changing the guardrail hand-off in `PlanChat` in parallel; whichever merges second
rebases its `PlanChat` change on the other's.

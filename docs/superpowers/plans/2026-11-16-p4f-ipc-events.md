# P4f (slice S8) Usage, Audit Events and Trace Spans over IPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A call the go engine answers leaves the usage log, the cost ledger, the governance chain, the MCP audit log,
the trace store and the OTLP export exactly as the JS gateway leaves them for the same call; a gateway crash mid-call
leaves exactly one usage line; one rate-limit window spans both engines; a test diffs a `js` run against a `go` run.

**Architecture:** The engine builds the trace store's records itself (`gw/engine/spans.go`, `observer.go`,
`traceparent.go`), ported from `lib/otel.js` and proven byte for byte by fixtures the real `lib/otel.js` writes
(`scripts/fixtures/gen-spans.js`), and sends them as `trace.span` through the outbox after the usage line. The control
plane stores them only where JS would (store present and tracing on), then exports them through OTLP with
`otel.exportRecord`. A call journal next to the outbox (`gw/ipc/journal.go`) records each go call's start and the seq of
its usage line, so a restart writes the line of a call that never got one, and never a second. Each engine tells the
other every request it admits (`ratelimit.take`), and a new gateway is seeded with the window (`ratelimit.window`).

**Tech Stack:** Go 1.27.3 standard library only (`crypto/rand`, `encoding/hex`, `encoding/json`, `net/http`); the
module's own `gw/guard` (`SecretSpans`) and `gw/guard/jsre` (read only); Node 24 and Deno 2.9.8, CommonJS,
`node:test`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-16-p4f-ipc-events-design.md` (decisions D1–D22, binding). The `deno` and
  `migrate-to-deno` skills bind all Deno work; the `go-stack-defaults` skill binds Go work, with the P0 decision on
  top: **Go is standard library only** (no new module in `gw/go.mod`).
- IPC v1 version rule (`ipc/v1/README.md`): only new optional fields and new message types. `ipc/v1` goes from 15 to
  17 schema files.
- `gw/guard` belongs to P4e: this plan imports `guard.SecretSpans` and `jsre` and changes nothing under `gw/guard/`.
  If P4e changes those signatures first, adapt the call sites here; do not edit `gw/guard`.
- The durability target stays a killed process; nothing is fsynced (owner decision of 2026-11-12,
  `docs/migration/2026-11-12-p4f-mark-durability.md`).
- Every task ends green on, with `export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"` first:
  `go -C gw vet ./...`, `GOOS=windows go -C gw vet ./...`, `go -C gw test -race -count=1 ./...`,
  `test -z "$(gofmt -l gw)"`, and the task's JS tests on Node (`node --test <files>`) and on Deno
  (`deno test -q --no-check --no-prompt -P=test <files>`). The full Node and Deno suites and the contract suite in
  `js`, `front`, `shadow` and `go` modes run in Task 6 and Task 8.
- The exactly-once tests stay green after Tasks 2, 3 and 4: `test/ipc-*.test.js`, `test/front-kill.test.js`.
- Fixture generators run under the CI fixtures job's Deno flags (`--allow-read --allow-write=gw/testdata --deny-env
  --deny-net --deny-run --deny-sys`) and give the same bytes on Node and Deno: no socket, no port, in-process request
  and response objects (as `scripts/fixtures/gen-chat.js`).
- Prettier: `npx --yes prettier@3 --check <file>` before editing an existing JS file; run `--write` only on files this
  plan creates or on files `--check` reported clean before the edit. `*.json` and `*.md` are in `.prettierignore`.
- Commits: one plain English sentence, like the repo history; **no** `Co-Authored-By`, no AI attribution. Never commit
  `.superpowers/`. Each task ends with its commit and `git push origin migrate/p4f-ipc-events`. No other branch is
  pushed or created.
- Tests never reach the network (127.0.0.1 only), never touch ports 4777 or 4787, and stop every process they start
  before removing its folder (`test/ipc-stub.js tmpDir(t, name, stops)`).
- Full matrix after Task 6 and after Task 8: `gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref
  migrate/p4f-ipc-events`.

## Scheduling (coordinator, 2026-11-16)

P4e (guard and budget wiring) is being implemented on `migrate/p4e-guard-wire`; it adds the sender goroutine and
touches `gw/engine/serve.go`, `gw/cmd/zaafpulse-gw/main.go`, `gw/ipc/outbox.go` and `lib/ipc/ingest.js`. Tasks 1 and 7
stay away from those files and run now. Tasks 2–6, 9 and 8 run after P4e merges into `migrate/deno-go` and this branch
merges `migrate/deno-go`; adapt their `serve.go`, `main.go`, outbox and ingest steps to P4e's code then.

## Decisions (from the spec, one line each; the evidence is in the spec)

1. D1: a go-answered request emits the JS spans for it: SERVER `POST {route}` for every answer the engine writes, CLIENT
   `chat {model}` per usage line, server first, after the last byte; none when tracing is off or the request is unsampled.
2. D2: Go builds the stored record; fixtures from the real `lib/otel.js`; the "facts to JS" alternative is rejected.
3. D3: `SpanRecord.A` is `json.RawMessage`, built in JS insertion order with `engine.Value`.
4. D4: `gen_ai.provider.name`, `server.address`, `server.port` are computed in JS (`providerSpanAttrs`) and pushed per
   provider.
5. D5: `responseObserver` is ported whole (four formats), id and finish reasons only.
6. D6: `traceparent`/`tracestate` parsing ported; `crypto/rand` ids; `traceId` on the usage line as `usageTraceId`.
7. D7: status message `scrub(e.error || "HTTP {status}")` ≤ 500 units through `jsre`; `err` = `maskErr` through
   `guard.SecretSpans`.
8. D8: `config.push.trace.spans` tells the engine whether tracing is live.
9. D9: the ingest stores a span only when the store is on, else skips and acknowledges; exports after the mark.
10. D10: go is allowed with OTLP export on, except with `captureContent: true`.
11. D11: `trace.span.otlp {traceState, remote, message}` makes the OTLP export equal.
12. D12: n2 closed by `otel.status({ fresh: true })`.
13. D13 (corrected 2026-11-16): no audit event per go-served chat call, as in JS; the one new audit entry is D24's, written by the control plane.
14. D14: the call journal `gw-outbox/calls.journal` closes the crash gap, for streams and non-stream calls.
15. D15: the recovered line: base, status 599, the fixed error text, no token counts, cost 0, `ts` = start, `ms` 0.
16. D16: the window is replicated with `ratelimit.take` both ways and seeded with `ratelimit.window`.
17. D17: money compared at nano-USD.
18. D18: `test/s8-equality.test.js` diffs a `js` run against a `go` run.
19. D19: the shadow does not compare spans.
20. D20: T6-2, T1-c, T2-f folded in; `audit.gap`, the replay lock and the 4 MiB tail stay deferred.
21. D21: M3 and M4 re-measured with spans and the journal on.
22. D22: 17 schemas; `build-deno.js` and the `doctor` line updated.
23. D23: the owner accepted the three defaults on 2026-11-16 (599 and cost 0; `captureContent` keeps js; the replicated window).
24. D24: one governance entry `gateway.budget_unchecked` per unchecked-budget alert window, from the supervisor's alert (Task 9).

## Known deviations to record (found while writing this plan)

These are JS behaviours the port does not copy; each gets a Go unit test pinning the engine's behaviour and a line in
the results, and stays out of the generated corpus (the precedent of P4b deviation 3 and P4c difference 2):
1. A response whose `id` (or `message.id`, `response.id`) is not a string: `finishModelSpan` throws on `r.id.slice`
   inside `done`'s try, so JS loses that client span; the engine keeps the span without `gen_ai.response.id`.
2. Gemini `finishReason` lower-casing: JS `toLowerCase` uses full Unicode special casing (`İ` → `i̇`); the engine uses
   `strings.ToLower` (Gemini sends ASCII enums).
3. A chunk JSON the engine's parser refuses (an escaped lone surrogate, nesting deeper than 512): JS reads its id and
   reasons, the engine does not (as P4b minor m1).
4. An OTLP header secret that appears in a provider's error text is masked by JS's own `secrets.mask` only; the engine
   masks the provider secrets of the handoff and `GENERIC_SECRETS`.
5. `url.path` is the request's raw path; JS gives WHATWG's pathname. Equal for every path the engine serves (it routes
   on the exact raw path, P4b entry condition 2).
6. A string cut in the middle of a surrogate pair (a User-Agent, span name or response id at 256 UTF-16 units, an
   attribute at 8192): JS keeps a lone high surrogate, the engine drops the pair whole (found in Task 1).

## File structure

| File | Responsibility |
|---|---|
| `lib/otel.js` | `providerSpanAttrs(prov, id)` (used by `modelSpan`), `exportRecord(span, extra)`, `status({ fresh })` |
| `scripts/fixtures/gen-spans.js` (new) | runs `otel.wrapHandler` and `otel.wrapUsage` over in-process requests; writes `gw/testdata/spans/*.json` |
| `test/spans-fixtures.test.js` (new) | the drift check: regenerating gives the committed bytes, on Node and Deno |
| `gw/engine/traceparent.go` (new) | `TraceCtx`, `StartTrace`, `parseTraceparent`, `cleanTracestate`, `RandomIDs` |
| `gw/engine/observer.go` (new) | `Observer` (`responseObserver(maxChars, false)`) |
| `gw/engine/spans.go` (new) | `ServerSpan`, `ClientSpan`, `routeOf`, `maskErr`, `scrub`, `jsRound`, `utf16Len` |
| `gw/engine/spans_test.go`, `observer_test.go`, `traceparent_test.go` (new) | fixture replay and the pinned deviations |
| `gw/ipc/messages.go` | `SpanRecord.A` raw JSON; `TraceSpanBody.OTLP`; `ConfigPush.Trace`; `ProviderConfig.Span`; `RateLimitTake`, `RateLimitWindow` |
| `ipc/v1/trace.span.schema.json`, `config.push.schema.json`, `ratelimit.take.schema.json` (new), `ratelimit.window.schema.json` (new), fixtures, `README.md` | D22 |
| `gw/ipc/journal.go` (new), `journal_test.go` (new) | the call journal: `Open`, `NoteClose`, `Recover`, compaction |
| `gw/ipc/outbox.go` | `AppendSendNoted` |
| `gw/engine/serve.go` | journal open/close, `finish` (answer, usage, spans), the 429's server span, `traceId` |
| `gw/engine/keys.go` | `RateLimiter.Note`, `Seed`, `Snapshot` |
| `gw/cmd/zaafpulse-gw/main.go` | journal recovery before `hello`; span and usage emitters; `ratelimit.*` |
| `lib/ipc/ingest.js`, `lib/ipc/server-sinks.js` | `spanStoreOn`, `afterSpan` |
| `lib/ipc/session.js`, `lib/ipc/supervisor.js` | `ratelimit.window` in the hello sequence; `ratelimit.take` both ways; `sendControl` |
| `lib/ipc/supervisor.js` (`onUncheckedAlert`), `lib/front/index.js`, `server.js` | D24, Task 9 |
| `lib/ipc/acker.js`, `test/ipc-acker.test.js` (new) | T2-f: retry a failed `beforeAck` on the next tick |
| `lib/front/index.js`, `lib/front/push.js` | routing with `otel {enabled, captureContent}`; `trace` and provider `span` in the push |
| `lib/gateway/keys.js`, `lib/gateway/index.js` | `onTake`, `note`, `windows`; `gateway.onRateTake`, `noteRateTake`, `rateWindows` |
| `server.js` | sinks wiring, push triggers (`tracing`, `otel`), `ratelimit` hooks, minute check of `trace` |
| `scripts/build-deno.js`, `scripts/smoke-binary.js` | the two new schemas, `17 schemas` |
| `test/s8-equality.test.js` (new), `test/contract/harness.js` (export `providerList`) | D18 |
| `test/contract/go-spans.test.js`, `test/contract/go-ratelimit.test.js` (new) | spans and one window, in every engine mode |
| `test/ratelimit-window.test.js`, `test/otel-export-record.test.js` (new) | the window's JS side; `exportRecord` lossless |
| `test/front-kill.test.js`, `test/contract/upstream.js` (`slowwhole`) | exactly one line across a kill |
| (no workflow change) | the S8 test runs in the existing Node and Deno jobs, which take `test/*.test.js` |
| `docs/migration/2026-11-16-p4f-results.md` (new), `docs/migration/2026-11-10-deno-go-migration-plan.md` | results, S8 row |

---

### Task 1: Span records in Go, proven by fixtures from `lib/otel.js` (riskiest: byte equality)

**Files:**
- Modify: `lib/otel.js` (around `modelSpan`, `otel.js:1258-1311`; exports at the end)
- Create: `scripts/fixtures/gen-spans.js`, `test/spans-fixtures.test.js`, `gw/testdata/spans/*.json`
- Create: `gw/engine/traceparent.go`, `gw/engine/observer.go`, `gw/engine/spans.go`
- Create: `gw/engine/traceparent_test.go`, `gw/engine/observer_test.go`, `gw/engine/spans_test.go`
- Modify: `gw/ipc/messages.go` (`SpanRecord.A`, `TraceSpanBody.OTLP`, `ProviderConfig.Span`)
- Modify: `ipc/v1/trace.span.schema.json`, `ipc/v1/config.push.schema.json`; add fixtures
  `ipc/v1/fixtures/trace.span/valid-otlp.json`, `invalid-otlp-remote-string.json`,
  `ipc/v1/fixtures/config.push/valid-provider-span.json`

**Interfaces:**
- Produces (JS): `providerSpanAttrs(prov, providerId) -> { name: string, address?: string, port?: number }`,
  exported from `lib/otel.js`.
- Produces (Go, package `engine`):
  - `type SpanIDs func(n int) string`; `func RandomIDs(n int) string`
  - `type TraceCtx struct { TraceID, SpanID, ParentID, TraceState string; Sampled, Remote bool }`
  - `func StartTrace(h http.Header, ids SpanIDs) TraceCtx`
  - `type ServerFacts struct { Method, Path, Proto string; TLS bool; UserAgent string; Status int; StartUs, EndUs int64 }`
  - `func ServerSpan(tc TraceCtx, f ServerFacts) ipc.TraceSpanBody`
  - `func ClientSpan(tc TraceCtx, spanID string, rec *ipc.UsageRecord, prov *ipc.ProviderSpan, obs *Observed, mask func(string) string) ipc.TraceSpanBody`
  - `type Observer struct{...}`; `func NewObserver() *Observer`; `(*Observer).Feed([]byte)`; `(*Observer).Result() *Observed`
  - `type Observed struct { ID string; Reasons []string }`
- Produces (Go, package `ipc`): `SpanRecord.A json.RawMessage`; `TraceSpanBody.OTLP *SpanOTLP`;
  `type SpanOTLP struct { TraceState *string; Remote *bool; Message *string }`;
  `ProviderConfig.Span *ProviderSpan`; `type ProviderSpan struct { Name string; Address *string; Port *int64 }`.

- [ ] **Step 1: Extract `providerSpanAttrs` in `lib/otel.js` (no behaviour change)**

Above `createOtel`, add:

```js
// The provider-derived attributes of a model span (P4f D4): the semconv provider name of its preset, and the server of
// its baseUrl as WHATWG URL reads it. modelSpan uses it, and lib/front/push.js pushes it per provider to the go engine,
// so both build the same attributes without a second URL parser.
function providerSpanAttrs(prov, providerId) {
  const p = prov && typeof prov === 'object' ? prov : {};
  const preset = p.preset && p.preset !== 'custom' ? p.preset : providerId;
  const out = { name: PROVIDER_NAME[preset] || String(preset || '_OTHER') };
  try {
    const u = p.baseUrl ? new URL(p.baseUrl) : null;
    if (u) {
      out.address = u.hostname;
      out.port = Number(u.port) || (u.protocol === 'https:' ? 443 : 80);
    }
  } catch {
    /* no usable base URL */
  }
  return out;
}
```

In `modelSpan`, replace the lines from `const prov = providers().find(...)` to the end of the `try { const u = ... }`
block, and the two attribute lines that use them, with:

```js
      const pa = providerSpanAttrs(providers().find((p) => p && p.id === e.provider), e.provider);
      const server = pa.address !== undefined ? { 'server.address': pa.address, 'server.port': pa.port } : {};
```

and `'gen_ai.provider.name': pa.name,` in the attributes. Add `providerSpanAttrs` to `module.exports`.

- [ ] **Step 2: Run the otel tests to prove no change**

Run: `node --test test/otel*.test.js test/trace-*.test.js` and the same files under Deno.
Expected: PASS, same counts as before the edit.

- [ ] **Step 3: Write the generator `scripts/fixtures/gen-spans.js`**

```js
// Fixtures for the go engine's trace spans (P4f, slice S8): the real lib/otel.js (wrapHandler, wrapUsage, modelSpan,
// finishModelSpan) handles each case in process (a request and a response object, no socket), with the local trace
// sink on and OTLP export off, and this records what the trace store would keep (lib/trace/recorder.js toRecord) plus
// what OTLP export needs beyond it (P4f D11), and the usage entry as wrapUsage hands it on (traceId).
// gw/engine/spans_test.go builds the same spans from the same input and must give the same bytes.
// Ids: scripts/fixtures/lib.js makes crypto.randomBytes(n) return n bytes equal to a counter that starts at 1 per case,
// so newId is deterministic; the Go test draws ids from the same counter. The server span's times come from
// performance.now: its st and d are written as 0, and the Go test passes StartUs = EndUs = 0.
// Writes gw/testdata/spans/<n>-<name>.json as { fn: "spans", in: { request, provider, answer, usage, usageWhen }, out }.
// Run: node scripts/fixtures/gen-spans.js (no network, no environment, writes only under gw/testdata).
const { EventEmitter } = require('events');
const { writeCases } = require('./lib');
const otel = require('../../lib/otel');
const { toRecord } = require('../../lib/trace/recorder');

const PROVIDERS = [
  { id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1' },
  { id: 'op', preset: 'openai', baseUrl: 'https://api.openai.com/v1' },
  { id: 'v6', preset: 'ollama', baseUrl: 'http://[::1]:11434/v1' },
  { id: 'idn', preset: 'groq', baseUrl: 'https://BÜCHER.example/v1' },
  { id: 'bad', preset: 'mistral', baseUrl: 'not a url' },
];

function fakeReq({ method = 'POST', path = '/v1/chat/completions', httpVersion = '1.1', headers = {}, tls = false }) {
  const req = new EventEmitter();
  Object.assign(req, { method, url: path, httpVersion, headers, socket: { encrypted: tls, remoteAddress: '127.0.0.1' } });
  return req;
}

function fakeRes() {
  const res = new EventEmitter();
  res.statusCode = 200;
  res.write = () => true;
  res.end = () => res;
  return res;
}

// One case: the handler answers with answer.status and answer.chunks (latin1-free utf8 strings or { b64 }), records
// the usage entry before the end ("before", a whole answer) or right after it ("after", a stream), and the response
// ends with "finish" or, when the client left, "close" only.
async function run({ request, answer, usage, usageWhen = 'before', ends = 'finish' }) {
  const recs = [];
  otel.init({
    getConfig: () => ({ gateway: { providers: PROVIDERS } }),
    sink: { enabled: () => true, add: (r) => recs.push(r), expect: () => {} },
    log: () => {},
  });
  let entry = null;
  const u = otel.wrapUsage({ record: (e) => (entry = e) });
  const handler = otel.wrapHandler((req, res) => {
    res.statusCode = answer.status;
    const record = () => usage && u.record(usage);
    if (usageWhen === 'before') record();
    for (const c of answer.chunks || []) res.write(c.b64 ? Buffer.from(c.b64, 'base64') : c);
    res.end();
    if (usageWhen === 'after') record();
  });
  const req = fakeReq(request);
  const res = fakeRes();
  handler(req, res);
  res.emit(ends);
  await otel.reset();
  const spans = recs.map((r) => {
    const rec = toRecord(r);
    if (rec.k === 'server') Object.assign(rec, { st: 0, d: 0 });
    const otlp = {};
    if (r.traceState) otlp.traceState = r.traceState;
    if (r.flags & 0x200) otlp.remote = true;
    if (r.status.code === 2 && r.status.message) otlp.message = r.status.message;
    return Object.keys(otlp).length ? { span: rec, otlp } : { span: rec };
  });
  return { usage: entry, spans };
}

const TP = '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01';
const body = (o) => JSON.stringify(o);
const ok = (id = 'chatcmpl-1', reason = 'stop') =>
  body({ id, object: 'chat.completion', choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finish_reason: reason }] });
const base = { keyId: 'k1', provider: 'oa', model: 'oa/m1', kind: 'chat', format: 'openai', stream: false, local: true };
const line = (more) => ({ ts: 1767225600000, ms: 12.345, status: 200, input: 1000, output: 500, cost: 0.002, ...base, ...more });

const CASES = [
  ['whole-200', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['traceparent-sampled', { request: { headers: { traceparent: TP, tracestate: 'a=1,b=2' } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['traceparent-unsampled', { request: { headers: { traceparent: TP.replace(/-01$/, '-00') } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['traceparent-version-ff', { request: { headers: { traceparent: TP.replace(/^00/, 'ff') } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['traceparent-zero-trace', { request: { headers: { traceparent: `00-${'0'.repeat(32)}-b7ad6b7169203331-01` } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['traceparent-future-version', { request: { headers: { traceparent: `cc-${TP.slice(3)}-what` } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['tracestate-nonprintable', { request: { headers: { traceparent: TP, tracestate: 'a=1\u0007' } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['user-agent-long', { request: { headers: { 'user-agent': `claude-cli/${'x'.repeat(250)}😀😀` } }, answer: { status: 200, chunks: [ok()] }, usage: line({ client: 'claude-code' }) }],
  ['user-agent-empty', { request: { headers: { 'user-agent': '' } }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['tls-http10', { request: { tls: true, httpVersion: '1.0' }, answer: { status: 200, chunks: [ok()] }, usage: line() }],
  ['provider-openai-https', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ provider: 'op' }) }],
  ['provider-ipv6', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ provider: 'v6' }) }],
  ['provider-idn', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ provider: 'idn' }) }],
  ['provider-bad-url', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ provider: 'bad' }) }],
  ['provider-unknown', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ provider: 'nope' }) }],
  ['routed-requested-saved', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ routedTo: 'oa/m2', requested: 'fast', savedTokens: 40, cached: 300 }) }],
  ['upstream-400-masked', { request: {}, answer: { status: 400, chunks: [body({ error: { message: 'bad' } })] }, usage: line({ status: 400, input: 0, output: 0, cost: 0, error: 'bad request: Authorization: Bearer abcdefgh12345678 api_key=supersecretvalue token: "abcdef123456"' }) }],
  ['upstream-500-no-tokens', { request: {}, answer: { status: 502, chunks: [body({ error: { message: 'upstream' } })] }, usage: line({ status: 500, input: 0, output: 0, cost: 0 }) }],
  ['network-599', { request: {}, answer: { status: 502, chunks: [body({ error: { message: 'fetch failed' } })] }, usage: line({ status: 599, input: 0, output: 0, cost: 0, error: 'fetch failed' }) }],
  ['secret-in-error-long', { request: {}, answer: { status: 400, chunks: [body({ error: {} })] }, usage: line({ status: 400, error: `${'é'.repeat(190)} ghp_${'A'.repeat(36)} ${'x'.repeat(400)}` }) }],
  ['rate-limited-429-no-usage', { request: {}, answer: { status: 429, chunks: [body({ error: { message: 'rate limit' } })] }, usage: null }],
  ['zero-tokens-200', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ input: 0, output: 0 }) }],
  ['long-model', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ model: `oa/${'m'.repeat(9000)}` }) }],
  ['ms-rounding', { request: {}, answer: { status: 200, chunks: [ok()] }, usage: line({ ts: 1767225600001, ms: 0.0005 }) }],
  ['stream-usage', { request: {}, usageWhen: 'after', answer: { status: 200, chunks: ['data: {"id":"c1","choices":[{"delta":{"content":"h"}}]}\n\n', 'data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'] }, usage: line({ stream: true, ttft: 3.5 }) }],
  ['stream-crlf-nospace', { request: {}, usageWhen: 'after', answer: { status: 200, chunks: ['data:{"id":"c2","choices":[{"finish_reason":"length"}]}\r\n', 'data: {"choices":[{"finish_reason":"length"}]}\r\n'] }, usage: line({ stream: true, ttft: null }) }],
  ['stream-split-utf8', { request: {}, usageWhen: 'after', answer: { status: 200, chunks: [{ b64: Buffer.from('data: {"id":"caf\xc3', 'latin1').toString('base64') }, { b64: Buffer.from('\xa9","choices":[{"finish_reason":"stop"}]}\n', 'latin1').toString('base64') }] }, usage: line({ stream: true }) }],
  ['stream-client-left', { request: {}, usageWhen: 'after', ends: 'close', answer: { status: 200, chunks: ['data: {"id":"c3","choices":[]}\n'] }, usage: line({ stream: true, output: 3 }) }],
  ['json-array-body', { request: {}, answer: { status: 200, chunks: [`[${ok('a1', 'tool_calls')},${ok('a2', 'stop')}]`] }, usage: line() }],
  ['reasons-dedup-many', { request: {}, answer: { status: 200, chunks: [body({ id: 'r', choices: Array.from({ length: 20 }, (_, i) => ({ finish_reason: `r${i % 18}` })) })] }, usage: line() }],
  ['anthropic-message', { request: { path: '/v1/messages' }, answer: { status: 200, chunks: [body({ type: 'message', id: 'msg_1', role: 'assistant', content: [{ type: 'text', text: 'hi' }], stop_reason: 'end_turn' })] }, usage: line({ format: 'anthropic' }) }],
  ['responses-api', { request: { path: '/v1/responses' }, answer: { status: 200, chunks: [body({ object: 'response', id: 'resp_1', status: 'incomplete', incomplete_details: { reason: 'max_output_tokens' }, output: [] })] }, usage: line({ format: 'responses' }) }],
  ['gemini', { request: { path: '/v1beta/models/gemini-x:generateContent' }, answer: { status: 200, chunks: [body({ responseId: 'g1', candidates: [{ content: { parts: [{ text: 'hi' }] }, finishReason: 'STOP' }] })] }, usage: line({ format: 'gemini' }) }],
  ['choices-not-iterable', { request: {}, answer: { status: 200, chunks: [body({ id: 'x1', choices: 5 })] }, usage: line() }],
  ['body-over-1mib', { request: {}, answer: { status: 200, chunks: [body({ id: 'big', pad: 'x'.repeat(1024 * 1024) })] }, usage: line() }],
  ['embeddings', { request: { path: '/v1/embeddings' }, answer: { status: 200, chunks: [body({ data: [] })] }, usage: line({ kind: 'embeddings' }) }],
];

async function generate() {
  return writeCases(
    'spans',
    CASES.map(([name, args], i) => ({ name: `${String(i + 1).padStart(2, '0')}-${name}`, fn: 'spans', args, call: run })),
  );
}

if (require.main === module) generate().catch((err) => {
  console.error(err);
  process.exit(1);
});
module.exports = { generate };
```

Before writing more, run it once and check each file by eye: a server span and (when `usage` is not null and the
request is sampled) a client span; `traceparent-unsampled` has `spans: []` and no `traceId`; `choices-not-iterable`
has a client span with no `gen_ai.response.id` (absorb threw, the whole JSON branch stopped).

Note: `writeCases` returns nothing; for the drift check, `generate` must also return the file texts. Change
`scripts/fixtures/lib.js writeCases` to collect `files.set(file, text)` and return the Map (it already builds `text`
through `encodeSecrets`); `gen-provauth.js` returns its own Map, so no other generator changes.

- [ ] **Step 4: Run the generator on Node and on Deno and compare**

```bash
node scripts/fixtures/gen-spans.js && git -C . status --short gw/testdata/spans | head
deno run --allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys scripts/fixtures/gen-spans.js
git diff --stat gw/testdata/spans
```

Expected: about 37 files on the first run, and no diff after the Deno run. A case that differs between Node and Deno
(a `StringDecoder` difference on invalid UTF-8, a `toLowerCase` difference) leaves the corpus and becomes a Go unit
test pinning Deno's output, listed under "Known deviations".

- [ ] **Step 5: Write the drift check `test/spans-fixtures.test.js`**

```js
// The span fixtures are generated from lib/otel.js (scripts/fixtures/gen-spans.js) and replayed in Go
// (gw/engine/spans_test.go). Regenerating them, on Node and on Deno, must give the committed files byte for byte, so a
// change to the gateway's spans fails until the fixtures are regenerated and the Go port matches them again.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { generate } = require('../scripts/fixtures/gen-spans');

const DIR = path.join(__dirname, '..', 'gw', 'testdata', 'spans');

test('the span fixtures equal a fresh run of lib/otel.js (regenerate: node scripts/fixtures/gen-spans.js)', async () => {
  const before = new Map(fs.readdirSync(DIR).filter((f) => f.endsWith('.json')).map((f) => [f, fs.readFileSync(path.join(DIR, f), 'utf8')]));
  const files = await generate();
  assert.deepEqual([...files.keys()].sort(), [...before.keys()].sort(), 'the same files');
  for (const [name, text] of files) assert.equal(text, before.get(name), name);
});
```

Run: `node --test test/spans-fixtures.test.js`. Expected: PASS (it rewrites the same bytes).

- [ ] **Step 6: Change the IPC types (`gw/ipc/messages.go`) and the schemas**

```go
// TraceSpanBody is one finished span as the local trace store keeps it, and what OTLP export needs beyond it (P4f D11).
type TraceSpanBody struct {
	Span SpanRecord `json:"span"`
	OTLP *SpanOTLP  `json:"otlp,omitzero"`
}

// SpanOTLP: the span's tracestate, whether its parent is remote (a server span continuing an incoming traceparent),
// and its status message (up to 500 UTF-16 units; err keeps 200).
type SpanOTLP struct {
	TraceState *string `json:"traceState,omitzero"`
	Remote     *bool   `json:"remote,omitzero" ipc:"enum=true"`
	Message    *string `json:"message,omitzero"`
}
```

In `SpanRecord`, `A json.RawMessage \`json:"a"\`` (the exact bytes, in the order they were built: a stored line is
byte-equal to the JS one, P4f D3). `checkShape` must accept a `json.RawMessage` field that holds an object: add a rule
`ipc:"object"` and check `len(a) > 0 && a[0] == '{'` after trimming spaces. Add to `ProviderConfig`:

```go
	Span *ProviderSpan `json:"span,omitzero"` // P4f D4: lib/otel.js providerSpanAttrs, computed by the control plane
}

// ProviderSpan is a provider's model-span attributes: gen_ai.provider.name, server.address and server.port.
type ProviderSpan struct {
	Name    string  `json:"name" ipc:"minlen=1"`
	Address *string `json:"address,omitzero"`
	Port    *int64  `json:"port,omitzero" ipc:"min=0"`
}
```

In `ipc/v1/trace.span.schema.json`, add next to `span`:

```json
    "otlp": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "traceState": { "type": "string" },
        "remote": { "type": "boolean", "const": true },
        "message": { "type": "string" }
      }
    }
```

In `ipc/v1/config.push.schema.json`, add to the provider item's `properties`:

```json
            "span": {
              "type": "object",
              "required": ["name"],
              "additionalProperties": false,
              "properties": {
                "name": { "type": "string", "minLength": 1 },
                "address": { "type": "string" },
                "port": { "type": "integer", "minimum": 0 }
              }
            }
```

Fixtures: `ipc/v1/fixtures/trace.span/valid-otlp.json` (an envelope whose body has `otlp: { "traceState": "a=1",
"remote": true, "message": "HTTP 400" }`), `invalid-otlp-remote-string.json` (`"remote": "yes"`),
`ipc/v1/fixtures/config.push/valid-provider-span.json` (one provider with `span: { "name": "openai", "address":
"api.openai.com", "port": 443 }`).

Run: `go -C gw test ./ipc/ && node --test test/ipc-schema.test.js test/ipc-types.test.js`. Expected: PASS (the round
trip keeps the `a` bytes; every fixture passes or fails as named).

- [ ] **Step 7: Write the failing Go fixture test `gw/engine/spans_test.go`**

```go
package engine

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// counterIDs is scripts/fixtures/lib.js crypto.randomBytes: n bytes equal to a counter that starts at 1 per case.
func counterIDs() SpanIDs {
	c := 1
	return func(n int) string {
		for {
			b := bytes.Repeat([]byte{byte(c & 255)}, n)
			c++
			if b[0] != 0 { // otel.js newId draws again for an all-zero id
				return hex.EncodeToString(b)
			}
		}
	}
}

type spanCase struct {
	Request struct {
		Method      *string           `json:"method"`
		Path        *string           `json:"path"`
		HTTPVersion *string           `json:"httpVersion"`
		Headers     map[string]string `json:"headers"`
		TLS         bool              `json:"tls"`
	} `json:"request"`
	Answer struct {
		Status int               `json:"status"`
		Chunks []json.RawMessage `json:"chunks"`
	} `json:"answer"`
	Usage     *ipc.UsageRecord `json:"-"`
	UsageRaw  json.RawMessage  `json:"usage"`
	UsageWhen string           `json:"usageWhen"`
	Ends      string           `json:"ends"`
}

func TestSpansMatchTheJSFixtures(t *testing.T) {
	for _, c := range loadFixtures(t, "spans") {
		t.Run(c.Name, func(t *testing.T) {
			var in spanCase
			if err := json.Unmarshal(c.In, &in); err != nil {
				t.Fatal(err)
			}
			got := replaySpanCase(t, &in)
			want := canonicalOut(t, c.Out)
			if !bytes.Equal(got, want) {
				t.Fatalf("spans differ\n got: %s\nwant: %s", got, want)
			}
		})
	}
}
```

`replaySpanCase` builds the header (`http.Header` from `Headers`, canonical names), calls
`StartTrace(h, counterIDs())`, feeds every chunk (a JSON string, or `{b64}` decoded) into `NewObserver()`, converts
the fixture's usage line (float USD `cost` → nano with `toNano` semantics: `int64(math.Round(cost * 1e9))`, JS
`Math.round` through `jsRound`) to an `ipc.UsageRecord`, then returns the JSON of
`{"usage": <the line with traceId when the trace is sampled>, "spans": [server, client?]}` written with the same
encoder the gateway uses (`ipc.Encode` for each `TraceSpanBody`, then assembled with `engine.Value` in the order
`usage`, `spans`). The usage line is compared field by field after converting `cost` back to float USD with
`fromNano`, since the fixture's line is the JS float. The server span gets `StartUs: 0, EndUs: 0` and the
`http.response.status_code` of `answer.status`; the client span is built only when `usage` is not null and
`tc.Sampled`. `canonicalOut` re-encodes the fixture's `out` with keys in their file order (decode into
`engine.Value` with `Parse`, then `Stringify`).

Run: `go -C gw test ./engine -run TestSpansMatchTheJSFixtures`. Expected: FAIL to compile (`StartTrace` undefined).

- [ ] **Step 8: Implement `gw/engine/traceparent.go`**

```go
package engine

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
)

// SpanIDs draws an id of n random bytes as lowercase hex, never all zero (lib/otel.js newId).
type SpanIDs func(n int) string

// RandomIDs is the engine's SpanIDs.
func RandomIDs(n int) string {
	b := make([]byte, n)
	for {
		_, _ = rand.Read(b)
		for _, x := range b {
			if x != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}

// TraceCtx is a request's trace context: lib/otel.js requestContext's ctx.self (TraceID, SpanID, Sampled, TraceState)
// and ctx.parent (ParentID, Remote: the parent is the caller's span).
type TraceCtx struct {
	TraceID, SpanID, ParentID, TraceState string
	Sampled, Remote                       bool
}

var traceparentRe = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$`)

// StartTrace continues the request's traceparent when it is valid, else starts a trace. The ids are drawn in
// requestContext's order: the trace id (only for a new trace), then the server span's id.
func StartTrace(h http.Header, ids SpanIDs) TraceCtx {
	in, ok := parseTraceparent(strings.Join(h.Values("Traceparent"), ", "), len(h.Values("Traceparent")) > 0)
	var tc TraceCtx
	if ok {
		tc.TraceID, tc.Sampled, tc.ParentID, tc.Remote = in.traceID, in.sampled, in.spanID, true
		tc.TraceState = cleanTracestate(strings.Join(h.Values("Tracestate"), ", "), len(h.Values("Tracestate")) > 0)
	} else {
		tc.TraceID, tc.Sampled = ids(16), true
	}
	tc.SpanID = ids(8)
	return tc
}

type incoming struct {
	traceID, spanID string
	sampled         bool
}

// parseTraceparent is otel.js parseTraceparent: W3C level 1, a future version may append fields, version ff is
// invalid, 00 may not; all-zero ids are invalid. The value is trimmed as String.prototype.trim does.
func parseTraceparent(v string, present bool) (incoming, bool) {
	if !present {
		return incoming{}, false
	}
	m := traceparentRe.FindStringSubmatch(jsTrim(v))
	if m == nil || m[1] == "ff" || (m[1] == "00" && m[5] != "") || allZeroHex(m[2]) || allZeroHex(m[3]) {
		return incoming{}, false
	}
	return incoming{traceID: m[2], spanID: m[3], sampled: hexNibble(m[4][1])&1 == 1}, true
}

// cleanTracestate is otel.js cleanTracestate: passed on when printable ASCII and at most 512 UTF-16 units, trimmed.
func cleanTracestate(v string, present bool) string {
	if !present || utf16Len(v) > 512 {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return ""
		}
	}
	return strings.TrimSpace(v)
}

func allZeroHex(s string) bool { return strings.Trim(s, "0") == "" }

func hexNibble(c byte) byte {
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - '0'
}

// jsTrim is String.prototype.trim: WhiteSpace and LineTerminator code points at both ends.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}
```

Note `parseTraceparent`'s flags: `parseInt(flags, 16) & 1` is the low bit of the second hex digit.

Add `gw/engine/traceparent_test.go` with table tests for the joined duplicate header (`"00-…-01, 00-…-01"` is
invalid), surrounding spaces, uppercase hex (invalid), and `tracestate` of 513 characters (dropped).

- [ ] **Step 9: Implement `gw/engine/observer.go`**

```go
package engine

import (
	"regexp"
	"strings"
)

// observerMax is otel.js MAX_BODY: 1 MiB, counted in UTF-16 units of the decoded text.
const observerMax = 1024 * 1024

// Observed is responseObserver's result without text (content capture keeps the go classes on js, P4f D10).
type Observed struct {
	ID      string
	Reasons []string
}

// Observer is lib/otel.js responseObserver(maxChars, false): it reads what the engine writes back (JSON, or server-sent
// events, in any of the four formats) for the response id and the finish reasons.
type Observer struct {
	dec     streamDecoder // StringDecoder('utf8'): a split character waits for its next bytes
	mode    byte          // 0 unknown, 'j' json, 's' sse
	buf     strings.Builder
	bufLen  int // UTF-16 units in buf
	size    int // json mode: UTF-16 units seen
	over    bool
	id      string
	reasons []string
}

func NewObserver() *Observer { return &Observer{} }

// Feed is responseObserver's feed(chunk).
func (o *Observer) Feed(p []byte) {
	if o.over || len(p) == 0 {
		return
	}
	s := o.dec.write(p)
	if o.mode == 0 {
		head := strings.TrimLeftFunc(s, isJSSpace)
		if head == "" {
			return
		}
		o.mode = 's'
		if head[0] == '{' || head[0] == '[' {
			o.mode = 'j'
		}
	}
	n := utf16Len(s)
	if o.mode == 'j' {
		o.size += n
		if o.size > observerMax {
			o.over = true
			o.buf.Reset()
			o.bufLen = 0
			return
		}
		o.buf.WriteString(s)
		o.bufLen += n
		return
	}
	o.buf.WriteString(s)
	o.bufLen += n
	text := o.buf.String()
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break
		}
		o.line(text[:i])
		text = text[i+1:]
	}
	o.buf.Reset()
	o.buf.WriteString(text)
	o.bufLen = utf16Len(text)
	if o.bufLen > observerMax {
		o.buf.Reset()
		o.bufLen = 0
	}
}

// Result is responseObserver's result(): the id and the finish reasons, each once, in first-seen order.
func (o *Observer) Result() *Observed {
	if o.buf.Len() > 0 {
		switch o.mode {
		case 'j':
			if v, err := Parse([]byte(o.buf.String())); err == nil {
				if v.Kind == Array {
					for _, x := range v.A {
						if !o.absorb(x) {
							break // a TypeError in absorb ends the loop: JS's try covers the whole for
						}
					}
				} else {
					o.absorb(v)
				}
			}
		case 's':
			o.line(o.buf.String())
		}
		o.buf.Reset()
	}
	seen := map[string]bool{}
	var rs []string
	for _, r := range o.reasons {
		if !seen[r] {
			seen[r] = true
			rs = append(rs, r)
		}
	}
	return &Observed{ID: o.id, Reasons: rs}
}

func (o *Observer) line(l string) {
	t := jsTrim(l)
	if !strings.HasPrefix(t, "data:") {
		return
	}
	d := jsTrim(t[5:])
	if d == "" || d == "[DONE]" {
		return
	}
	if v, err := Parse([]byte(d)); err == nil {
		o.absorb(v)
	}
}

var responseEndRe = regexp.MustCompile(`^response\.(completed|incomplete|failed)$`)
```

`absorb(v *Value) bool` ports `absorb(o)` statement by statement and returns false where JS would throw a TypeError
(its caller's `try` ends there). The JS rules to keep, each with a corpus case or a Go unit test:
- `!o || typeof o !== 'object'` returns: `nil`, `Null`, `Bool`, `Number`, `String` → return true;
  arrays are objects (property reads give undefined).
- `iter(x)` is `for (const c of x || [])`: falsy (`null`, `false`, `0`, `""`, missing) → no items; an `Array` → its
  items; a non-empty `String` → one item per code point, each a primitive; any other truthy value → TypeError (return
  false).
- `prop(c, k)` on an item: `Null` → TypeError; a primitive or an array → undefined; an object → its value.
- `reason(r)`: a non-empty `String` is appended.
- the id rules: `typeof o.id === 'string' && !st.id && !o.type`; `o.responseId` string → `st.id || it`;
  `st.id = st.id || o.id || ''` and `o.message.id || st.id` and `resp.id || st.id` only take a `String` value (Known
  deviation 1: a non-string id is ignored where JS would later throw and lose the span).
- Gemini: `reason(c.finishReason && String(c.finishReason).toLowerCase())` with `jsString(v)` (numbers through
  `NumberString`, booleans, arrays joined by `,`, objects `[object Object]`) and `strings.ToLower` (Known deviation 2).
- `o.type === 'message' || (Array.isArray(o.content) && o.role === 'assistant')`.
- `/^response\.(completed|incomplete|failed)$/.test(o.type)` tests `String(o.type)` (`jsString`), and
  `o.object === 'response' ? o : … ? o.response : null`; `resp` may be a primitive (its properties are undefined).
- The Responses status: `completed` → `stop`; `incomplete` → `incomplete_details.reason` (when that object is truthy
  and the reason truthy) else `length`; `failed` → `error`.

`streamDecoder.write(p)` is Node's `StringDecoder('utf8').write`: it holds back an incomplete final sequence (up to 3
bytes) and decodes the rest; invalid bytes become U+FFFD per maximal subpart, as `validText` in `gw/engine/stream.go`
already does. Reuse `validText` on the complete part.

Add `gw/engine/observer_test.go` with the pinned deviations 1 and 2 and a test that feeding a stream in 1-byte reads
gives the same result as one read.

- [ ] **Step 10: Implement `gw/engine/spans.go`**

```go
package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/guard/jsre"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// otel.js cleanValue: a string over 8192 UTF-16 units is cut and marked.
const maxAttrValue = 8192

// ServerFacts is what the server span reads of a request the engine answered (otel.js requestContext's done).
type ServerFacts struct {
	Method, Path, Proto string // Proto: "1.1" or "1.0" (Node's req.httpVersion; the front speaks HTTP/1.1 only)
	TLS                 bool
	UserAgent           string // the first User-Agent value; "" adds no attribute
	Status              int
	StartUs, EndUs      int64
}

// ServerSpan is the SERVER span "{method} {route}" of a request the engine answered.
func ServerSpan(tc TraceCtx, f ServerFacts) ipc.TraceSpanBody {
	route := routeOf(f.Path)
	a := NewObject()
	setStr(a, "http.request.method", f.Method)
	setStr(a, "http.route", route)
	setStr(a, "url.path", f.Path)
	scheme := "http"
	if f.TLS {
		scheme = "https"
	}
	setStr(a, "url.scheme", scheme)
	setStr(a, "network.protocol.version", f.Proto)
	a.Set("http.response.status_code", Num(float64(f.Status)))
	if f.UserAgent != "" {
		setStr(a, "user_agent.original", jsSlice(f.UserAgent, 256))
	}
	ok, msg := true, ""
	if f.Status >= 500 {
		setStr(a, "error.type", strconv.Itoa(f.Status))
		ok = false // span.fail(String(status)): the message is '' (masked '' stays '')
	}
	parent := ""
	if tc.Remote {
		parent = tc.ParentID
	}
	return record(tc, tc.SpanID, parent, "server", jsSlice(f.Method+" "+route, 256), f.StartUs, f.EndUs, a, ok, msg, tc.Remote)
}

// ClientSpan is modelSpan + finishModelSpan for one usage line: the CLIENT span "chat {model}" (or "embeddings
// {model}"), child of the server span. obs is the observed answer (nil: none was observed, embeddings).
func ClientSpan(tc TraceCtx, spanID string, rec *ipc.UsageRecord, prov *ipc.ProviderSpan, obs *Observed, mask func(string) string) ipc.TraceSpanBody {
	op := "chat"
	if rec.Kind != nil && *rec.Kind == "embeddings" {
		op = "embeddings"
	}
	a := NewObject()
	setStr(a, "gen_ai.operation.name", op)
	name := rec.Provider
	if name == "" {
		name = "_OTHER"
	}
	if prov != nil {
		name = prov.Name
	}
	setStr(a, "gen_ai.provider.name", name)
	setStr(a, "gen_ai.request.model", rec.Model)
	resp := rec.Model
	if rec.RoutedTo != nil && *rec.RoutedTo != "" {
		resp = *rec.RoutedTo
	}
	setStr(a, "gen_ai.response.model", resp)
	if prov != nil && prov.Address != nil {
		setStr(a, "server.address", *prov.Address)
		a.Set("server.port", Num(float64(*prov.Port)))
	}
	if rec.Stream != nil {
		a.Set("gen_ai.request.stream", &Value{Kind: Bool, B: *rec.Stream})
	}
	in, out, cached := deref(rec.Input), deref(rec.Output), deref(rec.Cached)
	failed := rec.Status >= 400 || (rec.Error != nil && *rec.Error != "")
	if in != 0 || out != 0 || rec.Status < 400 {
		a.Set("gen_ai.usage.input_tokens", Num(float64(in)))
		a.Set("gen_ai.usage.output_tokens", Num(float64(out)))
	}
	if cached != 0 {
		a.Set("gen_ai.usage.cache_read.input_tokens", Num(float64(cached)))
	}
	if rec.TTFT.Set && !rec.TTFT.Null && rec.Stream != nil && *rec.Stream { // e.ttft != null && e.stream
		a.Set("gen_ai.response.time_to_first_chunk", Num(rec.TTFT.V/1000))
	}
	if rec.SessionID != nil && *rec.SessionID != "" {
		setStr(a, "gen_ai.conversation.id", *rec.SessionID)
	}
	if rec.Agent != nil && *rec.Agent != "" {
		setStr(a, "gen_ai.agent.id", *rec.Agent)
	}
	a.Set("zaafpulse.cost.usd", Num(float64(rec.Cost)/1e9)) // lib/money.js fromNano
	key := rec.KeyID
	if key == "" {
		key = "dashboard"
	}
	setStr(a, "zaafpulse.api_key.id", key)
	setStr(a, "zaafpulse.provider.id", rec.Provider)
	kind := "chat"
	if rec.Kind != nil && *rec.Kind != "" {
		kind = *rec.Kind
	}
	setStr(a, "zaafpulse.request.kind", kind)
	if rec.Format != nil && *rec.Format != "" {
		setStr(a, "zaafpulse.request.format", *rec.Format)
	}
	if rec.Requested != nil && *rec.Requested != "" {
		setStr(a, "zaafpulse.request.model_alias", *rec.Requested)
	}
	if rec.SavedTokens != nil && *rec.SavedTokens != 0 {
		a.Set("zaafpulse.token_saver.saved_tokens", Num(float64(*rec.SavedTokens)))
	}
	msg := ""
	if failed {
		typ := "_OTHER"
		switch {
		case rec.Status == 599:
			typ = "network_error"
		case rec.Status != 0:
			typ = strconv.FormatInt(rec.Status, 10)
		}
		setStr(a, "error.type", typ)
		text := fmt.Sprintf("HTTP %d", rec.Status)
		if rec.Error != nil && *rec.Error != "" {
			text = *rec.Error
		}
		msg = jsSlice(scrub(mask(text)), 500)
	}
	if obs != nil && rec.Status < 400 {
		if obs.ID != "" {
			setStr(a, "gen_ai.response.id", jsSlice(obs.ID, 256))
		}
		if len(obs.Reasons) > 0 {
			rs := obs.Reasons
			if len(rs) > 16 {
				rs = rs[:16]
			}
			arr := &Value{Kind: Array}
			for _, r := range rs {
				arr.A = append(arr.A, Str(cleanString(r)))
			}
			a.Set("gen_ai.response.finish_reasons", arr)
		}
	}
	endMs := float64(rec.Ts)
	start := jsRound((endMs - rec.Ms) * 1000) // msToNs(end - ms) / 1000
	end := jsRound(endMs * 1000)
	return record(tc, spanID, tc.SpanID, "client", jsSlice(op+" "+rec.Model, 256), int64(start), int64(end), a, !failed, msg, false)
}

// record is lib/trace/recorder.js toRecord, plus the OTLP extras.
func record(tc TraceCtx, id, parent, kind, name string, startUs, endUs int64, a *Value, ok bool, msg string, remote bool) ipc.TraceSpanBody {
	if endUs < startUs {
		endUs = startUs
	}
	r := ipc.SpanRecord{T: tc.TraceID, S: id, N: name, K: kind, St: startUs, D: endUs - startUs, OK: ok,
		Src: "gateway", Svc: "zaafpulse", A: json.RawMessage(a.Stringify())}
	if parent != "" {
		r.P = &parent
	}
	if !ok {
		e := maskErr(msg)
		if msg == "" {
			e = "error"
		}
		r.Err = &e
	}
	b := ipc.TraceSpanBody{Span: r}
	var x ipc.SpanOTLP
	if tc.TraceState != "" {
		ts := tc.TraceState
		x.TraceState = &ts
	}
	if remote {
		t := true
		x.Remote = &t
	}
	if !ok && msg != "" {
		m := msg
		x.Message = &m
	}
	if x.TraceState != nil || x.Remote != nil || x.Message != nil {
		b.OTLP = &x
	}
	return b
}
```

Plus, in the same file:
- `setStr(a, k, v)` sets `Str(cleanString(v))`; `cleanString(v)` is `cleanValue` for a string: over 8192 UTF-16
  units → `jsSlice(v, 8192) + "…"`.
- `deref(p *int64) int64` (0 for nil).
- `jsRound(x float64) float64`: `f := math.Floor(x); if x-f >= 0.5 { f++ }; return f` (JS `Math.round`, ties to +∞,
  exact for 0.49999999999999994).
- `utf16Len(s string) int`: the sum of `utf16.RuneLen(r)` over the runes (an invalid rune counts 1, as U+FFFD).
- `routeOf(path string) string`: the `/v1beta/models/<model>:<method>` rewrite
  (`^/v1beta/models/.+:([A-Za-z]+)$` → `/v1beta/models/{model}:$1`), then each `/` segment that matches
  `^[a-z]+_[A-Za-z0-9]{6,}$`, `^[0-9a-fA-F-]{8,}$` or contains `[0-9]{4,}` becomes `{id}`. RE2 is enough here: every
  class is ASCII and no `\s` or case folding is involved.
- `scrub(text string) string`: `GENERIC_SECRETS` in order, compiled once with `jsre.MustCompile(src, flags)` and run
  with `ReplaceAll` over `jsre.U16(text)`:
  - `\b(Bearer|Basic|Api-Token)\s+[A-Za-z0-9._~+/=-]{8,}` flags `gi` → group 1 + ` ***`;
  - `\b(?:sk|pk|rk)-[A-Za-z0-9_-]{12,}` flags `g` → `***`;
  - `\bdt0[a-z]\d{2}\.[A-Za-z0-9._-]{16,}` flags `g` → `***`;
  - `((?:api[-_]?key|token|secret|password)["']?\s*[:=]\s*["']?)[^\s"',;&]{6,}` flags `gi` → group 1 + `***`.
- `maskErr(text string) string`: `guard.SecretSpans(text, nil)`; build the UTF-16 result with each span replaced by
  `‹<label> hidden›` (U+2039, U+203A, as `lib/trace/model.js maskSecrets`), cut to 200 units, and drop a trailing
  high surrogate. When `SecretSpans` returns an error (its budget ran out; impossible for 500 units, the step budget
  is 20,000,000), return `‹secret hidden›` for the whole text: never store text that was not scanned.

- [ ] **Step 11: Run the Go fixture test until every case passes**

Run: `go -C gw test ./engine -run 'TestSpans|TestObserver|TestTraceparent' -count=1 -v 2>&1 | tail -40`.
Expected: PASS for every `gw/testdata/spans` case. A failing case is fixed in the port, never in the fixture; a case
that cannot be matched (a JS runtime quirk) leaves the corpus as a pinned Go test and a "Known deviations" entry.

- [ ] **Step 12: Full checks, commit, push**

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./... && test -z "$(gofmt -l gw)"
node --test test/spans-fixtures.test.js test/ipc-schema.test.js test/ipc-types.test.js
deno test -q --no-check --no-prompt -P=test test/spans-fixtures.test.js test/ipc-schema.test.js test/ipc-types.test.js
git add lib/otel.js scripts/fixtures/gen-spans.js scripts/fixtures/lib.js test/spans-fixtures.test.js gw/testdata/spans gw/engine/traceparent.go gw/engine/observer.go gw/engine/spans.go gw/engine/*_test.go gw/ipc/messages.go gw/ipc/shape.go ipc/v1
git commit -m "Build the gateway's trace spans in Go, byte-equal to lib/otel.js by generated fixtures (P4f T1)"
git push origin migrate/p4f-ipc-events
```

### Task 2: The call journal closes the crash gap (riskiest: exactly once across a crash)

**Files:**
- Create: `gw/ipc/journal.go`, `gw/ipc/journal_test.go`
- Modify: `gw/ipc/outbox.go` (`AppendSendNoted`), `gw/ipc/outbox_test.go` (T1-c)
- Modify: `gw/engine/serve.go` (journal open around each served call; `emit` takes the call id),
  `gw/engine/*_test.go` that build an `Engine` (the new `emit` signature)
- Modify: `gw/cmd/zaafpulse-gw/main.go` (open and recover the journal before `hello`; `emitUsage` with the call)
- Modify: `test/contract/upstream.js` (model `slowwhole`), `test/front-kill.test.js` (T6-2 and the in-flight calls)

**Interfaces:**
- Consumes: `ipc.Outbox` (`Next`, `append`, `sendMu`), `ipc.UsageRecord`, `ipc.TypeUsageReport`.
- Produces (package `ipc`):
  - `const CallsFile = "calls.journal"`, `const RecoveredError = "the gateway stopped before the call ended"`
  - `func OpenCallJournal(dir string) (*CallJournal, error)`
  - `func (j *CallJournal) Open(ts int64, base UsageRecord) int64` (0 when the line could not be written)
  - `func (j *CallJournal) NoteClose(id, seq int64) error` (seq 0: closed with no line)
  - `func (j *CallJournal) Reopen(id int64)`
  - `func (j *CallJournal) Recover(box *Outbox, now int64) (int, error)`
  - `func (j *CallJournal) Close() error`
  - `func (o *Outbox) AppendSendNoted(typ string, body any, ts int64, note func(seq int64) error, undo func(), send func([]byte) error) (int64, error)` (`send` nil: append only)
- Produces (package `engine`): `type Calls interface { Open(ts int64, base ipc.UsageRecord) int64; NoteClose(id, seq int64) error }`;
  `engine.New(up *Upstream, emit func(r *ipc.UsageReport, call int64) error, logf func(string, ...any))`;
  `(*Engine).SetCalls(c Calls)`.

- [ ] **Step 1: Write the failing crash-point test `gw/ipc/journal_test.go`**

The test proves D14 at every write boundary: it runs a scripted sequence of calls through a journal and an outbox,
records the folder's bytes after every single file write (a hook on both writers), and for each snapshot, as if the
process had been killed right there, reopens the outbox, runs `Recover`, and counts the usage lines per call across
the reopened outbox's segments plus the acknowledged ones. Every call that was opened must have exactly one line.

```go
package ipc

import (
	"os"
	"path/filepath"
	"testing"
)

// snapshotDir copies every regular file of dir into a new temp folder (a kill at this point leaves exactly these bytes).
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	out := t.TempDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || e.Name() == ".lock" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestJournalExactlyOneLineAtEveryCrashPoint(t *testing.T) {
	dir := t.TempDir()
	box, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	j, err := OpenCallJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	var snaps []string
	acked := map[int64]bool{} // seqs acknowledged (and maybe pruned) before the snapshot
	hook := func() { snaps = append(snaps, snapshotDir(t, dir)) }
	j.afterWrite, box.afterWrite = hook, hook
	model := func(i int) UsageRecord { return UsageRecord{KeyID: "k", Provider: "p", Model: string(rune('a' + i))} }
	ids := make([]int64, 6)
	for i := range ids {
		ids[i] = j.Open(int64(1000+i), model(i))
		if ids[i] == 0 {
			t.Fatal("open failed")
		}
	}
	for i, id := range ids {
		if i == 4 {
			break // calls 4 and 5 are in flight at the end: a kill leaves them open
		}
		rec := model(i)
		rec.Status = 200
		if _, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: rec}, 2000, func(s int64) error { return j.NoteClose(id, s) }, func() { j.Reopen(id) }, nil); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := box.Ack(2); err != nil { // the control plane stored seqs 1 and 2: their segment may be pruned
				t.Fatal(err)
			}
			acked[1], acked[2] = true, true
		}
	}
	for n, snap := range snaps {
		counts := recoverAndCount(t, snap)
		for i := range ids {
			if got := counts[string(rune('a'+i))]; got > 1 {
				t.Fatalf("crash point %d: call %d has %d lines", n, i, got)
			}
		}
		// Every call opened before this point has exactly one line (stored, acked, or recovered).
		for i := 0; i < openedBy(snap); i++ {
			if counts[string(rune('a'+i))] != 1 {
				t.Fatalf("crash point %d: call %d has %d lines, want 1", n, i, counts[string(rune('a'+i))])
			}
		}
	}
}
```

`recoverAndCount(t, snap)` opens the outbox in `snap`, opens its journal, runs `Recover(box, 9999)`, then reads every
segment line (`Replay(1, ...)`, collecting `usage.report` bodies by `record.model`) and adds the models of the seqs
the snapshot's `stream.json` shows as acknowledged; those lines were sent in the original run, so the test records the
model of each acknowledged seq (`acked`) up front and counts them once. `openedBy(snap)` is the number of `"o"` lines
in the snapshot's `calls.journal` before recovery. Add a second test, `TestRecoverTwiceRecoversOnce`: a journal with two
open calls, `Recover` interrupted after its first append (a hook that returns an error from the second `NoteClose`),
then `Recover` again: each call has exactly one recovered line. A third, `TestRecoveredLine`: the recovered record is
the base with `status` 599, `ms` 0, `ts` the open's ts, `error` `RecoveredError`, no token fields, `cost` 0, no
`traceId`. A fourth, `TestNoteFailureAppendsNothing`: `note` returns an error, the outbox's `Next` does not move and
the call stays open (the next start recovers it). A fifth, `TestAppendFailureReopens`: the outbox write fails after the
note; `Reopen` is called and a later `Recover` recovers the call once.

The hooks: `afterWrite func()` fields on `CallJournal` and `Outbox` (nil in production), called after each completed
file write (a journal line, an outbox line, a `stream.json` save, a segment removal).

Run: `go -C gw test ./ipc -run 'Journal|Recover|NoteFailure|AppendFailure'`. Expected: FAIL to compile.

- [ ] **Step 2: Implement `gw/ipc/journal.go`**

```go
package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CallJournal records the go engine's calls in flight (P4f D14), next to the outbox in calls.journal: append only, one
// writer (the outbox's lock covers the folder), no fsync (the target is a killed process, as for the outbox).
//   {"o": id, "ts": start ms, "r": base record}  a call starts, before its provider call;
//   {"c": id, "seq": n}                         its usage line takes seq n (noted under the outbox's send lock, right
//                                               before that line is appended); without seq: it ends with no line;
//   {"u": id}                                   the append after its close failed: it is open again.
// On start, before hello, Recover appends one line for each call of the previous run that has none: no close, or a
// close whose seq the reopened outbox never wrote (seq >= Next). Next after a reopen is above every written seq and not
// above an unwritten one (stream.json is saved on ack and close only, never ahead of an append), and no event is
// appended between a close and its line (the send lock), so a call gets exactly one line: never none, never two.
// Recover notes its own closes first, so a kill during the recovery does not recover a call twice.
type CallJournal struct {
	mu         sync.Mutex
	path       string
	f          *os.File
	size       int64
	next       int64
	open       int // calls opened and not yet closed in this run
	afterWrite func()
}

type journalLine struct {
	O   int64        `json:"o,omitzero"`
	Ts  int64        `json:"ts,omitzero"`
	R   *UsageRecord `json:"r,omitzero"`
	C   int64        `json:"c,omitzero"`
	Seq int64        `json:"seq,omitzero"`
	U   int64        `json:"u,omitzero"`
}

const (
	CallsFile      = "calls.journal"
	RecoveredError = "the gateway stopped before the call ended"
	// journalCompactAt: with no call open, a journal past this size is emptied.
	journalCompactAt = 64 << 10
)

// OpenCallJournal opens dir's calls.journal for appending (created when missing). The caller holds the outbox's lock.
func OpenCallJournal(dir string) (*CallJournal, error) {
	p := filepath.Join(dir, CallsFile)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the call journal: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(st.Size(), 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &CallJournal{path: p, f: f, size: st.Size(), next: 1}, nil
}

func (j *CallJournal) write(l journalLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := j.f.Write(b)
	if err != nil {
		if n > 0 { // never leave a fragment in the middle (a later line would follow it on the same line)
			_ = j.f.Truncate(j.size)
			_, _ = j.f.Seek(j.size, 0)
		}
		return err
	}
	j.size += int64(n)
	if j.afterWrite != nil {
		j.afterWrite()
	}
	return nil
}

// Open records a call about to start and returns its id; 0 when the line could not be written (the call goes on, and
// a crash during it loses its line, as before P4f).
func (j *CallJournal) Open(ts int64, base UsageRecord) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.open == 0 && j.size > journalCompactAt {
		j.compact()
	}
	id := j.next
	if err := j.write(journalLine{O: id, Ts: ts, R: &base}); err != nil {
		return 0
	}
	j.next++
	j.open++
	return id
}

// NoteClose records that call id's usage line takes seq (0: the call ends with no line).
func (j *CallJournal) NoteClose(id, seq int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.write(journalLine{C: id, Seq: seq}); err != nil {
		return err
	}
	j.open--
	return nil
}

// Reopen undoes a close whose line was not appended.
func (j *CallJournal) Reopen(id int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.write(journalLine{U: id}) == nil {
		j.open++
	}
}

// compact empties the journal (no call open: every line in it is settled). Under mu.
func (j *CallJournal) compact() {
	if err := j.f.Truncate(0); err != nil {
		return
	}
	if _, err := j.f.Seek(0, 0); err == nil {
		j.size = 0
	}
}

// Recover writes the line of every call a previous run left without one, then empties the journal. It runs once,
// before hello, with no other writer of the outbox; the events it appends are sent by the replay after the handshake.
func (j *CallJournal) Recover(box *Outbox, now int64) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	data, err := os.ReadFile(j.path)
	if err != nil {
		return 0, err
	}
	type call struct {
		ts     int64
		base   UsageRecord
		closed bool
		seq    int64
	}
	calls := map[int64]*call{}
	var order []int64
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // a line cut by a kill (the last one) or damaged: skipped
		}
		switch {
		case l.O != 0 && l.R != nil:
			if _, seen := calls[l.O]; !seen {
				order = append(order, l.O)
			}
			calls[l.O] = &call{ts: l.Ts, base: *l.R}
		case l.C != 0:
			if c := calls[l.C]; c != nil {
				c.closed, c.seq = true, l.Seq
			}
		case l.U != 0:
			if c := calls[l.U]; c != nil {
				c.closed, c.seq = false, 0
			}
		}
	}
	next := box.Next()
	n := 0
	for _, id := range order {
		c := calls[id]
		if c.closed && (c.seq == 0 || c.seq < next) {
			continue
		}
		rec := c.base
		msg := RecoveredError
		rec.Ts, rec.Ms, rec.Status, rec.Error, rec.Cost = c.ts, 0, 599, &msg, 0
		rec.Input, rec.Output, rec.Cached, rec.TraceID = nil, nil, nil, nil
		rec.TTFT = Nullable[float64]{}
		_, err := box.AppendSendNoted(TypeUsageReport, &UsageReport{Record: rec}, now,
			func(seq int64) error { return j.write(journalLine{C: id, Seq: seq}) }, func() {}, nil)
		if err != nil {
			return n, err
		}
		n++
		next = box.Next()
	}
	j.compact()
	if j.size != 0 {
		return n, errors.New("the call journal could not be emptied")
	}
	return n, nil
}

// Close closes the journal file.
func (j *CallJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
```

Note the ids: a new run starts at 1 again, which is safe because `Recover` empties the journal before the run opens
any call, and `main.go` calls `Recover` before it serves.

- [ ] **Step 3: Implement `Outbox.AppendSendNoted` in `gw/ipc/outbox.go`**

```go
// AppendSendNoted is AppendSend with note(seq) first, under the send lock, given the seq the event will take (P4f D14:
// the call journal's close). A note that fails stops it: nothing is appended (the call stays open in the journal, and
// the next start writes its line). An append that fails after the note runs undo, so the note does not claim a seq the
// event never took. send nil appends only (the journal's recovery, before hello).
func (o *Outbox) AppendSendNoted(typ string, body any, ts int64, note func(seq int64) error, undo func(), send func(line []byte) error) (int64, error) {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	o.mu.Lock()
	seq, closed := o.state.Next, o.closed
	o.mu.Unlock()
	if closed {
		return 0, ErrOutboxClosed
	}
	if err := note(seq); err != nil {
		return 0, err
	}
	line, got, err := o.append(typ, body, ts)
	if err != nil {
		undo()
		return 0, err
	}
	if got != seq {
		return got, fmt.Errorf("outbox: the event took seq %d, %d was noted", got, seq)
	}
	if send == nil {
		return seq, nil
	}
	return seq, send(line)
}
```

Add the `afterWrite func()` hook to `Outbox` (called after a segment line, a `stream.json` save and a segment
removal), nil in production.

T1-c in `gw/ipc/outbox_test.go`: `TestPruneDuringReplay`: three segments of 2 events; `Replay(1, send)` where `send`
blocks on a channel for the first line; meanwhile a goroutine calls `Ack(4)` (it waits for `mu`, never `sendMu`, and
deletes the first two segments); release the send; the replay must send all six lines in order or stop with an error
that names a missing segment, never send a line twice, and never panic. Pin what the code does today and fix it if the
replay can read a removed segment's handle.

Run: `go -C gw test ./ipc -race -count=1`. Expected: PASS.

- [ ] **Step 4: Thread the call id through the engine (`gw/engine/serve.go`)**

```go
// Calls is the call journal (gw/ipc CallJournal): a served call is opened before its provider call, and its usage line
// is emitted with its id, so the outbox notes the line's seq first (P4f D14). nil: no journal (tests).
type Calls interface {
	Open(ts int64, base ipc.UsageRecord) int64
	NoteClose(id, seq int64) error
}
```

`Engine` gains `calls Calls` and `SetCalls(c Calls)`; `emit` becomes `func(r *ipc.UsageReport, call int64) error`;
`func (e *Engine) open(p *Plan) int64` returns 0 without a journal. In `Serve`, after the rate limit passes and before
`e.upstream.Call`: `call := e.open(&p)`, and `e.emit(&ipc.UsageReport{Record: rec}, call)`. In `serveStream`, `call :=
e.open(p)` before `e.upstream.Start`, and the deferred emit passes it; where `line == nil` (no line will come) the
defer calls `e.calls.NoteClose(call, 0)` when `call != 0`. Update every test helper that calls `engine.New` to the new
`emit` signature (a recording `func(r *ipc.UsageReport, _ int64) error`).

- [ ] **Step 5: Recover before `hello` and emit with the call (`gw/cmd/zaafpulse-gw/main.go`)**

After `box.SetPerSegment(o.segmentEvents)`:

```go
	calls, err := ipc.OpenCallJournal(dir)
	if err != nil {
		return exitWith(logger, ipc.ExitDamaged, "%v", err)
	}
	defer calls.Close()
	if n, err := calls.Recover(box, time.Now().UnixMilli()); err != nil {
		return exitWith(logger, ipc.ExitDamaged, "the call journal: %v", err)
	} else if n > 0 {
		logger.Printf("wrote the usage line of %d call(s) the last gateway left in flight (status 599)", n)
	}
```

`gateway` gains `calls *ipc.CallJournal`; after `g.engine = engine.New(...)`, `g.engine.SetCalls(calls)`; and:

```go
// emitUsage stores a go-engine call in the outbox and sends it; with a call id, the journal notes the line's seq first
// under the send lock (P4f D14).
func (g *gateway) emitUsage(r *ipc.UsageReport, call int64) error {
	if call == 0 || g.calls == nil {
		return g.appendAndSend(ipc.TypeUsageReport, r)
	}
	_, err := g.box.AppendSendNoted(ipc.TypeUsageReport, r, time.Now().UnixMilli(),
		func(seq int64) error { return g.calls.NoteClose(call, seq) },
		func() { g.calls.Reopen(call) }, g.ch.SendLine)
	return err
}
```

`--emit` (the synthetic stream) does not use the journal.

- [ ] **Step 6: Kill tests through the server (`test/front-kill.test.js`, T6-2)**

Add the upstream model `slowwhole` to `test/contract/upstream.js` (`chat`): the whole answer after 3000 ms. Extend
`test/front-kill.test.js` with two cases under `gateway.engines: { chat: "go", "chat-stream": "go" }` (as
`ZP_ENGINE=go` seeds them), 5 seeded runs each (`ZP_KILL_SEED`):
- a call to `slowwhole`: kill the gateway (SIGKILL, `taskkill /F /T` on Windows) 500–2500 ms into the call, wait for
  the supervisor's restart, then assert exactly one usage line for that call (`status` 599, `error` "the gateway
  stopped before the call ended", `ipc` present), and that a call made after the restart has exactly one line (T6-2:
  not just presence);
- a call to `slowstream` (P4c): kill after the second chunk; the same assertions.

Run: `node --test test/front-kill.test.js` and under Deno. Expected: PASS, every run with exactly one line per call.

- [ ] **Step 7: Full checks, commit, push**

```bash
go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./... && test -z "$(gofmt -l gw)"
node --test test/front-kill.test.js test/ipc-kill.test.js test/ipc-hardening.test.js
deno test -q --no-check --no-prompt -P=test test/front-kill.test.js test/ipc-kill.test.js test/ipc-hardening.test.js
git add gw/ipc gw/engine gw/cmd test/contract/upstream.js test/front-kill.test.js
git commit -m "Journal each go-served call so a gateway crash leaves exactly one usage line (P4f T2)"
git push origin migrate/p4f-ipc-events
```

### Task 3: The control plane stores and exports go spans, and routes go with OTLP export on

**Files:**
- Modify: `lib/otel.js` (`exportRecord`, `status({ fresh })`, module exports)
- Modify: `lib/ipc/ingest.js` (`spanStoreOn`, `afterSpan`), `lib/ipc/server-sinks.js` (`traceOn`, `exportSpan`)
- Modify: `lib/front/index.js` (`routing`'s `otel` option), `lib/front/push.js` (`trace`, provider `span`)
- Modify: `server.js` (`frontIngest`, `frontRouting`, `sources.config`, `configChanged`, the minute check)
- Modify: `gw/ipc/messages.go` (`ConfigPush.Trace`), `gw/engine/chat.go` (`State.Spans`),
  `ipc/v1/config.push.schema.json` (+ fixture `valid-trace.json`)
- Test: `test/ipc-ingest.test.js`, `test/ipc-server-sinks.test.js`, `test/front.test.js`, `test/front-push.test.js`,
  `test/otel-export-record.test.js` (new)

**Interfaces:**
- Consumes: `providerSpanAttrs` (Task 1); `TraceSpanBody.OTLP` (Task 1).
- Produces (JS): `otel.exportRecord(span, extra) -> boolean`; `otel.status({ fresh }) -> status`;
  `createIngest({ ..., spanStoreOn?: () => boolean, afterSpan?: (span, otlp) => void })`;
  `serverSinks({ ..., traceOn: () => boolean, exportSpan: (span, otlp) => void })`;
  `routing(gateway, { gate, otel: boolean | { enabled, captureContent } })`;
  `configPush({ gateway, keys, pricing, trace? })`.
- Produces (Go): `ConfigPush.Trace *TraceSwitch`, `type TraceSwitch struct { Spans bool }`; `(*State).Spans() bool`.

- [ ] **Step 1: Failing tests for the ingest's span gate and export hook (`test/ipc-ingest.test.js`)**

```js
test('a trace.span while the store is off is skipped, marked and acknowledged, and still exported (P4f D9)', () => {
  const written = [];
  const exported = [];
  let storeOn = false;
  const ingest = createIngest({
    recordUsage: () => ({}),
    recordGovernance: () => ({}),
    recordMcp: () => {},
    writeSpans: (list) => written.push(...list),
    spanStoreOn: () => storeOn,
    afterSpan: (span, otlp) => exported.push([span.s, otlp]),
    log: () => {},
  });
  const span = { t: 'a'.repeat(32), s: 'b'.repeat(16), p: null, n: 'POST /v1/chat/completions', k: 'server', st: 1, d: 2, ok: true, src: 'gateway', svc: 'zaafpulse', a: {} };
  assert.equal(ingest.handle({ type: 'trace.span', seq: 1, body: { span } }, 'st1'), true);
  assert.deepEqual(written, []);
  assert.equal(ingest.mark('st1'), 1);
  storeOn = true;
  assert.equal(ingest.handle({ type: 'trace.span', seq: 2, body: { span: { ...span, s: 'c'.repeat(16) }, otlp: { remote: true } } }, 'st1'), true);
  assert.equal(written.length, 1);
  assert.deepEqual(written[0].ipc, { stream: 'st1', seq: 2 });
  assert.deepEqual(exported, [['b'.repeat(16), {}], ['c'.repeat(16), { remote: true }]]);
  // A resend at or below the mark is a duplicate: neither stored nor exported again.
  assert.equal(ingest.handle({ type: 'trace.span', seq: 2, body: { span } }, 'st1'), false);
  assert.equal(exported.length, 2);
});

test('an export hook that throws neither fails the event nor stops the next (P4f D9)', () => {
  const lines = [];
  const ingest = createIngest({
    recordUsage: () => ({}), recordGovernance: () => ({}), recordMcp: () => {},
    writeSpans: () => {}, afterSpan: () => { throw new Error('exporter down'); }, log: (l) => lines.push(l),
  });
  const span = { t: 'a'.repeat(32), s: 'b'.repeat(16), p: null, n: 'x', k: 'server', st: 1, d: 2, ok: true, src: 'gateway', svc: 'zaafpulse', a: {} };
  assert.equal(ingest.handle({ type: 'trace.span', seq: 1, body: { span } }, 's'), true);
  assert.equal(ingest.handle({ type: 'trace.span', seq: 2, body: { span } }, 's'), true);
  assert.equal(lines.length, 1);
});
```

Run: `node --test test/ipc-ingest.test.js`. Expected: FAIL (the first test writes the span while the store is off).

- [ ] **Step 2: Implement the gate and the hook in `lib/ipc/ingest.js`**

Add `spanStoreOn = () => true` and `afterSpan = () => {}` to the options (and the JSDoc). In `store()`, before
`writeSpans`:

```js
    // P4f D9: stored only where JS would store it (the store exists and tracing is on); otherwise skipped like the
    // recorder drops a span, and still marked and acknowledged (a span is not a ledger line).
    if (!spanStoreOn()) return null;
```

After `stats.stored++`, next to the `usage.report` hook:

```js
    if (msg.type === 'trace.span') {
      try {
        afterSpan(msg.body.span, msg.body.otlp || {});
      } catch (err) {
        once(
          `hook span ${causeOf(err)}`,
          `ipc ingest: the OTLP export of a stored trace.span failed (the span stays stored and acknowledged): ${err && err.message}`,
        );
      }
    }
```

Update the header comment's `trace.span` bullet with the gate and the hook. In `lib/ipc/server-sinks.js`, take
`traceOn = () => true` and `exportSpan = () => {}`, and return
`spanStoreOn: () => !!traceStore && traceOn()` and `afterSpan: (span, otlp) => exportSpan(span, otlp)`; the strict
`writeSpans` stays (it is only reached when the store is on). Test in `test/ipc-server-sinks.test.js`: a null store
with `traceOn` true gives `spanStoreOn() === false`.

Run: `node --test test/ipc-ingest.test.js test/ipc-server-sinks.test.js`. Expected: PASS.

- [ ] **Step 3: `otel.exportRecord` and `otel.status({ fresh })` with a lossless test**

`test/otel-export-record.test.js`:

```js
// A span the go engine stored and the control plane exports (P4f D11) is the OTLP span JS would have exported: a JS
// span is exported, its raw record (the sink's copy) becomes the stored record (toRecord) plus the trace.span otlp
// extras, exportRecord exports that again, and the two encoded spans are equal.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createOtel, KIND } = require('../lib/otel');
const { toRecord } = require('../lib/trace/recorder');

test('exportRecord exports a stored span exactly as JS exported it', async () => {
  const batches = [];
  const raw = [];
  const o = createOtel({
    getConfig: () => ({ otel: { enabled: true, endpoint: 'http://127.0.0.1:9', protocol: 'http/json' } }),
    refreshMs: 0,
    transport: async (url, body) => {
      batches.push(JSON.parse(String(body)));
      return { status: 200 };
    },
    sink: { enabled: () => true, add: (r) => raw.push(r) },
    log: () => {},
  });
  const parent = { traceId: '0af7651916cd43dd8448eb211c80319c', spanId: 'b7ad6b7169203331', sampled: true, traceState: 'a=1', remote: true };
  const s = o.startSpan('POST /v1/chat/completions', {
    kind: KIND.SERVER,
    parent,
    attributes: { 'http.response.status_code': 502, 'zaafpulse.cost.usd': 0, 'gen_ai.response.finish_reasons': ['stop'] },
  });
  s.fail('502', 'upstream said Bearer abcdefgh12345678');
  s.end();
  await o.flush(1000);
  const r = raw[0];
  const extra = { traceState: r.traceState, ...(r.flags & 0x200 ? { remote: true } : {}), message: r.status.message };
  assert.equal(o.exportRecord(toRecord(r), extra), true);
  await o.flush(1000);
  const span = (b) => b.resourceSpans[0].scopeSpans[0].spans[0];
  assert.equal(batches.length, 2);
  assert.deepEqual(span(batches[1]), span(batches[0]));
});

test('exportRecord is false and quiet while export is off', () => {
  const o = createOtel({ getConfig: () => ({}), log: () => {} });
  assert.equal(o.exportRecord({ t: 'a'.repeat(32), s: 'b'.repeat(16), p: null, n: 'x', k: 'server', st: 1, d: 1, ok: true, a: {} }), false);
});
```

In `lib/otel.js` `createOtel`, next to `record`:

```js
  // A span the go engine stored (IPC trace.span, P4f D11) to the OTLP exporter, as JS would have exported it: the
  // record's microseconds are the nanoseconds JS had divided by 1000 (nowNs and msToNs are whole microseconds), the
  // flags are sampled + "parent remote known" + the remote bit, the status message comes from the otlp extras
  // (err keeps only 200 units). Never throws; false when export is off.
  function exportRecord(span, extra = {}) {
    try {
      current();
      if (!exporter || !span) return false;
      const kind = span.k === 'server' ? KIND.SERVER : span.k === 'client' ? KIND.CLIENT : KIND.INTERNAL;
      exporter.add({
        traceId: span.t,
        spanId: span.s,
        parentSpanId: span.p || '',
        traceState: typeof extra.traceState === 'string' ? extra.traceState : '',
        flags: 0x101 | (extra.remote === true ? 0x200 : 0),
        name: span.n,
        kind,
        startNs: BigInt(span.st) * 1000n,
        endNs: BigInt(span.st + span.d) * 1000n,
        attributes: { ...(span.a || {}) },
        dropped: 0,
        status: span.ok
          ? { code: STATUS.UNSET, message: '' }
          : { code: STATUS.ERROR, message: mask(String(extra.message || '')).slice(0, 500) },
      });
      return true;
    } catch {
      return false;
    }
  }
```

`status` takes `{ fresh = false } = {}` and sets `checked = -Infinity` before `current()` when `fresh`. Return both
from `createOtel`, and add to `module.exports`:
`exportRecord: (span, extra) => (instance ? instance.exportRecord(span, extra) : false)` and
`status: (opts) => (instance ? instance.status(opts) : { enabled: false })`.

Run: `node --test test/otel-export-record.test.js test/otel*.test.js`. Expected: PASS.

- [ ] **Step 4: Routing with OTLP export on (`lib/front/index.js`, D10, D12)**

```js
function routing(gateway, { gate = () => ({ ok: true }), otel = false } = {}) {
  // OTLP export (P4f D10): the go engine sends its spans, so export no longer keeps a class on js; capturing content
  // still does (prompts and answers would wait in the gateway's outbox on disk).
  const o = otel && typeof otel === 'object' ? otel : { enabled: !!otel };
  ...
    if (o.enabled && o.captureContent === true)
      notes[c] = 'OpenTelemetry export captures content (captureContent): the go engine does not carry prompts or answers';
    else if (!g.ok) notes[c] = g.reason || 'the shadow gate is not met';
    else engines[c] = 'go';
```

Update the JSDoc (`otel?: boolean | { enabled: boolean, captureContent?: boolean }`) and the header comment. In
`server.js frontRouting`, pass `otel: otel.status({ fresh: true })`. Tests in `test/front.test.js`: export on without
`captureContent` → `go`; with it → `js` and the note; `otel: true` (the old boolean) → `go`.

- [ ] **Step 5: `trace` and the provider `span` in the push (`lib/front/push.js`, `server.js`, Go types)**

In `push.js`, `provider(p)` adds `span: providerSpanAttrs(p, str(p.id))` (import from `../otel`), and
`configPush({ gateway, keys, pricing, trace })` adds `...(trace ? { trace: { spans: trace.spans === true } } : {})`.
In `server.js`:

```js
// Whether a gateway span is recorded anywhere (P4f D8): the local store exists and tracing is on, or OTLP export is on
// (lib/otel.js live()). Pushed to the gateway with each generation.
const traceLive = () => (!!traceStore && tracingOn()) || otel.status({ fresh: true }).enabled === true;
let pushedTrace = null;
```

`sources.config`: `() => { pushedTrace = traceLive(); return configPush({ ..., trace: { spans: pushedTrace } }); }`.
`configChanged`: a new branch `else if (front && JSON.stringify((before || {}).tracing) !== JSON.stringify((after ||
{}).tracing)) front.push();` after the `otel` branch (both push). The minute check:
`if (front && !front.pushIfChanged(frontRouting().engines) && pushedTrace !== null && traceLive() !== pushedTrace) front.push();`.
`frontIngest` passes `traceOn: tracingOn` and `exportSpan: (span, x) => otel.exportRecord(span, x)` to `serverSinks`.

Go: `ConfigPush.Trace *TraceSwitch \`json:"trace,omitzero"\``, `type TraceSwitch struct { Spans bool
\`json:"spans"\` }`; `State` keeps `spans bool` from `NewState` (`c.Trace != nil && c.Trace.Spans`) and exposes
`func (s *State) Spans() bool`. Schema: `"trace": { "type": "object", "required": ["spans"], "additionalProperties":
false, "properties": { "spans": { "type": "boolean" } } }` and the fixture `ipc/v1/fixtures/config.push/valid-trace.json`.

Tests in `test/front-push.test.js`: each pushed provider's `span` equals `providerSpanAttrs` of the raw provider (an
IPv6 base URL, a custom preset, a credential URL whose pushed `baseUrl` is cut but whose `span` still comes from the
raw one); `trace` is pushed as given.

- [ ] **Step 6: Full checks, commit, push**

```bash
go -C gw vet ./... && go -C gw test -race -count=1 ./... && test -z "$(gofmt -l gw)"
node --test test/ipc-ingest.test.js test/ipc-server-sinks.test.js test/front.test.js test/front-push.test.js test/otel-export-record.test.js test/ipc-schema.test.js test/ipc-types.test.js
deno test -q --no-check --no-prompt -P=test test/ipc-ingest.test.js test/ipc-server-sinks.test.js test/front.test.js test/front-push.test.js test/otel-export-record.test.js test/ipc-schema.test.js test/ipc-types.test.js
git add lib/otel.js lib/ipc lib/front server.js gw/ipc gw/engine/chat.go ipc/v1 test
git commit -m "Store and export the gateway's spans where JS would, and route go with OTLP export on (P4f T3)"
git push origin migrate/p4f-ipc-events
```

### Task 4: The engine emits its spans and the usage line's `traceId`

**Files:**
- Modify: `gw/engine/serve.go` (`trace`, `spans`, `Serve`, `serveStream`, the 429), `gw/engine/serve_stream_test.go`,
  `gw/engine/chat_test.go` (a span-emitting engine)
- Modify: `gw/cmd/zaafpulse-gw/main.go` (`SetSpanEmitter`)
- Create: `test/contract/go-spans.test.js`

**Interfaces:**
- Consumes: `StartTrace`, `ServerSpan`, `ClientSpan`, `NewObserver`, `RandomIDs` (Task 1); `State.Spans()` (Task 3);
  `ProviderConfig.Span` (Task 1); the journal's `open` (Task 2).
- Produces: `(*Engine).SetSpanEmitter(func(*ipc.TraceSpanBody) error)`; `(*Engine).SetIDs(SpanIDs)` (tests).

- [ ] **Step 1: Failing engine test (`gw/engine/serve_spans_test.go`, new)**

```go
func TestServedCallEmitsServerThenClientSpan(t *testing.T) {
	up := fakeProvider(t, `{"id":"chatcmpl-9","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	e, emitted := spanEngine(t, up, true) // trace.spans true
	req := chatRequest(t, `{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}`)
	req.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	rr := httptest.NewRecorder()
	e.Serve("chat", rr, req, "/v1/chat/completions", noJS(t))
	usage, spans := emitted()
	if len(usage) != 1 || usage[0].Record.TraceID == nil || *usage[0].Record.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("usage traceId: %+v", usage)
	}
	if len(spans) != 2 || spans[0].Span.K != "server" || spans[1].Span.K != "client" {
		t.Fatalf("spans: %+v", spans)
	}
	if *spans[0].Span.P != "b7ad6b7169203331" || *spans[1].Span.P != spans[0].Span.S {
		t.Fatalf("parents: %+v", spans)
	}
	if !bytes.Contains(spans[1].Span.A, []byte(`"gen_ai.response.id":"chatcmpl-9"`)) {
		t.Fatalf("client attributes: %s", spans[1].Span.A)
	}
}
```

Plus: `TestUnsampledCallEmitsNoSpanAndNoTraceID` (`-00`); `TestSpansOffEmitsNothing` (`trace.spans` false: no span,
no `traceId`); `TestRateLimitedCallEmitsTheServerSpanOnly` (rpm 1, two calls: the second gives one server span with
`http.response.status_code` 429 and no client span); `TestStreamSpansFollowTheUsageLine` (a stream: the usage line,
then the server span with status 200, then the client span with the stream's id and finish reason, in that order on
the emitter); `TestClientGoneStillEmitsSpans` (the request context cancelled after the provider answered: both spans,
the server span with the answer's status). `spanEngine` wires a recording `emit` and `SetSpanEmitter`, and
`SetIDs(counterIDs())` from `spans_test.go`.

Run: `go -C gw test ./engine -run 'Span|Unsampled|RateLimitedCall'`. Expected: FAIL (`SetSpanEmitter` undefined).

- [ ] **Step 2: Implement the emission in `gw/engine/serve.go`**

```go
// trace starts a request's trace context when the engine answers it and a span is recorded somewhere (config.push
// trace.spans, P4f D8). false: no span and no traceId (tracing off, or the caller's traceparent is unsampled).
func (e *Engine) trace(s *State, r *http.Request) (TraceCtx, bool) {
	if !s.Spans() {
		return TraceCtx{}, false
	}
	tc := StartTrace(r.Header, e.ids)
	return tc, tc.Sampled
}

// spans emits a request's server span and, for a usage line, its client span, after the answer's last byte (P4f D1:
// the server span first, as JS records them when the response ends).
func (e *Engine) spans(s *State, tc TraceCtx, r *http.Request, path string, status int, started time.Time, ended time.Time,
	rec *ipc.UsageRecord, prov *ipc.ProviderSpan, obs *Observed) {
	if e.emitSpan == nil {
		return
	}
	ua := ""
	if v := r.Header.Values("User-Agent"); len(v) > 0 {
		ua = v[0] // Node keeps the first User-Agent and drops the others
	}
	out := []ipc.TraceSpanBody{ServerSpan(tc, ServerFacts{Method: r.Method, Path: path,
		Proto: strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor), TLS: r.TLS != nil, UserAgent: ua,
		Status: status, StartUs: started.UnixMicro(), EndUs: ended.UnixMicro()})}
	if rec != nil {
		out = append(out, ClientSpan(tc, e.ids(8), rec, prov, obs, s.mask))
	}
	for i := range out {
		if err := e.emitSpan(&out[i]); err != nil {
			e.logf("go engine: a trace span could not be stored: %v", err)
		}
	}
}
```

In `Serve`, after the hand-off decisions and before the rate limit: `tc, traced := e.trace(s, r)`. The 429:

```go
	if wait := e.limits.Take(p.Key.ID, p.Key.RPM, e.now()); wait > 0 {
		a := RateLimited(p.Key, wait)
		write(w, a)
		if traced {
			e.spans(s, tc, r, rawPath, a.Status, started, time.Now(), nil, nil, nil)
		}
		return
	}
```

The served call: set `rec.TraceID = &tc.TraceID` when `traced` before the emit; after `write(w, a)` (or the skipped
write when the client left):

```go
	if traced {
		obs := NewObserver()
		obs.Feed(a.Body) // what JS's observer sees: the body res.end writes, also to a client that left
		e.spans(s, tc, r, rawPath, a.Status, started, time.Now(), &rec, p.Target.Provider.Span, obs.Result())
	}
```

`serveStream(w, r, s, p, req, started, tc, traced)`: an `obs := NewObserver()` fed with each `out` it writes (and
`cs.End()`'s), a `status` variable (the failure's or the provider's whole answer's status, else 200), `ended :=
time.Now()` taken when the handler's writing is over (before the deferred goroutine), and in the deferred goroutine,
after the usage emit: `if traced { e.spans(s, tc, r, rawPath, status, started, ended, &rec, p.Target.Provider.Span,
obs.Result()) }`. The goroutine reads `r` only for its header, method, proto and TLS fields: copy those into locals
before the handler returns (net/http may reuse the request). `Engine` gains `emitSpan`, `ids SpanIDs` (default
`RandomIDs` in `New`), `SetSpanEmitter`, `SetIDs`.

In `main.go`, after `engine.New`: `g.engine.SetSpanEmitter(func(b *ipc.TraceSpanBody) error { return
g.appendAndSend(ipc.TypeTraceSpan, b) })`.

Run: `go -C gw test ./engine -race -count=1`. Expected: PASS.

- [ ] **Step 3: Contract test `test/contract/go-spans.test.js`**

```js
// Contract: the spans of a chat call (P4f). In every mode the trace store holds, for a call with a traceparent, the
// server span "POST /v1/chat/completions" (parent: the caller's span) and the client span "chat oa/m1" under it, and the
// usage line names the trace. Under ZP_ENGINE=go the spans come through the IPC ingest (their stored lines carry
// "ipc"), so the run proves the engine sent them.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { start } = require('./harness');

const GO = process.env.ZP_ENGINE === 'go';
let h;
test.before(async () => {
  h = await start();
  await h.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
});
test.after(async () => {
  if (h) await h.stop();
});

function storedSpans(traceId) {
  const dir = path.join(h.server.data, '.data', 'traces');
  const out = [];
  for (const f of fs.existsSync(dir) ? fs.readdirSync(dir).filter((x) => x.endsWith('.ndjson')) : [])
    for (const l of fs.readFileSync(path.join(dir, f), 'utf8').split('\n'))
      if (l.includes(traceId)) out.push(JSON.parse(l));
  return out;
}

async function waitFor(fn, ms = 5000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timed out');
    await new Promise((r) => setTimeout(r, 50));
  }
}

test('a traced chat call stores its server and client spans and names its trace on the usage line', async () => {
  const traceId = '4bf92f3577b34da6a3ce929d0e0e4736';
  const r = await h.call('/v1/chat/completions', {
    key: h.key,
    body: { model: 'oa/m1', messages: [{ role: 'user', content: 'hi' }] },
    headers: { traceparent: `00-${traceId}-00f067aa0ba902b7-01` },
  });
  assert.equal(r.status, 200);
  // the recorder writes JS spans within a second; the ingest writes go spans at once
  const spans = await waitFor(() => {
    const s = storedSpans(traceId);
    return s.length >= 2 ? s : null;
  });
  const server = spans.find((s) => s.k === 'server');
  const client = spans.find((s) => s.k === 'client');
  assert.equal(server.n, 'POST /v1/chat/completions');
  assert.equal(server.p, '00f067aa0ba902b7');
  assert.equal(client.n, 'chat oa/m1');
  assert.equal(client.p, server.s);
  assert.equal(client.a['gen_ai.request.model'], 'oa/m1');
  if (GO) assert.ok(server.ipc && client.ipc, 'the go engine sent both spans');
  const line = (await h.logs()).find((l) => l.traceId === traceId);
  assert.ok(line, 'the usage line names the trace');
});
```

Run: `node --test test/contract/go-spans.test.js`, `ZP_ENGINE=front …`, `ZP_ENGINE=go …`, and the same under Deno.
Expected: PASS in all modes.

- [ ] **Step 4: Full checks, commit, push**

```bash
go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./... && test -z "$(gofmt -l gw)"
ZP_ENGINE=go node --test --test-concurrency=1 test/contract/*.test.js
ZP_ENGINE=shadow node --test --test-concurrency=1 test/contract/*.test.js
git add gw/engine gw/cmd test/contract/go-spans.test.js
git commit -m "Emit the go engine's server and client spans and the usage line's trace id (P4f T4)"
git push origin migrate/p4f-ipc-events
```

### Task 5: One rate-limit window across the engines

**Files:**
- Create: `ipc/v1/ratelimit.take.schema.json`, `ipc/v1/ratelimit.window.schema.json`, fixtures
  `ipc/v1/fixtures/ratelimit.take/{valid-take,invalid-no-key}.json`,
  `ipc/v1/fixtures/ratelimit.window/{valid-window,invalid-at-string}.json`; `ipc/v1/README.md` rows
- Modify: `gw/ipc/messages.go` (types, `TypeRateLimitTake`, `TypeRateLimitWindow`, decode table)
- Modify: `gw/engine/keys.go` (`RateLimiter.Note`, `Seed`), `gw/engine/serve.go` (`onTake`), `gw/engine/keys_test.go`
- Modify: `gw/cmd/zaafpulse-gw/main.go` (handle both types; send a take)
- Modify: `lib/gateway/keys.js` (`onTake`, `note`, `windows`), `lib/gateway/index.js` (`onRateTake`, `noteRateTake`,
  `rateWindows`), `lib/ipc/session.js`, `lib/ipc/supervisor.js`, `lib/front/index.js` (`sendControl`), `server.js`
- Modify: `lib/ipc/schema.js` (`EVENTS` unchanged; the two types known), `scripts/build-deno.js`,
  `scripts/smoke-binary.js` (17 schemas)
- Test: `test/ratelimit-window.test.js` (new), `test/contract/go-ratelimit.test.js` (new), `gw/engine/keys_test.go`

**Interfaces:**
- Produces (IPC): `ratelimit.take {keyId: string (minLength 1), at: integer ≥ 0}` both directions;
  `ratelimit.window {windows: [{keyId, at: integer[]}]}` cp → gw.
- Produces (JS): `createKeys(store, { now, onTake })`; `keys.note(keyId, at)`; `keys.windows() -> [{keyId, at}]`
  (each the newest `rpm` takes); `gateway.onRateTake(fn)`, `gateway.noteRateTake(keyId, at)`,
  `gateway.rateWindows()`; `front.sendControl(type, body) -> boolean`; supervisor option
  `onControl(msg)` and source `window()`.
- Produces (Go): `(*RateLimiter).Note(id string, at int64)`, `(*RateLimiter).Seed(id string, at []int64)`,
  `(*Engine).SetOnTake(func(keyID string, at int64))`, `(*Engine).NoteTake(id string, at int64)`,
  `(*Engine).SeedWindows([]ipc.RateWindow)`.

- [ ] **Step 1: Failing JS test `test/ratelimit-window.test.js`**

```js
// One window per key across the engines (P4f D16): each side counts the takes it admits and the takes the other side
// tells it about; refused requests are not takes.
const test = require('node:test');
const assert = require('node:assert/strict');
const { createKeys } = require('../lib/gateway/keys');

function store() {
  let data = { keys: [] };
  return { get: () => data, save: () => {}, set: (d) => (data = d) };
}

test('a take noted from the other engine counts in the window, in time order', () => {
  let t = 1_000_000;
  const told = [];
  const keys = createKeys(store(), { now: () => t, onTake: (id, at) => told.push([id, at]) });
  const k = { id: 'k1', rpm: 3 };
  assert.equal(keys.rateLimit(k), 0);
  keys.note('k1', t - 10); // the go engine admitted one 10 ms earlier
  keys.note('k1', t + 5);
  t += 20;
  assert.equal(keys.rateLimit(k), 60); // three in the last minute: refused, the oldest is 30 ms old -> ceil(59.97)
  assert.deepEqual(told, [['k1', 1_000_000]]); // only its own admitted take is told
  assert.deepEqual(keys.windows(), [{ keyId: 'k1', at: [999_990, 1_000_000, 1_000_005] }]);
});

test('windows() keeps the newest rpm takes per key and drops takes older than a minute', () => {
  let t = 0;
  const keys = createKeys(store(), { now: () => t });
  for (let i = 0; i < 5; i++) keys.note('k', 100 + i);
  t = 100 + 60_000 + 2; // the first three are older than a minute
  assert.deepEqual(keys.windows(), [{ keyId: 'k', at: [103, 104] }]);
});
```

`windows()` cuts each key to its newest `rpm` takes when the key is known (the store's `rpm`, default 60), else to
10,000 (the largest `rpm`, `keys.js:72`).

Run: `node --test test/ratelimit-window.test.js`. Expected: FAIL (`keys.note` is not a function).

- [ ] **Step 2: Implement in `lib/gateway/keys.js`**

```js
function createKeys(store, { now = Date.now, onTake = () => {} } = {}) {
  ...
  // A take is inserted in time order: the other engine's takes arrive a moment late (P4f D16).
  function insert(id, at) {
    const w = (windows.get(id) || []).filter((x) => now() - x < 60000);
    let i = w.length;
    while (i > 0 && w[i - 1] > at) i--;
    w.splice(i, 0, at);
    windows.set(id, w);
  }
```

`rateLimit(k)`: unchanged decision; on admit, after `windows.set(k.id, w)`, `try { onTake(k.id, t) } catch { /* the
other engine misses one take */ }`. New methods:
`note(id, at)` (ignores a non-string id or a non-finite `at`; then `insert`), and
`windows()` as the test pins (sorted by key id for a stable message). The window state stays in memory, as before.

In `lib/gateway/index.js`: `let rateTake = () => {}; const keys = createKeys(keyStore, { now, onTake: (id, at) =>
rateTake(id, at) });` and on the returned gateway object: `onRateTake(fn) { rateTake = typeof fn === 'function' ? fn :
() => {}; }`, `noteRateTake: (id, at) => keys.note(id, at)`, `rateWindows: () => keys.windows()`.

Run: `node --test test/ratelimit-window.test.js test/gateway*.test.js`. Expected: PASS.

- [ ] **Step 3: The messages (schemas, Go types, JS session)**

`ipc/v1/ratelimit.take.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "ratelimit.take (both directions, P4f): one request admitted under a key's requests-per-minute window",
  "type": "object",
  "required": ["keyId", "at"],
  "additionalProperties": false,
  "properties": {
    "keyId": { "type": "string", "minLength": 1 },
    "at": { "type": "integer", "minimum": 0 }
  }
}
```

`ipc/v1/ratelimit.window.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "ratelimit.window (control plane to gateway, P4f): the takes of the last minute per key, sent before budget.snapshot",
  "type": "object",
  "required": ["windows"],
  "additionalProperties": false,
  "properties": {
    "windows": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["keyId", "at"],
        "additionalProperties": false,
        "properties": {
          "keyId": { "type": "string", "minLength": 1 },
          "at": { "type": "array", "items": { "type": "integer", "minimum": 0 } }
        }
      }
    }
  }
}
```

Go:

```go
const (
	TypeRateLimitTake   = "ratelimit.take"
	TypeRateLimitWindow = "ratelimit.window"
)

// RateLimitTake is one request admitted under a key's window (both directions; a control message, P4f D16).
type RateLimitTake struct {
	KeyID string `json:"keyId" ipc:"minlen=1"`
	At    int64  `json:"at" ipc:"min=0"`
}

// RateLimitWindow seeds a new gateway with the last minute's takes per key.
type RateLimitWindow struct {
	Windows []RateWindow `json:"windows"`
}

type RateWindow struct {
	KeyID string  `json:"keyId" ipc:"minlen=1"`
	At    []int64 `json:"at"`
}
```

Register both in the decode table next to `TypeAck`. JS: the session's `onMessage` gets
`else if (msg.type === 'ratelimit.take') onControl(msg);` (a control message: never acknowledged, never stored);
`startSession` takes `onControl = () => {}` and `initial.window` (a list of bodies); in the hello branch, between
`secrets.handoff` and `budget.snapshot`: `for (const w of initial.window || []) send('ratelimit.window', w);`. The
supervisor takes `sources.window` (returns the list) and `onControl`, and passes them to each session; `lib/front`
exposes `sendControl(type, body)`, which sends on the current session when it is ready and returns false otherwise.

`server.js`:

```js
// One rate-limit window across the engines (P4f D16): a take JS admits goes to the gateway, a take the gateway admits
// comes back (sup onControl), and a new gateway starts with the window (sources.window), split under 1 MiB a message.
gateway.onRateTake((keyId, at) => front && front.sendControl('ratelimit.take', { keyId, at }));
```

with `sources.window: () => windowBodies(gateway.rateWindows())` (a helper in `lib/front/push.js` that packs windows
into bodies of at most 1 MiB of JSON) and `onControl: (msg) => gateway.noteRateTake(msg.body.keyId, msg.body.at)`.

- [ ] **Step 4: The Go side (`gw/engine/keys.go`, `serve.go`, `main.go`)**

```go
// Note counts a take the control plane admitted (P4f D16), in time order; takes older than a minute are dropped.
func (r *RateLimiter) Note(id string, at int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.windows[id]
	i := len(w)
	for i > 0 && w[i-1] > at {
		i--
	}
	w = append(w, 0)
	copy(w[i+1:], w[i:])
	w[i] = at
	r.windows[id] = w
}

// Seed replaces a key's window with the control plane's (a new gateway session).
func (r *RateLimiter) Seed(id string, at []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := append([]int64(nil), at...)
	slices.Sort(w)
	r.windows[id] = w
}
```

`take` already filters entries older than a minute. In `Serve`, after `Take` returns 0: `if e.onTake != nil {
e.onTake(p.Key.ID, now) }` with `now` the value passed to `Take`. In `main.go`, `g.engine.SetOnTake(func(id string, at
int64) { _ = g.ch.Send(ipc.TypeRateLimitTake, ipc.RateLimitTake{KeyID: id, At: at}, "", "") })` and two `handle`
cases: `*ipc.RateLimitTake` → `g.engine.NoteTake(b.KeyID, b.At)`; `*ipc.RateLimitWindow` → `g.engine.SeedWindows(b.Windows)`.
Go tests: `TestNoteKeepsTimeOrder`, `TestSeedThenTake` (a seeded window of `rpm` entries refuses the next take with
JS's wait).

- [ ] **Step 5: Contract test `test/contract/go-ratelimit.test.js`**

```js
// One window across the engines (P4f D16). A key with rpm 3: chat (the go engine under ZP_ENGINE=go), embeddings (the
// server), chat, then a fourth call to either is refused with 429, in every mode.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start } = require('./harness');

let h;
test.before(async () => {
  h = await start();
  await h.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
});
test.after(async () => {
  if (h) await h.stop();
});

test('a key with rpm 3 is refused on its fourth request, whichever engine answered the first three', async () => {
  const k = await h.newKey({ scopes: ['llm'], rpm: 3 });
  const chat = () => h.call('/v1/chat/completions', { key: k.secret, body: { model: 'oa/m1', messages: [{ role: 'user', content: 'hi' }] } });
  const embed = () => h.call('/v1/embeddings', { key: k.secret, body: { model: 'oa/m1', input: 'hi' } });
  assert.equal((await chat()).status, 200);
  await new Promise((r) => setTimeout(r, 50)); // one IPC hop for the take
  assert.equal((await embed()).status, 200);
  await new Promise((r) => setTimeout(r, 50));
  assert.equal((await chat()).status, 200);
  await new Promise((r) => setTimeout(r, 50));
  assert.equal((await chat()).status, 429);
  assert.equal((await embed()).status, 429);
});
```

Run in `js`, `front` and `go` modes, Node and Deno. Expected: PASS everywhere (in `go` mode it failed before this
task: each engine had its own window).

- [ ] **Step 6: Bookkeeping, full checks, commit, push**

`scripts/build-deno.js` lists `ipc/v1/ratelimit.take.schema.json` and `ipc/v1/ratelimit.window.schema.json`;
`scripts/smoke-binary.js` expects `Gateway IPC  v1, 17 schemas`; `ipc/v1/README.md` gets the two rows and a
paragraph "One rate-limit window (added in v1 by P4f)".

```bash
go -C gw vet ./... && GOOS=windows go -C gw vet ./... && go -C gw test -race -count=1 ./... && test -z "$(gofmt -l gw)"
node --test test/ratelimit-window.test.js test/ipc-*.test.js test/build-deno.test.js
ZP_ENGINE=go node --test test/contract/go-ratelimit.test.js
deno test -q --no-check --no-prompt -P=test test/ratelimit-window.test.js test/ipc-session.test.js test/ipc-supervisor.test.js
git add ipc/v1 gw lib server.js scripts test
git commit -m "Share one rate-limit window between the JS gateway and the go engine (P4f T5)"
git push origin migrate/p4f-ipc-events
```

### Task 6: The S8 equality test: a `js` run and a `go` run, every store diffed

**Files:**
- Create: `test/s8-equality.test.js`
- Modify: `test/contract/harness.js` (export `providerList`, no behaviour change)

**Interfaces:**
- Consumes: everything above; the harness's `startServer({ config })`, `clientFor(base)`, `providerList(url)`,
  `PRICES`; `test/contract/upstream.js startUpstream`; `lib/money.js toNano`.
- Produces: the S8 exit evidence. It runs in the existing Node `test` job (four OS) and the Deno job (two OS), which
  run `test/*.test.js` and have Go on the path (as the kill tests); no workflow change.

- [ ] **Step 1: Write the test**

```js
// The S8 proof (P4f D18, program plan §5 S8: "Ledger and audit identical to JS for the same run"). The same calls go
// through a server with every class on js and a server with chat and chat-stream on the go engine, each on a fresh
// data folder, each exporting OTLP (JSON) to a receiver in this test. Then every store is normalized and diffed: the
// usage log, the cost ledger, the governance chain, the MCP audit log, the trace store and the OTLP export.
// Normalized: ids made by either run (keys, span ids, fresh trace ids) become labels in order of first appearance;
// times become markers (ts, ms, ttft, st, d); money is compared at nano-USD (D17); the ipc mark is dropped (it only says
// which engine wrote the line, and the go run must have it on every served call: asserted separately).
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const path = require('path');
const { startServer, clientFor, providerList, PRICES } = require('./contract/harness');
const { startUpstream } = require('./contract/upstream');
const { toNano } = require('../lib/money');
const { buildStub } = require('../scripts/ipc/build-stub');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hex = (i, n) => i.toString(16).padStart(n, '0');
const tp = (i) => `00-${hex(0xa000 + i, 32)}-${hex(0xb000 + i, 16)}-01`;

function receiver() {
  const spans = [];
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      try {
        for (const rs of JSON.parse(body).resourceSpans || [])
          for (const ss of rs.scopeSpans || []) spans.push(...(ss.spans || []));
      } catch {
        /* not JSON */
      }
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end('{}');
    });
  });
  return new Promise((resolve) =>
    server.listen(0, '127.0.0.1', () => resolve({ url: `http://127.0.0.1:${server.address().port}`, spans, close: () => new Promise((r) => server.close(r)) })),
  );
}

const user = [{ role: 'user', content: 'hi' }];
// One corpus, in order. `trace`: the index of a fixed traceparent, or null (a fresh root), or 'off' (unsampled).
const CORPUS = [
  { name: 'whole', trace: 1, body: { model: 'oa/m1', messages: user } },
  { name: 'fresh root', trace: null, body: { model: 'oa/m1', messages: user } },
  { name: 'unsampled', trace: 'off', body: { model: 'oa/m1', messages: user } },
  { name: 'stream with usage', trace: 4, body: { model: 'oa/m1', messages: user, stream: true, stream_options: { include_usage: true } } },
  { name: 'stream', trace: 5, body: { model: 'oa/m1', messages: user, stream: true } },
  { name: 'private provider', trace: 6, body: { model: 'pv/m1', messages: user } },
  { name: 'provider 4xx', trace: 7, body: { model: 'oa/fail-400', messages: user } },
  { name: 'provider 5xx', trace: 8, body: { model: 'oa/fail-500', messages: user } },
  { name: 'network error', trace: 9, body: { model: 'dead/m1', messages: user } },
  { name: 'claude-code client', trace: 10, body: { model: 'oa/m1', messages: user }, headers: { 'user-agent': 'claude-cli/2.0.0 (external)' } },
  { name: 'rate limited 1', trace: 11, key: 'rate', body: { model: 'oa/m1', messages: user } },
  { name: 'rate limited 2 (429)', trace: 12, key: 'rate', body: { model: 'oa/m1', messages: user } },
  { name: 'hand-off: a route', trace: 13, body: { model: 'smart', messages: user } },
  { name: 'embeddings (js in both)', trace: 14, path: '/v1/embeddings', body: { model: 'oa/m1', input: 'hi' } },
];

async function runOnce(engine, otlpUrl, upstream) {
  const config = {
    pricingSync: { enabled: false },
    pricing: PRICES,
    otel: { enabled: true, endpoint: otlpUrl, protocol: 'http/json' },
    ...(engine === 'go'
      ? { gateway: { engine: 'front', engines: { chat: 'go', 'chat-stream': 'go' }, shadowDays: 0, shadowMinCompares: 0 } }
      : {}),
  };
  const server = await startServer({ config });
  try {
    const c = clientFor(server.base);
    for (const p of providerList(upstream.url)) await c.admin('providers', { action: 'save', ...p });
    await c.admin('routes', { action: 'save', name: 'smart', targets: ['oa/m1'] });
    await c.setGuardrails({ scope: 'external', secrets: 'off', pii: 'off', injection: 'off' });
    const keys = { main: await c.newKey({ scopes: ['llm'] }), rate: await c.newKey({ scopes: ['llm'], rpm: 1 }) };
    const statuses = [];
    for (const k of CORPUS) {
      const headers = { ...(k.headers || {}) };
      if (typeof k.trace === 'number') headers.traceparent = tp(k.trace);
      if (k.trace === 'off') headers.traceparent = tp(3).replace(/-01$/, '-00');
      const r = await c.call(k.path || '/v1/chat/completions', { key: keys[k.key || 'main'].secret, body: k.body, headers });
      statuses.push([k.name, r.status]);
      await sleep(100); // the ingest stores a go call's events a few ms after the answer
    }
    await sleep(1500); // the JS recorder writes its spans once a second
    await c.call('/api/rescan', { method: 'POST', body: {} }); // one ledger pass (costTick)
    const ledger = await waitForLedger(c);
    const stores = readStores(server.data, keys);
    await waitForOtlp(); // the exporter's batch delay
    return { statuses, ledger, ...stores, keys };
  } finally {
    await server.stop();
  }
}
```

The rest of the file, each a plain function with a unit of its own:
- `waitForLedger(c)`: polls `GET /api/cost/ledger` every 200 ms (at most 10 s) until a row with source `gateway`
  appears, then returns the rows with `source === 'gateway'`, each amount through `toNano`.
- `readStores(data, keys)`: reads `<data>/.data/gateway-usage.jsonl`, every `<data>/.data/audit/*.jsonl` line,
  `<data>/.cache/mcp-audit.jsonl` (empty when missing) and every `<data>/.data/traces/*.ndjson` line, as parsed JSON.
- `waitForOtlp()`: polls the receiver until its span count has not changed for 2 s (at most 20 s).
- `normalize(run)`: one label map per run. Key ids → `key:main`, `key:rate`; trace ids: the fixed ones stay, the
  others (fresh roots) become `trace:<n>` in the order of the usage lines that name them; span ids become `span:<n>`
  in the order of (trace label, kind server then client); then:
  - usage lines: drop `ipc`; `ts`, `ms`, `ttft` → `'<time>'` when present; `cost` → `toNano(cost)`; ids relabelled;
  - governance entries: drop `ts`, `hmac`, `prev`, `seq`, `id`; ids relabelled (key targets);
  - MCP audit lines: drop `ts`, `ms`;
  - trace records: drop `ipc`; `st`, `d` → `'<time>'`; `t`, `s`, `p` relabelled; `a['zaafpulse.cost.usd']` →
    `toNano`; only records of the corpus' traces (the admin calls make no span: no child, not a model endpoint);
    sorted by (trace label, kind);
  - OTLP spans: the same relabelling on `traceId`, `spanId`, `parentSpanId`; `startTimeUnixNano`,
    `endTimeUnixNano` → `'<time>'`; the cost attribute's `doubleValue` → `toNano`; sorted like the records.
- Before relabelling, a check per run: every client span lies inside its server span (`st` and `st + d`), and in the
  go run every usage line of a call the engine served (whole, fresh root, unsampled, streams, private, 4xx, 5xx,
  network error, claude-code, rate limited 1) carries `ipc`.

```js
test('S8: the same calls through js and through go leave identical stores (P4f D18)', { timeout: 180000 }, async (t) => {
  const built = buildStub();
  if (built.skip) {
    if (process.env.CI) throw new Error(`the go gateway is needed in CI: ${built.skip}`);
    return t.skip(built.skip);
  }
  const upstream = await startUpstream();
  const otlpJs = await receiver();
  const otlpGo = await receiver();
  try {
    const js = await runOnce('js', otlpJs.url, upstream);
    const go = await runOnce('go', otlpGo.url, upstream);
    js.otlp = otlpJs.spans;
    go.otlp = otlpGo.spans;
    assert.deepEqual(go.statuses, js.statuses, 'the same answers');
    assertServedByGo(go);
    const a = normalize(js);
    const b = normalize(go);
    for (const store of ['usage', 'ledger', 'governance', 'mcp', 'traces', 'otlp'])
      assert.deepEqual(b[store], a[store], `${store}: the go run differs from the js run`);
  } finally {
    await otlpJs.close();
    await otlpGo.close();
    await upstream.close();
  }
});
```

Check the upstream's models before running: `oa/fail-400` and `oa/fail-500` must exist in `test/contract/upstream.js
failure(model)` (add them if the names differ, as a test-only change), and `dead/m1` is the `dead` provider of
`providerList` (nothing listens).

- [ ] **Step 2: Run it on Node and Deno**

Run: `node --test test/s8-equality.test.js` and `deno test -q --no-check --no-prompt -P=test test/s8-equality.test.js`.
Expected: PASS. A diff names its store and shows both sides; fix the engine (or the port) until the diff is empty. A
difference that is a recorded deviation (the list at the top) is not in this corpus; if one appears, the corpus case is
wrong, not the normalizer: never normalize a real difference away.

- [ ] **Step 3: The full suites (the once-per-plan run of the global constraints)**

```bash
export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"
node --test test/*.test.js 2>&1 | tail -5
deno test -q --no-check --no-prompt -P=test test/ 2>&1 | tail -5
for m in js front shadow go; do ZP_ENGINE=$m npm run test:contract 2>&1 | tail -3; done
```

Expected: green; the known load flakes of P4c (they pass alone) are listed with their names, not ignored.

- [ ] **Step 4: Commit, push, full matrix**

```bash
git add test/s8-equality.test.js test/contract/harness.js test/contract/upstream.js
git commit -m "Prove S8: a js run and a go run leave the same usage log, ledger, audit chain, trace store and OTLP export (P4f T6)"
git push origin migrate/p4f-ipc-events
gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref migrate/p4f-ipc-events
```

### Task 7: The acker retries a failed mark write (P4a T2-f)

**Files:**
- Modify: `lib/ipc/acker.js`
- Test: `test/ipc-acker.test.js` (new; or the acker cases of `test/ipc-hardening.test.js`)

**Interfaces:**
- Consumes: `createAcker({ send, everyMs, max, setTimer, clearTimer })` (unchanged signature).
- Produces: a failed `send` (the mark file could not be written) is tried again after `everyMs`, also when no new event
  arrives.

- [ ] **Step 1: The failing test**

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const { createAcker } = require('../lib/ipc/acker');

test('a failed ack is tried again on the next tick without a new event (P4a T2-f)', () => {
  const timers = [];
  const setTimer = (fn) => (timers.push(fn), timers.length);
  let fails = 1;
  const sent = [];
  const acker = createAcker({
    send: (b) => {
      if (fails-- > 0) throw new Error('mark file');
      sent.push(b);
    },
    setTimer,
    clearTimer: () => {},
  });
  acker.note('s', 3);
  timers.shift()(); // the first flush fails
  assert.deepEqual(sent, []);
  assert.equal(timers.length, 1, 'a retry is scheduled');
  timers.shift()();
  assert.deepEqual(sent, [{ stream: 's', upTo: 3 }]);
});
```

Run: `node --test test/ipc-acker.test.js`. Expected: FAIL (`a retry is scheduled`: 0 timers).

- [ ] **Step 2: Implement**

In `flush`'s catch:

```js
      } catch {
        // Not acknowledged (the mark file could not be written): tried again after everyMs, also when no other event
        // comes (P4a T2-f: before, an idle acker never retried and the gateway's outbox kept the events until the next).
        if (!timer) {
          timer = setTimer(flush, everyMs);
          if (timer && timer.unref) timer.unref();
        }
      }
```

`stop()` already clears the timer. Run the acker test, `test/ipc-hardening.test.js` and `test/ipc-session.test.js` on
Node and Deno. Expected: PASS.

- [ ] **Step 3: Commit, push**

```bash
git add lib/ipc/acker.js test/ipc-acker.test.js
git commit -m "Retry a failed acknowledgement on the next tick when no event follows (P4a T2-f)"
git push origin migrate/p4f-ipc-events
```

### Task 9: One governance entry per unchecked-budget alert window (D24, owner decision, design rule 2)

Runs after P4e merges (P4e changes how unchecked calls reach the ingest; this task only hooks the supervisor's alert).

**Files:**
- Modify: `lib/ipc/supervisor.js` (`noteUnchecked`, option `onUncheckedAlert`, header comment)
- Modify: `lib/front/index.js` (pass `onUncheckedAlert` through to the supervisor), `server.js` (the `govRecord` call)
- Test: `test/ipc-supervisor.test.js`, `test/front-server.test.js` (or a new `test/front-unchecked-audit.test.js`)

**Interfaces:**
- Consumes: `createSupervisor` (unchanged otherwise), `govRecord(entry)` in `server.js`.
- Produces: supervisor option `onUncheckedAlert({ calls: number, since: number, at: number })`, called once per alert
  window (at most every `ALERT_EVERY_MS`, 60 s), never throwing into the ingest path; the governance entry
  `{ action: 'gateway.budget_unchecked', outcome: 'error', target: { type: 'gateway' }, actor: { via: 'system' },
  calls, since: <ISO> }`.

- [ ] **Step 1: The failing supervisor test (`test/ipc-supervisor.test.js`)**

```js
test('one unchecked-budget audit call per alert window, with the calls since the last one (P4f D24)', async (t) => {
  const { sessions, start } = fakeStart();
  const clock = { t: 1000 };
  const alerts = [];
  const ingest = createIngest({ recordUsage: () => {}, recordGovernance: () => {}, recordMcp: () => {}, writeSpans: () => {}, log: () => {} });
  const sup = createSupervisor(fakeOpts(start, [], clock, { ingest, onUncheckedAlert: (a) => alerts.push(a) }));
  t.after(() => stopFake(sup, sessions));
  sup.start();
  const base = JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', 'usage.report', 'valid-unchecked.json'), 'utf8'));
  const at = (seq, t) => {
    clock.t = t;
    sessions[0].opts.onEvent({ ...base, seq }, 'gw-1');
  };
  at(1, 1000); // opens a window: one entry at once
  at(2, 20_000);
  at(3, 40_000);
  assert.deepEqual(alerts, [{ calls: 1, since: 1000, at: 1000 }]);
  at(4, 61_000); // the next window: the calls since the last entry, from the first of them
  assert.deepEqual(alerts[1], { calls: 3, since: 20_000, at: 61_000 });
  at(4, 62_000); // a resend: a duplicate, not counted
  at(5, 200_000);
  assert.deepEqual(alerts[2], { calls: 1, since: 200_000, at: 200_000 });
  assert.equal(alerts.length, 3);
});

test('an unchecked-alert hook that throws changes nothing for the ingest (P4f D24)', async (t) => {
  const { sessions, start } = fakeStart();
  const logs = [];
  const ingest = createIngest({ recordUsage: () => {}, recordGovernance: () => {}, recordMcp: () => {}, writeSpans: () => {}, log: () => {} });
  const sup = createSupervisor(fakeOpts(start, logs, { t: 5 }, { ingest, onUncheckedAlert: () => { throw new Error('audit down'); } }));
  t.after(() => stopFake(sup, sessions));
  sup.start();
  const msg = JSON.parse(fs.readFileSync(path.join(DIR, 'fixtures', 'usage.report', 'valid-unchecked.json'), 'utf8'));
  sessions[0].opts.onEvent(msg, 'gw-1');
  assert.equal(sup.status().unchecked.count, 1);
  assert.ok(logs.some((l) => l.includes('audit down')));
});
```

(`fakeOpts(start, logs, clock, extra)` spreads `extra` into the options, as the existing tests do; the event's `seq`
is set per call so the ingest does not drop the later ones as duplicates.)

Run: `node --test test/ipc-supervisor.test.js`. Expected: FAIL (`alerts` stays empty).

- [ ] **Step 2: Implement in `lib/ipc/supervisor.js`**

```js
  const unchecked = { count: 0, lastAt: null, alertedAt: -Infinity, pending: 0, pendingSince: null };
  function noteUnchecked() {
    unchecked.count++;
    unchecked.lastAt = now();
    unchecked.pending++;
    if (unchecked.pendingSince === null) unchecked.pendingSince = unchecked.lastAt;
    if (unchecked.lastAt - unchecked.alertedAt >= ALERT_EVERY_MS) {
      unchecked.alertedAt = unchecked.lastAt;
      log(`gw: alert: ${unchecked.count} metered calls passed without a budget check (the budget snapshot was stale)`);
      // Design rule 2 (P4f D24): the alert window is also one governance entry, written by the control plane.
      const info = { calls: unchecked.pending, since: unchecked.pendingSince, at: unchecked.lastAt };
      unchecked.pending = 0;
      unchecked.pendingSince = null;
      try {
        onUncheckedAlert(info);
      } catch (err) {
        log(`gw: the unchecked-budget audit entry could not be written: ${err && err.message}`);
      }
    }
  }
```

Add `onUncheckedAlert = () => {}` to the options and its JSDoc; `status().unchecked` keeps its fields (do not expose
`pending`). Update the header comment's "logged once a minute as an alert" with "and written once per alert window to
the governance audit (onUncheckedAlert)".

Run: `node --test test/ipc-supervisor.test.js`. Expected: PASS.

- [ ] **Step 3: Wire it in `lib/front/index.js` and `server.js`**

`createFront({ ..., onUncheckedAlert })` passes it to `createSupervisor`. In `server.js`, in the `createFront` call:

```js
  // Design rule 2 (P4f D24): calls that passed a stale budget snapshot are marked, alerted and audited: one governance
  // entry per alert window, as the system.
  onUncheckedAlert: ({ calls, since }) =>
    govContext.runAs({ via: 'system' }, () =>
      govRecord({
        action: 'gateway.budget_unchecked',
        outcome: 'error',
        target: { type: 'gateway' },
        actor: { via: 'system' },
        calls,
        since: new Date(since).toISOString(),
      }),
    ),
```

A test in `test/front-server.test.js` style: a server in front mode with a gateway stub that emits unchecked usage
(`--emit` already sends `usage.report` events; give the stub `--emit-unchecked` only if the existing synthetic events
carry no `budget: "unchecked"`, else use them), then read the governance chain (`GET /api/governance/audit?action=
gateway.budget_unchecked` as the owner, or the `.data/audit` files): exactly one entry for the window, `calls` equal
to the stored unchecked count, `actor.via` `system`, and the chain verifies.

- [ ] **Step 4: Full checks, commit, push**

```bash
node --test test/ipc-supervisor.test.js test/front-server.test.js test/ipc-hardening.test.js
deno test -q --no-check --no-prompt -P=test test/ipc-supervisor.test.js test/front-server.test.js test/ipc-hardening.test.js
npm run typecheck
git add lib/ipc/supervisor.js lib/front/index.js server.js test
git commit -m "Write one governance entry per unchecked-budget alert window (P4f D24, design rule 2)"
git push origin migrate/p4f-ipc-events
```

### Task 8: Bench, results and the program plan

**Files:**
- Create: `docs/migration/2026-11-16-p4f-results.md`, `bench/results/<date>-node-{js,go}-private-<host>.json` and a
  notes file
- Modify: `docs/migration/2026-11-10-deno-go-migration-plan.md` (§5 S8 row), `ipc/v1/README.md` (P4f section),
  `README.md` architecture note if it lists the IPC messages

- [ ] **Step 1: Re-measure M3 and M4 (D21) on the owner's MacBook, on power**

```bash
ZP_BENCH_TIER=private caffeinate -dims node bench/run.js
ZP_ENGINE=go ZP_BENCH_TIER=private caffeinate -dims node bench/run.js
```

Tracing is on by default (the local store), so the go run measures the spans and the journal. Targets: M3 non-stream
overhead p95 ≤ 2.84 ms; M4 500 streams with 0 errors, overhead p95 ≤ 10 ms, gateway RSS ≤ 80 MB. If a target is missed,
stop and report (the likely cost: the observer re-parsing each SSE line that `ChatStream` already parsed; the remedy
is to hand the observer the parsed value from `ChatStream`, proven equal by the span fixtures).

- [ ] **Step 2: Write the results**

`docs/migration/2026-11-16-p4f-results.md`, in the shape of the P4b and P4c results: What ships (spans, `traceId`,
the journal, one window, OTLP export allowed without `captureContent`); Measured (the S8 test on Node and Deno, the
kill tests, the bench table against M3 and M4, the full-matrix run ids); Differences from the JS gateway (the "Known
deviations" list with their pinned tests, plus anything found in Tasks 1–6); Known gaps; the owner's answers to the
three spec questions, or "default applied"; Follow-ups (P4d and P4e add their cases to the S8 corpus; S9 sends
`audit.event` and closes the `audit.gap` minor).

- [ ] **Step 3: Tick the plan**

In the program plan §5 table, row S8: "Ledger and audit identical to JS for the same run: **met in P4f** (2026-11-16,
`docs/migration/2026-11-16-p4f-results.md`)". In `ipc/v1/README.md`, a section "Spans, the call journal and one
window (added in v1 by P4f)" naming the optional fields and the two message types.

- [ ] **Step 4: Commit, push, full matrix**

```bash
git add docs bench/results ipc/v1/README.md README.md
git commit -m "P4f results: spans and usage over IPC equal to JS, exactly one line across a crash, one rate-limit window"
git push origin migrate/p4f-ipc-events
gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref migrate/p4f-ipc-events
```

## Self-review (done while writing)

- Spec coverage: D24 (Task 9), D23 (no task: the defaults were already planned); D1 (Tasks 1, 4), D2–D7 (Task 1), D8 (Tasks 3, 4), D9–D12 (Task 3), D13 (Task 6's governance and MCP
  diff), D14–D15 (Task 2), D16 (Task 5), D17–D18 (Task 6), D19 (no task: nothing changes in the shadow), D20 (T6-2 and
  T1-c in Task 2, T2-f in Task 7), D21–D22 (Tasks 5, 8).
- Types used across tasks: `TraceCtx`, `SpanIDs`, `ServerFacts`, `Observed`, `ipc.TraceSpanBody.OTLP`,
  `ipc.ProviderSpan` (Task 1); `CallJournal`, `AppendSendNoted`, `engine.Calls`, the `emit(r, call)` signature
  (Task 2); `State.Spans()`, `ipc.TraceSwitch` (Task 3); `SetSpanEmitter`, `SetIDs` (Task 4); `RateLimitTake`,
  `RateLimitWindow`, `RateWindow` (Task 5). Each is defined in the task that produces it and named in the
  "Interfaces" block of the tasks that consume it.
- Order: Task 1 and Task 2 touch different files except `gw/engine/serve.go` (Task 2 only) and may run in parallel;
  Task 4 needs both. Task 3 adds `State.Spans()` used by Task 4.

## Plan deviations found while building

Task 1 (2026-11-16):
1. **A cut surrogate pair** (Known deviation 6): JavaScript's `slice` keeps the high half of a pair it cuts (a
   `user_agent.original`, a span name or a response id at 256 units, an attribute at 8192), and the store then holds a
   lone surrogate; the engine cuts the pair whole (`jsSlice`, as since P4b). Pinned by `TestCutPairIsDroppedWhole`; the
   corpus case `15-user-agent-long` ends exactly at a pair instead.
2. **`providerSpanAttrs` looks a preset up as an own key of `PROVIDER_NAME`.** Before, a preset named like an
   `Object.prototype` member (`constructor`) gave a function, which `cleanValue` dropped, so the span had no
   `gen_ai.provider.name`; now it is the preset itself. A JS fix, not a port difference (corpus case `24`).
3. **Order of a client span that does not wait**: on a model endpoint, a `chat` usage line's span waits for the
   response (server span first); any other kind (`embeddings`) ends at once, before the server span. The fixtures pin
   both orders (`61-embeddings`); Task 4's `spans` helper must emit in the same order.
4. `LLMFormat(method, path)` (lib/otel.js `llmFormat`) is exported from `gw/engine/spans.go`: the observer runs only for
   a model endpoint other than embeddings.
5. `scripts/fixtures/lib.js writeCases` returns the written texts (the drift check reads them); every other generator
   ignores the value and gives the same bytes as before.
6. The fixtures carry `providerSpan` (what `config.push` will push for the case's provider) next to the request, since
   the Go side reads it from the push, not from config.json.

After the P4e merge (`e41e2ea`, 2026-11-17), re-checked against the merged code:
7. **The emit returns a seq** (P4e's own-spend tracking) and every event goes through P4e's sender (`g.out.enqueue`,
   never blocking). Task 2 keeps `engine.New`'s emit as it is and adds `SetCalls(c Calls, emitCall func(r, call)
   (int64, error))`; `emitLine(rec, unchecked, call)` uses `emitCall` for a journalled call. `AppendSendNoted` takes
   the sender's `enqueue` as its send. The guard's refusal (P4e) is not journalled: it never reaches a provider.
8. **The journal is `calls.journal`, not `calls.jsonl`**: the outbox reads every `*.jsonl` name in its folder as a
   segment (`anySegment`, `loadSegments`), and the P3 kill test checks that no `*.jsonl` is left after the drain.
9. **Close order**: the journal closes after the outbox (`box.Close` takes the send lock, so no close is noted after
   it; the second `box.Close` of the existing defer does nothing).
10. T1-c is `TestPruneDuringReplay` in `gw/ipc/journal_test.go`; the crash-point test uses one-event segments so the
    ack really prunes.
11. Task 4 must keep P4e's order in `Serve`: rate limit, budget, guard (a guard refusal writes its line and, from
    Task 4, its spans), then the provider call.

# P4d: translation on the Go engine's request path (slice S5) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The go engine serves `/v1/messages` (with `count_tokens`), `/v1/responses`, the Gemini
`generateContent`/`streamGenerateContent`/`countTokens` calls, `/v1/embeddings`, and chat calls to an Anthropic-format
provider, whole and streamed, byte for byte as the JS gateway answers them, behind eleven new sub-classes with their own
shadow and switch-rule records.

**Architecture:** Each path gets a pure planner in `gw/engine` that reuses P4b's admission checks, ports `mapModel` up
to `modelMap`, translates the client body with `gw/translate` and hands the translated chat body to the existing
P4b/P4e pipeline (stream-usage merge, `State.Guard`, `finishBody`). Answers are built by pure functions over the
provider's bytes (whole) or reader (streams), so a generator that runs the real JS gateway in process
(`scripts/fixtures/gen-xlate.js`) proves every byte. The shadow replays the provider's text with a tape of the random
bytes and clock reads `translate.js` made. `Serve` dispatch, the front's classes and the contract suite come last,
because that is where P4f changes the same files.

**Tech Stack:** Go 1.27.3, standard library only (`net/http`, `io`, `encoding/json`, `strings`, `unicode/utf8`,
`sync`, `time`); `gw/translate` and `gw/translate/jsv` (stream B); `gw/guard` (P4e). Node 24.21.0 and Deno 2.9.8 for
the generators and the contract suite (CommonJS, `node:` built-ins only). GitHub Actions (`.github/workflows/ci.yml`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-17-p4d-translate-wire-design.md` (Decisions 1–27 are binding). Where code
  contradicts the spec, follow the code and record it under "Plan deviations" at the end of this plan.
- Go is **standard library only** (program plan §9); module `gw/`, `GOTOOLCHAIN=local`. Deno: no npm or jsr runtime
  imports; follow the `deno` and `migrate-to-deno` skills. Go: follow `go-stack-defaults`.
- No IPC change: `ipc/v1` schemas, `gw/ipc/messages.go` and `lib/front/push.js` are not edited (Decision 22).
- Byte equality is proven only by fixtures the real JS writes; never by hand-written expected bytes.
- Fixture generators must give identical bytes on Node and on Deno under the CI fixtures job's flags:
  `deno run -q --no-prompt --allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys`.
- The engine hands off (forwards unchanged to the control plane) every refusal it does not answer today (Decision 14):
  only 429, 402 (price, budget), guard 400/500, provider answers, and the 502 of a failed provider call are its own.
- Routes (`router/<name>`), OAuth2/service-account providers (P4g), the token saver acting (tool results), a model
  `mapModel` cannot decide without the model cache, and `GET /v1/models`, `GET /v1beta/models` stay handed off.
- Every task ends green on: `cd gw && gofmt -l . && go vet ./... && GOOS=windows go vet ./... && go test -race ./...`
  (gofmt prints nothing), `node --test test/` for touched JS tests, and every `scripts/fixtures/gen-*.js` leaving
  `git status --porcelain gw/testdata` empty.
- Commits: one plain English sentence, **no AI attribution, no Co-Authored-By line**, never `.superpowers/`, no
  temporary branches, no `git stash`. Push `migrate/p4d-translate-wire` after each commit. Never touch ports 4777/4787.
- **P4f overlap.** Steps marked **(re-check after P4f merges)** touch files P4f changes (`gw/engine/serve.go`,
  `gw/engine/chat.go` `State`, `lib/front/index.js`, `test/contract/harness.js`, `test/front-shadow.test.js`, and every
  fixture that embeds a `config.push`). Before such a step: `git fetch origin && git log origin/migrate/deno-go
  --oneline -1`; if P4f has merged, `git merge origin/migrate/deno-go` first and use P4f's signatures
  (`emitLine(rec, unchecked, call)`, `e.open(p)`, its answer/span helper, `Take` with its replication hook); if not,
  build against today's code and repeat the step's check after the merge.

## Files

| File | Responsibility |
|---|---|
| `scripts/fixtures/gen-xlate.js` (new) | runs `createGateway` in process for every P4d path; writes `gw/testdata/xlate/*.json` |
| `gw/testdata/xlate/` (new) | the JS truth: provider requests, client status/headers/bytes, usage line |
| `gw/engine/xlate_test.go` (new) | replays every `xlate` fixture through the planners and answer builders |
| `gw/engine/plan.go` (new) | `admit`, `target`, `credential`, `PlanFor` (dispatch by path class), `Plan` fields of P4d |
| `gw/engine/anthropic.go` (new) | the Anthropic provider leg: request build, whole answer, event stream → OpenAI text |
| `gw/engine/mapmodel.go` (new) | `MapModel`, `decodeURIComponent`, `geminiPath` |
| `gw/engine/formats.go` (new) | refusal and provider-error shapes per client format |
| `gw/engine/xlate.go` (new) | `PlanMessages`, `PlanResponses`, `PlanGemini` (translated plans) |
| `gw/engine/xlate_answer.go` (new) | `RespondTranslated` (whole answers) |
| `gw/engine/xlate_stream.go` (new) | `TranslatedStream` (streams, pure over an `io.Reader`) |
| `gw/engine/passthrough.go` (new) | native Anthropic and Responses passthrough, whole and streamed |
| `gw/engine/count.go`, `embeddings.go` (new) | `count_tokens`, Gemini `countTokens`, `/v1/embeddings` |
| `gw/engine/providers.go` | `AdaptChat` gains the soft `reasoning_effort` drop (`TakesReasoning`) |
| `gw/engine/guard.go` | `State.Guard` reads the plan's guard format, provider-body builder and refusal shape |
| `gw/engine/chat.go` | `PlanChat` uses `admit`/`target`; an Anthropic target plans the Anthropic leg **(re-check after P4f merges)** |
| `gw/engine/upstream.go` | a plan's own header timeout (`Plan.Timeout`, count's 15 s) |
| `gw/engine/shadow.go`, `shadow_xlate.go` (new) | `Capture` grows; `CompareTranslated` with the tape |
| `gw/engine/serve.go`, `serve_xlate.go` (new), `dispatch.go` (new) | dispatch by class; serving loops of the new paths **(re-check after P4f merges)** |
| `gw/front/route.go`, `front.go` | `%XX` in canonical paths; sub-class lists per path class |
| `scripts/fixtures/gen-route.js` | new `%XX` and `%2e` cases |
| `lib/gateway/shadow.js`, `providers.js`, `translate.js`, `usage.js`, `index.js` | the capture: provider tee, client bytes, last line, tape |
| `lib/front/index.js` | `GO_CLASSES`, `SUB_CLASSES` **(re-check after P4f merges)** |
| `test/contract/harness.js`, `formats.test.js`, `guardrails.test.js`, `budgets.test.js` | go and shadow modes for the new sub-classes **(re-check after P4f merges)** |
| `bench/run.js`, `bench/mock-provider.js` | `ZP_BENCH_FORMAT` knob |
| `docs/migration/2026-11-17-p4d-results.md` (new), the program plan | results, S5 row, owed M3/M4 |

## Interfaces (fixed here; every task uses these names)

```go
package engine

// Sub-classes (Decision 2).
const (
	SubChat = "chat"; SubChatStream = "chat-stream"
	SubChatAnthropic = "chat-anthropic"; SubChatAnthropicStream = "chat-anthropic-stream"
	SubMessages = "messages"; SubMessagesStream = "messages-stream"; SubMessagesCount = "messages-count"
	SubResponses = "responses"; SubResponsesStream = "responses-stream"
	SubGemini = "gemini"; SubGeminiStream = "gemini-stream"; SubGeminiCount = "gemini-count"
	SubEmbeddings = "embeddings"
)

// Subs lists each path class's sub-classes (gw/front uses the same table).
var Subs = map[string][]string{
	"chat":       {SubChat, SubChatStream, SubChatAnthropic, SubChatAnthropicStream},
	"messages":   {SubMessages, SubMessagesStream, SubMessagesCount},
	"responses":  {SubResponses, SubResponsesStream},
	"gemini":     {SubGemini, SubGeminiStream, SubGeminiCount},
	"embeddings": {SubEmbeddings},
}

type Shape int // how a refusal is written: ShapeOpenAI (fail), ShapeAnthropic (anthropicError), ShapeGemini
const ( ShapeOpenAI Shape = iota; ShapeAnthropic; ShapeGemini )

// Upstream kinds of a plan.
const (
	UpChat = "chat"               // POST <base>/chat/completions (OpenAI format)
	UpAnthropic = "anthropic"     // POST <base>/messages, a translated chat body (callChat's Anthropic branch)
	UpAnthropicNative = "anthropic-native" // POST <base>/messages, the client's body (passthrough)
	UpResponsesNative = "responses-native" // POST <base>/responses, the client's body
	UpEmbeddings = "embeddings"   // POST <base>/embeddings
	UpCount = "count"             // POST <base>/messages/count_tokens (15 s)
	UpNone = "none"               // answered by the engine (Plan.Self)
)

// Plan gains (P4d):
//   Class       string          the sub-class
//   Format      string          the client's format: "openai", "anthropic", "responses", "gemini"
//   Shape       Shape
//   Upstream    string          one of the Up* kinds
//   Env         *translate.Env  translate.Default in production; a counter in fixtures; a tape in the shadow
//   ClientModel string          the model name the client used (body.model, or the Gemini path's model)
//   Soft, Dropped []string      translate's tags (Dropped also gains "reasoning" from AdaptChat)
//   Custom      []string        Responses custom tool names
//   GuardFormat string          "openai-chat", "anthropic", "responses", "embeddings"
//   build       func(t *Target, body *Value) []byte   the provider body from the checked body
//   Self        *Answer         an answer the engine gives without a provider (count estimate)
//   Timeout     time.Duration   0: the upstream default (120 s)
//   AnthropicHeaders [][2]string  the client's anthropic-version / anthropic-beta (passthrough, count)

func (s *State) PlanFor(class string, r *Request, now int64) Plan   // class = gw/front Classify's path class
func (s *State) MapModel(name string) (string, bool)
func decodeURIComponent(s string) (string, bool)
func geminiPath(raw string) (model, action string, ok bool)
func TakesReasoning(p *ipc.ProviderConfig, model string) bool
func AdaptChatTagged(p *ipc.ProviderConfig, model string, body *Value, soft []string, dropped *[]string)
func shapeError(sh Shape, status int, msg, typ, code string) Answer           // NoLine
func providerError(format string, status int, msg string) []byte              // translated out.error
func (s *State) RespondTranslated(p *Plan, status int, raw []byte, ms float64) Answer
func (s *State) RespondNative(p *Plan, status int, raw []byte, ms float64) Answer
func (s *State) RespondEmbeddings(p *Plan, status int, raw []byte, ms float64) Answer
func (s *State) RespondCount(p *Plan, status int, raw []byte) Answer
type StreamResult struct{ TTFT *float64; Input, Output, Cached int64 }
type TranslatedStream struct{ /* … */ }
func (s *State) NewTranslatedStream(p *Plan, elapsed func() float64) *TranslatedStream
func (t *TranslatedStream) Open() string
func (t *TranslatedStream) Ping() string                         // "" when the format has none
func (t *TranslatedStream) Run(body io.Reader, contentType string, write func(string) error)
func (t *TranslatedStream) Line(ms float64) ipc.UsageRecord
type NativeStream struct{ /* … */ }
func (s *State) NewNativeStream(p *Plan, elapsed func() float64) *NativeStream
func (n *NativeStream) Run(body io.Reader, write func(string) error)
func (n *NativeStream) Line(ms float64) ipc.UsageRecord
func AnthropicToChatText(model string, body io.Reader, env *translate.Env) io.Reader // callChat's synthesized stream
func SubClass(class string, p *Plan, r *Request) string
func (s *State) CompareTranslated(class string, p *Plan, c *Capture) ShadowLine
```

```js
// lib/gateway/shadow.js (P4d additions)
teeResponse(r)          // providers.request: wraps the provider Response of a captured request
tapeRandom(buf)         // translate.js rid
tapeNow(ms)             // translate.js clock reads
noteUsage(entry)        // now also called by usage.record for every line of a captured request
// capture shape (GET /_zp/shadow/<id>): { providerRequests, providerResponse, upstream: [{ status, contentType,
//   body, ended, truncated }], client: { status, headers, body, truncated }, usage, tape: { random: [hex], now: [ms] } }
```

---

### Task 1: The route-level fixture generator and the replay harness (riskiest: the JS truth for every path)

**Files:**
- Create: `scripts/fixtures/gen-xlate.js`, `gw/testdata/xlate/*.json`, `gw/engine/xlate_test.go`
- Modify: `.gitignore` (`gw/testdata/.gen-xlate-data/`)

**Interfaces:**
- Consumes: `scripts/fixtures/lib.js` (`FIXED_NOW`, the `crypto.randomBytes` counter, `ROOT`), `lib/front/push.js`
  (`configPush`, `secretsPush`, `snapshotPush`), `lib/gateway` `createGateway`, `lib/cost/guard.js createGuard`.
- Produces: fixture files `{ fn, in: { config, secrets, snapshot, request: { path, headers, body }, provider }, out:
  { requests: [{ url, headers, body }], client: { status, headers, body }, usage } }`, where `fn` is one of
  `chatAnthropic`, `messages`, `responses`, `gemini`, `count`, `geminiCount`, `embeddings`, with `stream` cases using
  the same `fn` (the request decides) and `provider` either `{ status, headers?, body }`, `{ status, chunks: [...],
  cut? }`, `{ throw: "<code>" | "timeout" }`, or a list of those for paths with two provider calls; the Go harness
  `runXlate(t, f)`.

- [ ] **Step 1: Write the generator.** It follows `gen-chat.js` (in-process `gateway.handle`, a fake `fetchImpl`, a
  scratch data folder removed at the end) and `gen-chat-guard.js` (policy, keys, a budget guard over a fake budget
  service, `snapshotPush` for the Go side). It must require `./lib` **before** `lib/gateway` so the random counter and
  the clock are installed, and reset the counter before each case (`lib.resetCounter()`, a two-line export added to
  `lib.js` only if `writeCases` cannot be used; prefer `writeCases`, whose `call` resets it).

```js
// Fixtures for the go engine's translated paths (P4d, slice S5): the real JS gateway answers each case in process
// against a provider stubbed at fetch; this records what it sent each provider, what it answered the client (status,
// the headers the engine sets, the bytes) and its usage line. gw/engine/xlate_test.go plans and answers the same case
// from the config.push, secrets.handoff and budget.snapshot the control plane would send and must give the same bytes.
// Writes gw/testdata/xlate/<name>.json. Run: node scripts/fixtures/gen-xlate.js (CI: the fixtures job, Node and Deno).
const fs = require('fs');
const path = require('path');
const { writeCases, ROOT } = require('./lib'); // first: the random counter and the fixed clock
const { createGateway } = require('../../lib/gateway');
const { createSecrets } = require('../../lib/secrets');
const { createGuard } = require('../../lib/cost/guard');
const { configPush, secretsPush, snapshotPush } = require('../../lib/front/push');
const { hash } = require('../../lib/gateway/keys');
const { toNano } = require('../../lib/money');

const SECRET = 'zp_xlateFixtureSecret0123456789abcd';
const KEY_OA = 'sk-oa-xlate-123456';
const KEY_AN = 'sk-an-xlate-654321';
const GATEWAY = {
  providers: [
    { id: 'oa', preset: 'custom', baseUrl: 'http://127.0.0.1:9/v1', tier: 'external', approved: true, apiKey: { env: 'KEY_OA' } },
    { id: 'an', preset: 'anthropic', baseUrl: 'http://127.0.0.1:10/v1', approved: true, apiKey: { env: 'KEY_AN' } },
    { id: 'op', preset: 'openai', baseUrl: 'http://127.0.0.1:8/v1', approved: true, apiKey: { env: 'KEY_OA' } },
    { id: 'pv', preset: 'ollama', baseUrl: 'http://127.0.0.1:11434/v1', aliases: { fast: 'llama-9' } },
    { id: 'or', preset: 'openrouter', baseUrl: 'http://127.0.0.1:11/v1', approved: true, apiKey: { env: 'KEY_OA' } },
    { id: 'lt', preset: 'litellm', baseUrl: 'http://127.0.0.1:4000/v1' },
    { id: 'gm', preset: 'gemini', baseUrl: 'http://127.0.0.1:12/v1beta/openai', approved: true, apiKey: { env: 'KEY_OA' } },
  ],
  routes: [{ name: 'router/x', targets: ['oa/m1', 'op/gpt-6.1'] }],
  guardrails: { scope: 'external', secrets: 'off', pii: 'off', injection: 'off' },
  defaults: { model: '', plannerModel: '', codeModel: '' },
  tokenSaver: true,
  modelMap: [{ match: 'claude-*', to: 'oa/m1' }, { match: 'gpt-codex*', to: 'op/gpt-6.1' }, { match: 'gemini-*', to: 'gm/gemini-2.5-pro' }],
};
```

  The corpus (one `[name, opt, request, provider]` per case; `opt` may set `policy`, `key`, `budgets`, `headers`).
  Every case must be one the engine serves (hand-offs are Go unit tests). At least:
  - **chatAnthropic**: whole; stream; tools both ways; thinking/`reasoning_effort` high; a system message;
    `parallel_tool_calls: false`; provider 400 (Anthropic error JSON); provider 529; provider throw `ECONNREFUSED`;
    a non-JSON 200 is **not** a case (Decision 26a); stream cut mid-event (ends clean, J3); stream with an
    unparseable event (skipped); stream with `stream_options.include_usage` from the client.
  - **messages** translated to `oa`, `op` (reasoning model, `max_completion_tokens`), `pv` (Ollama: no credential,
    `reasoning_effort` soft-dropped, `x-zp-dropped: reasoning`), `or` (OpenRouter), `lt` (LiteLLM), `an` through
    `modelMap` (`claude-*` → `oa/m1` gives `requested`); whole and stream; tools; `tool_choice` any/none/tool;
    images (base64, url); documents; server tool dropped (`x-zp-dropped`); thinking enabled/adaptive;
    `output_config.effort`; a JSON answer to a stream request (`content-type: application/json` → `wholeToChunks`);
    a chunk with `error` mid-stream; provider cut (`cut: true` → `terminated`); provider 429 with `retry-after`
    (not passed through); provider throw; `out.whole` TypeError (provider 200 `{"choices":null}`); guard redact,
    log, block, 500 failure (policy that makes a check throw, as `gen-chat-guard.js` does), each in the Anthropic
    shape; budget 402 and price 402 in the Anthropic shape; 429 from the key's own window (two calls, `rpm: 1`).
  - **messages** native passthrough to `an` (model `an/claude-x`): whole; stream (`message_start` model rewritten,
    `event:` names kept, a non-JSON data line passed as it came, usage merged from `message_start` and
    `message_delta`); client `anthropic-version` and `anthropic-beta` forwarded, a CR/LF one dropped, a 1001-char one
    dropped; provider 400 (body masked, the `KEY_AN` value in it becomes `***`); `j.usage` missing (no tokens);
    guard redact in format `anthropic`.
  - **responses** translated to `oa` (whole, stream, custom tools, `json_object`, `instructions`, `max_output_tokens`);
    to `an` (double translation, whole and stream); native passthrough to `op` (whole; stream with `response.model`
    rewritten, an event named `message` written without `event:`, usage from `response.usage`; provider non-2xx
    with `error.message`); guard block in the OpenAI shape.
  - **gemini** `generateContent` to `oa`, to `an`, to `gm` (`reasoning_effort` kept: `gemini-2.5` on the `gemini`
    preset), `streamGenerateContent` (with and without `?alt=sse`), model `oa%2Fm1` (path decoded), `?key=` secret,
    `x-goog-api-key`, provider 404 → `NOT_FOUND`, provider 500 → `UNAVAILABLE`, guard block with `details`,
    provider throw → 502 `INVALID_ARGUMENT` (J9, Decision 27).
  - **count** without model; OpenAI model (estimate); native `an` model with provider `{input_tokens: 42}`; native
    with provider 500 (estimate); native with provider throw (estimate); native with a `NaN`-like answer
    (`{"input_tokens":"x"}` → estimate); guard block on a native model (estimate, no line); key not allowed for the
    model (estimate). **geminiCount**: `contents`, `generateContentRequest`, neither.
  - **embeddings** to `oa` (2xx: bytes passed, line `input` from `usage.prompt_tokens`, `output: 0`); to `pv`; provider
    400 (masked body, line without `error`); provider throw (502, **no line**: `out.usage` is `null`); guard redact in
    format `embeddings`.

  For each case the runner records: every fetch (`{ url, headers, body }`, in order), the client's status, the
  headers `content-type`, `cache-control`, `access-control-allow-origin`, `retry-after`, `x-zp-dropped` (each `null`
  when absent), the body (whole text, or the whole streamed text), and the last usage line written during the case
  (`null` when none) without `ts` and `ms`, with `cost` as `toNano(cost)` and `ttft` as `"absent"`, `null` or
  `"set"` (as `gen-chat.js`). A stream provider is a `ReadableStream` that enqueues one chunk per read and errors with
  `new Error('terminated')` after the last when `cut` (as `gen-chat.js`).

- [ ] **Step 2: Run it on Node and on Deno and compare.**

Run: `node scripts/fixtures/gen-xlate.js && git add -N gw/testdata/xlate && deno run -q --no-prompt --allow-read --allow-write=gw/testdata --deny-env --deny-net --deny-run --deny-sys scripts/fixtures/gen-xlate.js && git diff --stat gw/testdata/xlate`
Expected: `gen-xlate: <n> cases in gw/testdata/xlate` twice, and no diff between the two runs. If Deno's
`TextDecoder` or `ReadableStream` order differs on a case, drop that case and record why in the generator.

- [ ] **Step 3: Write the Go harness** (`gw/engine/xlate_test.go`). It loads `xlate` fixtures, builds the `State` as
  `stateFor` does (plus `WithSnapshot` of `in.snapshot`), the `Request` with `RawPath` from `in.request.path`, and an
  `Env` whose `Random(n)` returns `n` bytes equal to a counter starting at 1 for each case and `Now()` returns
  `fixtureNow`. In this task every `fn` calls `t.Skip("planner not ported yet")`; Task 7 turns an unported `fn` into
  a failure.

```go
package engine

import (
	"encoding/json"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/translate"
)

type xlateProvider struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Chunks  []string          `json:"chunks"`
	Cut     bool              `json:"cut"`
	Throw   string            `json:"throw"`
}

type xlateCase struct {
	Config   ipc.ConfigPush     `json:"config"`
	Secrets  ipc.SecretsHandoff `json:"secrets"`
	Snapshot *ipc.BudgetSnapshot `json:"snapshot"`
	Request  struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Provider  json.RawMessage `json:"provider"` // one xlateProvider, or a list
}

type xlateOut struct {
	Requests []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"requests"`
	Client struct {
		Status  int                `json:"status"`
		Headers map[string]*string `json:"headers"`
		Body    string             `json:"body"`
	} `json:"client"`
	Usage json.RawMessage `json:"usage"`
}

// counterEnv is the generator's determinism: Random gives n bytes equal to a counter that starts at 1 per case.
func counterEnv() *translate.Env {
	n := 0
	return &translate.Env{
		Now: func() int64 { return fixtureNow },
		Random: func(k int) []byte {
			n++
			b := make([]byte, k)
			for i := range b {
				b[i] = byte(n)
			}
			return b
		},
	}
}

var xlatePorted = map[string]func(t *testing.T, name string, c xlateCase, want xlateOut){}

func TestXlateFixtures(t *testing.T) {
	for _, f := range loadFixtures(t, "xlate") {
		run, ok := xlatePorted[f.Fn]
		var c xlateCase
		var want xlateOut
		if err := json.Unmarshal(f.In, &c); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if err := json.Unmarshal(f.Out, &want); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		t.Run(f.Name, func(t *testing.T) {
			if !ok {
				t.Skip("planner not ported yet") // Task 7 makes this a failure
			}
			run(t, f.Name, c, want)
		})
	}
}
```

- [ ] **Step 4: Run** `go -C gw test ./engine -run TestXlateFixtures -v | tail -3`. Expected: PASS with every subtest
  skipped. `node scripts/fixtures/gen-xlate.js && git status --porcelain gw/testdata` prints nothing.
- [ ] **Step 5: Commit** "Add the P4d fixture generator for translated gateway calls and the Go replay harness" and push.

### Task 2: The Anthropic provider leg and `chat-anthropic` (riskiest Go byte work: two translations and a synthesized stream)

**Files:**
- Create: `gw/engine/plan.go`, `gw/engine/anthropic.go`, `gw/engine/anthropic_test.go`
- Modify: `gw/engine/chat.go` (`PlanChat` uses `admit`/`target`; the format hand-off goes) **(re-check after P4f merges)**,
  `gw/engine/shadow.go` (`subOf` → `SubClass`), `gw/engine/xlate_test.go` (register `chatAnthropic`)

**Interfaces:**
- Consumes: `translate.OpenAIToAnthropicRequest`, `(*Env).AnthropicResponseToOpenAI`, `(*Env).NewAnthropicEvents`,
  `translate.ReadSSE`, `jsv.Parse`, `jsv.Stringify`, `toJSV`, `ChatStream`, `finishBody`.
- Produces: `admit`, `target`, `credential`, `SubClass`, `AnthropicToChatText`, `anthropicBuild`, the Plan fields
  listed under Interfaces, `UpAnthropic`.

- [ ] **Step 1: Failing tests.** Register `chatAnthropic` in `xlatePorted` (plan with `PlanFor("chat", …)`, then
  `RespondChatAnthropic` for whole and the synthesized stream through `ChatStream` for streams; compare
  `want.Requests[0]`, the client and the usage exactly as `TestChatFixtures` does). Add:

```go
// An Anthropic-format target is the chat-anthropic leg now (P4d); OAuth2 stays P4g.
func TestChatAnthropicSubClass(t *testing.T) {
	s := xlateState(t)
	p := s.PlanChat(chatReq(`{"model":"an/claude-x","messages":[{"role":"user","content":"hi"}]}`), fixtureNow)
	if p.Handoff != "" || p.Upstream != UpAnthropic || SubClass("chat", &p, nil) != SubChatAnthropic {
		t.Fatalf("plan %+v", p)
	}
	p = s.PlanChat(chatReq(`{"model":"an/claude-x","stream":true,"messages":[]}`), fixtureNow)
	if SubClass("chat", &p, nil) != SubChatAnthropicStream {
		t.Fatal(p.Class)
	}
}

func TestCredentialHandsOffOAuth(t *testing.T) {
	s := xlateStateWith(t, func(p *ipc.ProviderConfig) {
		if p.ID == "an" { p.Auth = &ipc.ProviderAuth{Type: "oauth2"} }
	})
	if p := s.PlanChat(chatReq(`{"model":"an/claude-x","messages":[]}`), fixtureNow); p.Handoff != "provider: OAuth2 or a service account (P4g)" {
		t.Fatal(p.Handoff)
	}
}

// The synthesized stream is callChat's: one data line per converted chunk, then [DONE]; an upstream cut or an
// unparseable event ends it cleanly.
func TestAnthropicToChatTextEndsClean(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":3}}}\n\nevent: x\ndata: {bad\n\n"
	b, _ := io.ReadAll(AnthropicToChatText("an/claude-x", iotest.TimeoutReader(strings.NewReader(in)), counterEnv()))
	if !strings.HasSuffix(string(b), "data: [DONE]\n\n") {
		t.Fatalf("%q", b)
	}
}
```

  `xlateState`, `xlateStateWith` and `chatReq` are helpers in `anthropic_test.go` over the first `xlate` fixture's
  config (so no config is hand-written).

- [ ] **Step 2: Run** `go -C gw test ./engine -run 'ChatAnthropic|CredentialHandsOff|AnthropicToChatText|TestXlateFixtures/.*chatAnthropic'`.
  Expected: FAIL (undefined: `SubClass`, `Upstream`, `AnthropicToChatText`).

- [ ] **Step 3: Implement `plan.go`.** Factor `PlanChat`'s first checks out unchanged:

```go
// admit is the checks every gateway path makes before reading the model (PlanChat's first lines, P4b): loopback Host
// and client, no browser or proxy header, POST, a key, a JSON object body of at most 20 MB. scope "" is chat's authed
// (the scope is checked with the model later); "llm" is authed(req, 'llm').
func (s *State) admit(r *Request, now int64, scope string) (*ipc.GatewayKeyAuth, *Value, string) {
	if !loopbackName(r.Host) || !loopbackPeer(r.Peer) {
		return nil, nil, "access: not a loopback host and client"
	}
	for _, h := range append([]string{"Origin", "Referer", "Sec-Fetch-Site"}, proxyHeaders...) {
		if _, ok := r.Header[h]; ok {
			return nil, nil, "access: a browser or proxy header"
		}
	}
	if r.Method != http.MethodPost {
		return nil, nil, "method"
	}
	secret, ok := RequestSecret(r.Header, r.RawPath, r.Query)
	key, why := s.Keys.Authenticate(secret, ok, now)
	if key == nil {
		return nil, nil, "key: " + why
	}
	if scope != "" && Authorize(key, scope, "", "") != "" {
		return nil, nil, "key: not allowed"
	}
	if r.TooBig || len(r.Body) == 0 {
		return nil, nil, "body: empty or over 20 MB"
	}
	body, err := Parse(r.Body)
	if err != nil || body.Kind != Object {
		return nil, nil, "body: not a JSON object the engine serves"
	}
	return key, body, ""
}

// target is resolveAll's single target for a model the key may use: a route is handed off (Decision 9).
func (s *State) target(model string, key *ipc.GatewayKeyAuth) (*Target, string) {
	for _, rt := range s.Gateway.Routes {
		if rt.Name == model {
			return nil, "model: a route"
		}
	}
	t := Resolve(s.Gateway.Providers, model)
	if t == nil || (key.PrivateOnly && TierOf(t.Provider) != "private") {
		return nil, "model: no usable target"
	}
	return t, ""
}

// credential is the provider's API key, "" for none; OAuth2 and service accounts are P4g's (Decision 17).
func (s *State) credential(t *Target) (string, string) {
	a := t.Provider.Auth
	if a == nil {
		return "", ""
	}
	if a.Type != "key" {
		return "", "provider: OAuth2 or a service account (P4g)"
	}
	if c := s.Secrets[t.Provider.ID]; c != "" {
		return c, ""
	}
	return "", "provider: no credential"
}

// providerHeader is providers.js headers(p): x-api-key and anthropic-version for the Anthropic format, else Bearer.
func providerHeader(t *Target, cred string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	if FormatOf(t.Provider) == "anthropic" {
		if cred != "" {
			h.Set("X-Api-Key", cred)
		}
		h.Set("Anthropic-Version", "2023-06-01")
	} else if cred != "" {
		h.Set("Authorization", "Bearer "+cred)
	}
	return h
}

// PlanFor plans a request of path class class (gw/front Classify).
func (s *State) PlanFor(class string, r *Request, now int64) Plan {
	switch class {
	case "chat":
		return s.PlanChat(r, now)
	case "messages":
		if r.RawPath == "/v1/messages/count_tokens" {
			return s.PlanCount(r, now)
		}
		return s.PlanMessages(r, now)
	case "responses":
		return s.PlanResponses(r, now)
	case "gemini":
		return s.PlanGemini(r, now)
	case "embeddings":
		return s.PlanEmbeddings(r, now)
	}
	return handoff("class: not on the go engine")
}
```

  `PlanMessages`, `PlanResponses`, `PlanGemini`, `PlanCount`, `PlanEmbeddings` are added in Tasks 3, 5 and 6; until
  then add them as one-line stubs returning `handoff("not ported yet")` in `plan.go` and delete each stub in the task
  that implements it.

- [ ] **Step 4: Implement `anthropic.go`.**

```go
// anthropicBuild is callChat's Anthropic branch: { ...rest, model: t.model } without stream_options, through
// openAIToAnthropicRequest, as JSON.stringify writes it (no adaptChat).
func anthropicBuild(t *Target, body *Value) []byte {
	out := body.Clone()
	out.Delete("stream_options")
	out.Set("model", Str(t.Model))
	req, err := translate.OpenAIToAnthropicRequest(toJSV(out))
	if err != nil {
		return nil // a BadRequest is caught at plan time (planAnthropicLeg); a nil body is never sent
	}
	return []byte(jsv.MustStringify(req))
}

// planAnthropicLeg turns a chat plan's provider request into the Anthropic leg. It checks once, at plan time, that
// the translation does not throw (a throw would be the JS 502 after a 599 line: handed off instead).
func planAnthropicLeg(p *Plan, cred string, pending *Value) string {
	if anthropicBuild(p.Target, pending) == nil {
		return "translate: the request cannot be translated for an Anthropic provider"
	}
	p.Upstream = UpAnthropic
	p.URL = p.Target.Provider.BaseURL + "/messages"
	p.Header = providerHeader(p.Target, cred)
	p.build = anthropicBuild
	return ""
}

// anthropicWhole is callChat's whole answer: the provider's JSON through anthropicResponseToOpenAI(j, body.model),
// re-read as the Response it becomes (JSON.stringify then JSON.parse). ok false: r.json() would throw (599 and 502).
func anthropicWhole(env *translate.Env, raw []byte, clientModel string) ([]byte, bool) {
	j, err := jsv.Parse([]byte(decodeWHATWG(raw)))
	if err != nil {
		return nil, false
	}
	o, err := env.AnthropicResponseToOpenAI(j, clientModel)
	if err != nil {
		return nil, false
	}
	return []byte(jsv.MustStringify(o)), true
}

// AnthropicToChatText is callChat's synthesized stream: readSse over the provider's events, each parsed (a bad one
// skipped) and converted, one "data: <JSON>\n\n" per chunk, then "data: [DONE]\n\n". Any error (a cut, a conversion
// that throws) ends it cleanly, as the JS catch does.
func AnthropicToChatText(model string, body io.Reader, env *translate.Env) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		conv := env.NewAnthropicEvents(model)
		_ = translate.ReadSSE(body, func(_, data string) error {
			o, err := jsv.Parse([]byte(data))
			if err != nil {
				return nil
			}
			chunks, err := conv.Next(o)
			if err != nil {
				return err
			}
			for _, c := range chunks {
				if _, werr := io.WriteString(pw, "data: "+jsv.MustStringify(c)+"\n\n"); werr != nil {
					return werr
				}
			}
			return nil
		})
		_, _ = io.WriteString(pw, "data: [DONE]\n\n")
		_ = pw.Close()
	}()
	return pr
}

// decodeWHATWG is Response.text(): a leading byte order mark dropped, invalid bytes per maximal subpart (validText).
func decodeWHATWG(b []byte) string { return validText(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})) }
```

  In `PlanChat`, replace the format hand-off and the credential block by:

```go
	cred, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
```

  and after `out` is built (the stream-usage merge already skips Anthropic targets through `StreamUsageOf`):

```go
	p := Plan{Key: key, Target: t, Model: model, ClientModel: model, URL: t.Provider.BaseURL + "/chat/completions",
		Header: providerHeader(t, cred), Base: s.base(key, t, model, r), Stream: stream, Format: "openai",
		Shape: ShapeOpenAI, Upstream: UpChat, GuardFormat: "openai-chat", Env: translate.Default,
		WantsUsage: so.Truthy() && so.Get("include_usage").Truthy(),
		build: func(t *Target, b *Value) []byte { return finishBody(t, b) }}
	if FormatOf(t.Provider) == "anthropic" {
		if why := planAnthropicLeg(&p, cred, out); why != "" {
			return handoff(why)
		}
	}
	p.Class = SubClass("chat", &p, nil)
```

  and replace `p.Body = finishBody(t, out)` by `p.Body = p.build(t, out)`. `RespondChatAnthropic(p, status, raw, ms)`:
  a non-2xx goes through `s.Respond` unchanged (callChat returns it as it came); a 2xx goes through `anthropicWhole`
  then `s.Respond(p, 200, converted, ms)`; `!ok` → `s.Failed(p, why, ms)` with `why` the Go JSON error text (Decision
  26a). `SubClass` replaces `subOf`:

```go
// SubClass is the sub-class a request of path class class belongs to. A plan the engine would serve says so itself;
// for a hand-off the body is read again, so its line is filed under the sub-class it asked for (P4c final review m1).
func SubClass(class string, p *Plan, r *Request) string {
	if p.Handoff == "" && p.Class != "" {
		return p.Class
	}
	switch class {
	case "chat":
		stream := p.Stream
		if p.Handoff != "" && r != nil {
			b, err := Parse(r.Body)
			stream = err == nil && b.Get("stream").Truthy()
		}
		anth := p.Upstream == UpAnthropic
		switch {
		case anth && stream:
			return SubChatAnthropicStream
		case anth:
			return SubChatAnthropic
		case stream:
			return SubChatStream
		}
		return SubChat
	case "messages":
		if r != nil && r.RawPath == "/v1/messages/count_tokens" {
			return SubMessagesCount
		}
		if bodyStreams(r) {
			return SubMessagesStream
		}
		return SubMessages
	case "responses":
		if bodyStreams(r) {
			return SubResponsesStream
		}
		return SubResponses
	case "gemini":
		if r != nil {
			if _, a, ok := geminiPath(r.RawPath); ok && a == "countTokens" {
				return SubGeminiCount
			} else if ok && a == "streamGenerateContent" {
				return SubGeminiStream
			}
		}
		return SubGemini
	}
	return class
}

func bodyStreams(r *Request) bool {
	if r == nil {
		return false
	}
	b, err := Parse(r.Body)
	return err == nil && b.Get("stream").Truthy()
}
```

  Keep `subOf(p, r)` as `SubClass("chat", p, r)` so the P4c call sites compile.

- [ ] **Step 5: Run** the Step 2 tests, then `go -C gw test -race ./engine`. Expected: PASS, and `TestChatFixtures`,
  `TestChatStreamFixtures`, `TestChatGuardFixtures` unchanged.
- [ ] **Step 6: Commit** "Serve chat calls to an Anthropic-format provider in the go engine as the JS callChat translates them" and push.

### Task 3: Translated whole answers for messages, responses and Gemini

**Files:**
- Create: `gw/engine/mapmodel.go`, `gw/engine/formats.go`, `gw/engine/xlate.go`, `gw/engine/xlate_answer.go`,
  `gw/engine/xlate_plan_test.go`
- Modify: `gw/engine/providers.go` (`TakesReasoning`, `AdaptChatTagged`), `gw/engine/guard.go` (format, builder,
  shape), `gw/engine/budget.go` (`quota` takes the plan's shape), `gw/engine/chat.go` (`RateLimited` takes a shape)
  **(re-check after P4f merges)**, `gw/engine/xlate_test.go` (register `messages`, `responses`, `gemini` whole cases)

**Interfaces:**
- Consumes: Task 2's `admit`, `target`, `credential`, `providerHeader`, `planAnthropicLeg`, `anthropicWhole`;
  `translate.AnthropicToOpenAI`, `ResponsesToOpenAI`, `GeminiToOpenAI`, `(*Env).OpenAIToAnthropic`,
  `OpenAIToResponses`, `OpenAIToGemini`; P4e's `State.Guard`, `State.Budget`.
- Produces: `PlanMessages` (translated part), `PlanResponses` (translated part), `PlanGemini`, `MapModel`,
  `decodeURIComponent`, `geminiPath`, `shapeError`, `providerError`, `RespondTranslated`, `AdaptChatTagged`.

- [ ] **Step 1: Failing unit tests** (`xlate_plan_test.go`):

```go
func TestMapModel(t *testing.T) {
	s := xlateState(t) // modelMap: claude-* → oa/m1, gpt-codex* → op/gpt-6.1, gemini-* → gm/gemini-2.5-pro
	for _, c := range []struct{ in, out string; ok bool }{
		{"oa/m1", "oa/m1", true}, {"fast", "fast", true}, {"router/x", "router/x", true},
		{"claude-sonnet-9", "oa/m1", true}, {"CLAUDE-x", "oa/m1", true}, // allows() folds case as keys.js
		{"zz/none", "", false},   // a provider id that is not usable: the model cache decides → hand off
		{"llama3", "", false},
	} {
		got, ok := s.MapModel(c.in)
		if got != c.out || ok != c.ok {
			t.Errorf("%q: %q %v", c.in, got, ok)
		}
	}
}

func TestDecodeURIComponent(t *testing.T) {
	for in, want := range map[string]string{"oa%2Fm1": "oa/m1", "a%3Ab": "a:b", "caf%C3%A9": "café", "plain": "plain"} {
		if got, ok := decodeURIComponent(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"%", "%2", "%zz", "%C3", "%ED%A0%80"} {
		if _, ok := decodeURIComponent(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

func TestGeminiPath(t *testing.T) {
	m, a, ok := geminiPath("/v1beta/models/pv/llama3:8b:streamGenerateContent")
	if !ok || m != "pv/llama3:8b" || a != "streamGenerateContent" {
		t.Fatal(m, a, ok)
	}
	if _, _, ok := geminiPath("/v1beta/models/:generateContent"); ok {
		t.Fatal("an empty model matched")
	}
}

func TestAdaptChatSoftDrop(t *testing.T) {
	s := xlateState(t)
	pv := providerByID(s, "pv")
	body, _ := Parse([]byte(`{"model":"llama-9","reasoning_effort":"high","max_tokens":5}`))
	dropped := []string{"web_search"}
	AdaptChatTagged(pv, "llama-9", body, []string{"reasoning_effort"}, &dropped)
	if string(body.Stringify()) != `{"model":"llama-9","max_tokens":5}` || strings.Join(dropped, ",") != "web_search,reasoning" {
		t.Fatalf("%s %v", body.Stringify(), dropped)
	}
	body, _ = Parse([]byte(`{"model":"gpt-oss-20b","reasoning_effort":"high"}`))
	AdaptChatTagged(pv, "gpt-oss-20b", body, []string{"reasoning_effort"}, &dropped)
	if body.Get("reasoning_effort") == nil {
		t.Fatal("gpt-oss takes reasoning_effort")
	}
}

// Every hand-off the translated planners keep (Decision 14): nothing is sent, JS answers.
func TestTranslatedHandoffs(t *testing.T) {
	s := xlateState(t)
	for _, c := range []struct{ path, body, why string }{
		{"/v1/messages", `{"model":"oa/m1"}`, "body: messages is not an array"},
		{"/v1/messages", `{"model":7,"messages":[]}`, "model: missing"},
		{"/v1/messages", `{"model":"llama3","messages":[]}`, "model: mapping needs the model list"},
		{"/v1/messages", `{"model":"router/x","messages":[]}`, "model: a route"},
		{"/v1/messages", `{"model":"oa/m1","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`, "token saver: tool results"},
		{"/v1/responses", `{"model":"oa/m1","input":"hi","previous_response_id":"r1"}`, "translate: the request cannot be translated"},
		{"/v1beta/models/oa%2Gm1:generateContent", `{"contents":[]}`, "model: the path does not decode"},
	} {
		p := s.PlanFor(classOf(c.path), xlateReq(c.path, c.body), fixtureNow)
		if p.Handoff != c.why {
			t.Errorf("%s %s: %q", c.path, c.body, p.Handoff)
		}
	}
}
```

  (`classOf` calls `front.Classify`; `xlateReq` builds a loopback `Request` with the fixture key.)

- [ ] **Step 2: Run** `go -C gw test ./engine -run 'MapModel|DecodeURI|GeminiPath|AdaptChatSoft|TranslatedHandoffs'`.
  Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement `mapmodel.go`.**

```go
// MapModel is lib/gateway/index.js mapModel up to modelMap (Decision 8): the name when it is a route or resolves, else
// the first modelMap entry whose match allows it. ok false: the model cache or defaults would decide; hand off.
func (s *State) MapModel(name string) (string, bool) {
	if name != "" {
		for _, rt := range s.Gateway.Routes {
			if rt.Name == name {
				return name, true
			}
		}
		if Resolve(s.Gateway.Providers, name) != nil {
			return name, true
		}
	}
	for _, m := range s.Gateway.ModelMap {
		if m.Match != "" && m.To != "" && Allows([]string{m.Match}, name) {
			return m.To, true
		}
	}
	return "", false
}

// decodeURIComponent is the JS function for a canonical path segment: every %XX decoded, the bytes valid UTF-8 (no
// surrogate, no overlong form); ok false where JS throws a URIError.
func decodeURIComponent(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b = append(b, s[i])
			continue
		}
		if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			return "", false
		}
		b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
		i += 2
	}
	if !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c >= 'a':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

var geminiActions = []string{"generateContent", "streamGenerateContent", "countTokens"}

// geminiPath is the route's /^\/v1beta\/models\/(.+):(generateContent|streamGenerateContent|countTokens)$/ with the
// model decoded (ok false: no match, or a model that does not decode).
func geminiPath(raw string) (model, action string, ok bool) {
	const pre = "/v1beta/models/"
	if !strings.HasPrefix(raw, pre) {
		return "", "", false
	}
	rest := raw[len(pre):]
	for _, a := range geminiActions {
		if strings.HasSuffix(rest, ":"+a) && len(rest) > len(a)+1 {
			m, ok := decodeURIComponent(rest[:len(rest)-len(a)-1])
			return m, a, ok
		}
	}
	return "", "", false
}
```

  Note the greedy `(.+)`: the action is the text after the **last** `:`, which `HasSuffix` gives; `generateContent`
  never suffixes `streamGenerateContent` (capital G).

- [ ] **Step 4: Implement `providers.go` additions.**

```go
var takesReasoningEffort = regexp.MustCompile(`(?i)(^|/)(o[1-9]|gpt-5|gpt-oss)`)
var geminiReasoning = regexp.MustCompile(`gemini-(2\.5|[3-9])`)

// TakesReasoning is providers.js takesReasoning.
func TakesReasoning(p *ipc.ProviderConfig, model string) bool {
	if p.Reasoning != nil {
		return *p.Reasoning
	}
	return takesReasoningEffort.MatchString(model) || (p.Preset == "gemini" && geminiReasoning.MatchString(model))
}

// AdaptChatTagged is adaptChat for a translated body: AdaptChat, then a reasoning_effort the translation added (soft)
// is removed for a model that does not take it, and "reasoning" is added to dropped once.
func AdaptChatTagged(p *ipc.ProviderConfig, model string, body *Value, soft []string, dropped *[]string) {
	AdaptChat(p, model, body)
	for _, f := range soft {
		if re := body.Get("reasoning_effort"); f == "reasoning_effort" && re != nil && re.Kind != Null && !TakesReasoning(p, model) {
			body.Delete("reasoning_effort")
			if dropped != nil && !slices.Contains(*dropped, "reasoning") {
				*dropped = append(*dropped, "reasoning")
			}
		}
	}
}
```

  `out.reasoning_effort != null` in JS is false for `undefined` and `null`: `re != nil && re.Kind != Null`. The Go
  `regexp` is fine here: the JS patterns are plain alternations without lookaround (as `openAIReasoning` already is).

- [ ] **Step 5: Implement `formats.go`.**

```go
const errorInfo = "type.googleapis.com/google.rpc.ErrorInfo"

// shapeError is the route's catch for a thrown HttpError: fail (OpenAI), anthropicError, or the Google error.
// retry is the 429's Retry-After ("" for none); Gemini sends none (J9).
func shapeError(sh Shape, status int, msg, typ, code, retry string) Answer {
	h := answerHeader()
	var body []byte
	switch sh {
	case ShapeAnthropic:
		b, e := NewObject(), NewObject()
		b.Set("type", Str("error"))
		e.Set("type", Str(anthropicType(status)))
		e.Set("message", Str(msg))
		if code != "" {
			e.Set("code", Str(code))
		}
		b.Set("error", e)
		body = b.Stringify()
	case ShapeGemini:
		b, e := NewObject(), NewObject()
		e.Set("code", Num(float64(status)))
		e.Set("message", Str(msg))
		e.Set("status", Str(googleStatus(status, false)))
		if code != "" {
			d := NewObject()
			d.Set("@type", Str(errorInfo))
			d.Set("reason", Str(code))
			d.Set("domain", Str("zaafpulse"))
			e.Set("details", &Value{Kind: Array, A: []*Value{d}})
		}
		b.Set("error", e)
		body = b.Stringify()
		retry = ""
	default:
		body = errorBody(msg, typ, code)
	}
	if retry != "" && status == http.StatusTooManyRequests {
		h.Set("Retry-After", retry)
	}
	return Answer{Status: status, Header: h, Body: body, NoLine: true}
}

func anthropicType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

// googleStatus: the route's catch (INVALID_ARGUMENT by default) or geminiApi's out.error (provider: 404 NOT_FOUND,
// UNAVAILABLE by default).
func googleStatus(status int, provider bool) string {
	switch status {
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 404:
		if provider {
			return "NOT_FOUND"
		}
	}
	if provider {
		return "UNAVAILABLE"
	}
	return "INVALID_ARGUMENT"
}

// providerError is translated()'s out.error(status, message) for a provider's non-2xx answer.
func providerError(format string, status int, msg string) []byte {
	b, e := NewObject(), NewObject()
	switch format {
	case "anthropic":
		b.Set("type", Str("error"))
		e.Set("type", Str(anthropicType(status)))
		e.Set("message", Str(msg))
	case "gemini":
		e.Set("code", Num(float64(status)))
		e.Set("message", Str(msg))
		e.Set("status", Str(googleStatus(status, true)))
	default: // responses
		e.Set("message", Str(msg))
		typ := "invalid_request_error"
		if status >= 500 {
			typ = "server_error"
		}
		e.Set("type", Str(typ))
		e.Set("code", &Value{Kind: Null})
	}
	b.Set("error", e)
	return b.Stringify()
}
```

  Change `RateLimited(key, wait)` to `RateLimited(key, wait, sh Shape)` (`shapeError(sh, 429, msg, "rate_limit_error",
  "", wait)`), `quota(msg)` to `quota(msg, sh)`, and `refusal(p, …)` to build its body with `shapeError(p.Shape, …)`
  (keeping the line). The OpenAI shape gives today's bytes: `TestChatGuardFixtures` must stay green.

- [ ] **Step 6: Implement `xlate.go`** (translated part; Task 5 adds the passthrough branches before it):

```go
// planTranslated is translated() → proxyChat for a body already translated to chat (J2): format is the client's.
func (s *State) planTranslated(r *Request, now int64, key *ipc.GatewayKeyAuth, clientModel string, tagged translate.Tagged,
	custom []string, format string, sh Shape) Plan {
	b, ok := jsv.Stringify(tagged.Body)
	if !ok {
		return handoff("translate: the request cannot be translated")
	}
	oai, err := Parse([]byte(b)) // what JSON.stringify sends (Decision 7)
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	m := oai.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	model, ok := s.MapModel(m.S)
	if !ok {
		return handoff("model: mapping needs the model list")
	}
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed")
	}
	t, why := s.target(model, key)
	if why != "" {
		return handoff(why)
	}
	cred, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
	if s.Gateway.TokenSaver && !strings.EqualFold(strings.Join(r.Header.Values("X-Zp-Token-Saver"), ", "), "off") &&
		hasToolMessage(oai) {
		return handoff("token saver: tool results")
	}
	stream := oai.Get("stream").Truthy()
	out := oai.Clone()
	if stream && StreamUsageOf(t.Provider) {
		so := oai.Get("stream_options")
		merged := NewObject()
		switch {
		case so != nil && so.Kind == Object:
			merged = so.Clone()
		case so != nil && (so.Kind == String && so.S != "" || so.Kind == Array && len(so.A) > 0):
			return handoff("stream: stream_options is not an object")
		}
		merged.Set("include_usage", &Value{Kind: Bool, B: true})
		out.Set("stream_options", merged)
	}
	p := Plan{Key: key, Target: t, Model: model, ClientModel: clientModel, Stream: stream, Format: format, Shape: sh,
		URL: t.Provider.BaseURL + "/chat/completions", Header: providerHeader(t, cred), Upstream: UpChat,
		GuardFormat: "openai-chat", Env: translate.Default, Soft: tagged.Soft, Dropped: tagged.Dropped, Custom: custom,
		Base: s.base(key, t, model, r)}
	f := format
	p.Base.Format = &f
	if model != m.S {
		req := jsSlice(m.S, 80)
		p.Base.Requested = &req
	}
	if stream {
		v := true
		p.Base.Stream = &v
	}
	p.build = func(t *Target, b *Value) []byte {
		b.Set("model", Str(t.Model))
		AdaptChatTagged(t.Provider, t.Model, b, p.Soft, &p.Dropped)
		return b.Stringify()
	}
	if FormatOf(t.Provider) == "anthropic" {
		if why := planAnthropicLeg(&p, cred, out); why != "" {
			return handoff(why)
		}
	}
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, out
	} else {
		p.Body = p.build(t, out)
	}
	return p
}

// PlanMessages is messagesApi: the Anthropic passthrough (Task 5) or the translated call.
func (s *State) PlanMessages(r *Request, now int64) Plan {
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	if msgs := body.Get("messages"); msgs == nil || msgs.Kind != Array {
		return handoff("body: messages is not an array")
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	if p, done := s.planAnthropicNative(r, now, key, body, m.S); done { // Task 5; until then returns false
		return p
	}
	tagged, err := translate.AnthropicToOpenAI(toJSV(body))
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	p := s.planTranslated(r, now, key, m.S, tagged, nil, "anthropic", ShapeAnthropic)
	p.Class = map[bool]string{false: SubMessages, true: SubMessagesStream}[p.Stream]
	return p
}

// PlanResponses is responsesApi: the native passthrough (Task 5) or the translated call.
func (s *State) PlanResponses(r *Request, now int64) Plan {
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	m := body.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return handoff("model: missing")
	}
	if p, done := s.planResponsesNative(r, now, key, body, m.S); done { // Task 5
		return p
	}
	tagged, custom, err := translate.ResponsesToOpenAI(toJSV(body))
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	p := s.planTranslated(r, now, key, m.S, tagged, custom, "responses", ShapeOpenAI)
	p.Class = map[bool]string{false: SubResponses, true: SubResponsesStream}[p.Stream]
	return p
}

// PlanGemini is geminiApi (countTokens: Task 6).
func (s *State) PlanGemini(r *Request, now int64) Plan {
	model, action, ok := geminiPath(r.RawPath)
	if !ok {
		if _, _, matched := geminiPathShape(r.RawPath); matched {
			return handoff("model: the path does not decode")
		}
		return handoff("path: not a Gemini model call")
	}
	if action == "countTokens" {
		return s.planGeminiCount(r, now) // Task 6
	}
	key, body, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	stream := action == "streamGenerateContent"
	o, err := translate.GeminiToOpenAI(toJSV(body), model, stream)
	if err != nil {
		return handoff("translate: the request cannot be translated")
	}
	p := s.planTranslated(r, now, key, model, translate.Tagged{Body: o, Dropped: []string{}}, nil, "gemini", ShapeGemini)
	p.Class = map[bool]string{false: SubGemini, true: SubGeminiStream}[stream]
	return p
}
```

  `geminiPathShape` is `geminiPath` without the decode (to tell "no match" from "does not decode"). Check against
  `geminiToOpenAI`'s return: if stream B's `GeminiToOpenAI` returns a `Tagged`, use its `Soft`/`Dropped` instead of
  the empty list (the JS `geminiToOpenAI` returns a tagged body; follow the code). Every hand-off text above must
  match `TestTranslatedHandoffs`.

- [ ] **Step 7: Generalize `State.Guard`** (`guard.go`): `check` calls `checkBody(toJSV(p.pending), *p.Policy,
  p.guardFormat())` (`"openai-chat"` when empty) and `Guard` sets `p.Body = p.build(p.Target, body)` (falling back to
  `finishBody` when `build` is nil). The refusal is `shapeError(p.Shape, …)` with the line.

- [ ] **Step 8: Implement `xlate_answer.go`.**

```go
// RespondTranslated is translated() for a whole answer (status, raw: the provider's; ms: since the start).
func (s *State) RespondTranslated(p *Plan, status int, raw []byte, ms float64) Answer {
	ok := status >= 200 && status <= 299
	if ok && p.Upstream == UpAnthropic {
		conv, good := anthropicWhole(p.Env, raw, p.ClientModel)
		if !good {
			return s.Failed(p, "the provider's answer is not JSON", ms) // Decision 26a: JS writes V8's text
		}
		raw = conv
	}
	text := decodeWHATWG(raw)
	rec := p.Base
	rec.Status, rec.Ms = int64(status), ms
	model := rec.Model
	if rec.RoutedTo != nil {
		model = *rec.RoutedTo
	}
	if !ok {
		j, _ := jsv.Parse([]byte(text))
		e := jsv.Get(j, "error") // j is nil (JS null) when not JSON: (j && …) → Get on nil must not panic here
		msg := "HTTP " + NumberString(float64(status))
		switch {
		case j != nil && jsv.Truthy(e) && jsv.Truthy(jsv.Or(jsv.Get(e, "message"), e)):
			msg = jsv.ToString(jsv.Or(jsv.Get(e, "message"), e))
		case text != "":
			msg = text
		}
		masked := s.mask(jsSlice(msg, 300))
		rec.Error = &masked
		rec.Cost = s.Pricer.Cost(model, 0, 0, 0)
		return Answer{Status: status, Header: answerHeader(), Body: providerError(p.Format, status, masked), Usage: rec}
	}
	j, err := jsv.Parse([]byte(text))
	if err != nil {
		j = jsv.NewObject() // r.json().catch(() => ({}))
	}
	in, out, cached := tokensJSV(j)
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	rec.Cost = s.Pricer.Cost(model, float64(in), float64(out), float64(cached))
	var whole *jsv.Object
	switch p.Format {
	case "anthropic":
		whole, err = p.Env.OpenAIToAnthropic(j, p.ClientModel)
	case "responses":
		whole, err = p.Env.OpenAIToResponses(j, p.ClientModel, p.Custom)
	default:
		whole, err = p.Env.OpenAIToGemini(j, p.ClientModel)
	}
	if err != nil { // out.whole threw after the line was written: the route's 502 with the TypeError text
		a := shapeError(p.Shape, http.StatusBadGateway, s.mask(err.Error()), "upstream_error", "", "")
		a.Usage, a.NoLine = rec, false
		return a
	}
	h := answerHeader()
	if len(p.Dropped) > 0 {
		h.Set("X-Zp-Dropped", jsSlice(strings.Join(p.Dropped, ", "), 500))
	}
	return Answer{Status: 200, Header: h, Body: []byte(jsv.MustStringify(whole)), Usage: rec}
}
```

  `tokensJSV` is `tokens` over a `jsv` value (same whole-number rule). Use the `jsv` helpers that exist
  (`jsv.Get` panics a `TypeError` on `null`: wrap the error branch in `jsv.Catch` or test `j != nil` first, as above;
  if `jsv.Or` does not exist, write the `||` inline: first truthy of `message` and `e`). The fixtures decide every
  byte; adjust to them, never the other way round.

- [ ] **Step 9: Register** `messages`, `responses`, `gemini` (whole cases: request without truthy `stream` and not
  `streamGenerateContent`) in `xlatePorted`: plan with `PlanFor`, give the plan `counterEnv()`, run `s.Budget` with the
  fixture's snapshot (a 402 answer is compared as the client answer with `usage: null`), `s.Guard` (a refusal answer
  is compared with its line), then `RespondTranslated` on the fixture provider (`throw` → `s.Failed` then the shaped
  502: `shapeError(p.Shape, 502, s.mask(id+": "+why), "upstream_error", "", "")` with the 599 line). Compare
  `want.Requests[0]` (URL, lower-cased headers, body), status, the five headers and body bytes, and the usage line
  (without `ms`, `ttft` rule as `gen-chat.js`).
- [ ] **Step 10: Run** `go -C gw test -race ./engine`. Expected: PASS; the whole `messages`, `responses`, `gemini`
  fixtures pass, stream ones still skip.
- [ ] **Step 11: Commit** "Serve translated Anthropic, Responses and Gemini calls whole in the go engine, byte for byte with the JS gateway" and push.

### Task 4: Translated streams

**Files:**
- Create: `gw/engine/xlate_stream.go`, `gw/engine/xlate_stream_test.go`
- Modify: `gw/engine/xlate_test.go` (register the stream cases)

**Interfaces:**
- Consumes: `translate.NewAnthropicStream`, `NewResponsesStream`, `NewGeminiStream` (on `p.Env`), `WholeToChunks`,
  `ReadSSE`; Task 2's `AnthropicToChatText`; `usageOf` rules (`tokensJSV`).
- Produces: `TranslatedStream`, `NewTranslatedStream`, `Open`, `Ping`, `Run`, `Line`, `ErrCut` (the reader error a
  fixture's `cut` gives, message `terminated`).

- [ ] **Step 1: Failing tests.**

```go
// Every split of the provider's bytes gives the same client text (one byte per read, and one read).
func TestTranslatedStreamSplits(t *testing.T) {
	for _, f := range loadFixtures(t, "xlate") {
		c, want := xlateDecode(t, f)
		if !xlateIsStream(c) || xlateProviderOf(c).Throw != "" || xlateProviderOf(c).Status != 200 {
			continue
		}
		whole := runStream(t, c, iotest.OneByteReader)
		if whole != want.Client.Body {
			t.Errorf("%s: one byte per read differs", f.Name)
		}
	}
}

// One held translated stream costs at most 80 KiB (P4c Decision 19's budget; Decision 24).
func TestMemoryPerHeldTranslatedStream(t *testing.T) {
	const n = 200
	held := heldStreams(t, n, "/v1/messages") // n streams opened through httptest, each paused after its first event
	if per := held.bytesPerStream(); per > 80<<10 {
		t.Fatalf("%d bytes per held stream", per)
	}
}
```

  `runStream` plans the fixture, builds `NewTranslatedStream`, writes `Open()`, calls `Run` with a reader over the
  fixture chunks (wrapped by the given wrapper, ending in `ErrCut` when `cut`) and the provider's content type, and
  returns the written text. `heldStreams` follows `gw/engine/memstream_test.go` (copy its measuring helper; use the
  serve path from Task 8 once it exists — until then measure `NewTranslatedStream` + a paused `Run` goroutine and
  note in the test which one it measures).

- [ ] **Step 2: Run** `go -C gw test ./engine -run 'TranslatedStream|MemoryPerHeldTranslated'`. Expected: FAIL.

- [ ] **Step 3: Implement `xlate_stream.go`.**

```go
// clientStream is the writer of the client's format (translate.AnthropicStream, ResponsesStream, GeminiStream).
type clientStream interface {
	Push(c jsv.Value) (string, error)
	Finish() (string, error)
	Error(msg jsv.Value) string
}

// ErrCut is how a provider that breaks off reads (the JS stream's error text on Node: "terminated").
var ErrCut = errors.New("terminated")

// TranslatedStream is translated()'s stream branch (J2), pure over the provider's body: the caller writes the
// headers, Open, the pings, and emits Line once Run returns.
type TranslatedStream struct {
	p       *Plan
	s       *State
	out     clientStream
	open    func() string
	ping    func() string
	elapsed func() float64
	ttft    *float64
	in, ou, cached int64
}

func (s *State) NewTranslatedStream(p *Plan, elapsed func() float64) *TranslatedStream {
	t := &TranslatedStream{p: p, s: s, elapsed: elapsed}
	switch p.Format {
	case "anthropic":
		a := p.Env.NewAnthropicStream(p.ClientModel)
		t.out, t.open, t.ping = a, a.Open, a.Ping
	case "responses":
		r := p.Env.NewResponsesStream(p.ClientModel, p.Custom)
		t.out, t.open = r, r.Open
	default:
		t.out = p.Env.NewGeminiStream(p.ClientModel)
	}
	return t
}

func (t *TranslatedStream) Open() string {
	if t.open == nil {
		return ""
	}
	return t.open()
}

func (t *TranslatedStream) Ping() string {
	if t.ping == nil {
		return ""
	}
	return t.ping()
}

// Run reads the provider's answer and writes the client's events through write (which the caller serializes with the
// pings). It returns when the answer ends, the provider breaks off, a write fails, or a step throws; in the last two
// JS cases an error event is written (s.error(mask(err.message))).
func (t *TranslatedStream) Run(body io.Reader, contentType string, write func(string) error) {
	if t.p.Upstream == UpAnthropic {
		body, contentType = AnthropicToChatText(t.p.ClientModel, body, t.p.Env), "text/event-stream"
	}
	fail := func(msg string) { _ = write(t.out.Error(t.s.mask(msg))) }
	onChunk := func(c jsv.Value) error {
		if e := jsv.Get(c, "error"); jsv.Truthy(e) {
			m := jsv.Get(e, "message")
			if !jsv.Truthy(m) {
				m = "upstream error"
			}
			return errors.New(jsv.ToString(m)) // new Error(x): String(x)
		}
		w, err := t.out.Push(c)
		if err != nil {
			return err
		}
		if w != "" {
			return writeErr(write(w))
		}
		return nil
	}
	var err error
	if strings.Contains(contentType, "json") {
		raw, rerr := io.ReadAll(body)
		if rerr != nil {
			fail(rerr.Error())
			return
		}
		j, perr := jsv.Parse([]byte(decodeWHATWG(raw)))
		if perr != nil {
			fail("the provider's answer is not JSON") // Decision 26a
			return
		}
		chunks, cerr := translate.WholeToChunks(j)
		if cerr != nil {
			fail(cerr.Error())
			return
		}
		for _, c := range chunks {
			if err = onChunk(c); err != nil {
				break
			}
		}
		ms := t.elapsed()
		t.ttft = &ms
		t.in, t.ou, t.cached = tokensJSV(j)
	} else {
		err = translate.ReadSSE(body, func(_, data string) error {
			if data == "[DONE]" {
				return nil
			}
			c, perr := jsv.Parse([]byte(data))
			if perr != nil {
				return nil
			}
			if d := firstDelta(c); t.ttft == nil && d != nil && (jsv.Truthy(jsv.Get(d, "content")) || jsv.Truthy(jsv.Get(d, "tool_calls"))) {
				ms := t.elapsed()
				t.ttft = &ms
			}
			if jsv.Truthy(jsv.Get(c, "usage")) {
				t.in, t.ou, t.cached = tokensJSV(c)
			}
			return onChunk(c)
		})
	}
	if errors.Is(err, errWrite) {
		return
	}
	if err != nil {
		fail(err.Error())
		return
	}
	w, ferr := t.out.Finish()
	if ferr != nil {
		fail(ferr.Error())
		return
	}
	_ = write(w)
}

// errWrite marks a failed client write: the stream stops without an error event (the client is gone).
var errWrite = errors.New("client write failed")

func writeErr(err error) error {
	if err != nil {
		return errWrite
	}
	return nil
}

// Line is translated()'s line after a stream: status 200, the time to the first token, the last usage seen.
func (t *TranslatedStream) Line(ms float64) ipc.UsageRecord {
	rec := t.p.Base
	rec.Status, rec.Ms = 200, ms
	in, out, cached := t.in, t.ou, t.cached
	rec.Input, rec.Output, rec.Cached = &in, &out, &cached
	rec.TTFT = ipc.Null[float64]()
	if t.ttft != nil {
		rec.TTFT = ipc.Some(*t.ttft)
	}
	model := rec.Model
	if rec.RoutedTo != nil {
		model = *rec.RoutedTo
	}
	rec.Cost = t.s.Pricer.Cost(model, float64(in), float64(out), float64(cached))
	return rec
}
```

  `firstDelta(c)` is `c.choices && c.choices[0] && c.choices[0].delta` without throwing (JS `&&` chain: a non-object
  `choices` gives `undefined`, never a TypeError). `ReadSSE` returns `ErrSSETooLarge` for an event over 16 MiB; that
  error's text goes into the error event (Decision 26c). A provider read error from the serve loop is `ErrCut`'s text
  (`terminated`, Decision 26b) — the serve loop wraps the provider body so any read error other than EOF reads as
  `ErrCut`.

- [ ] **Step 4: Register** the stream cases in `xlatePorted` (`runStream` with a plain reader; client headers:
  `content-type: text/event-stream`, `cache-control: no-store`, CORS, `x-zp-dropped` from `p.Dropped`; line via
  `Line(1)` compared as `gen-chat.js` streams are, `ttft` as `set`/`null`).
- [ ] **Step 5: Run** `go -C gw test -race ./engine`. Expected: PASS, every translated stream fixture byte-equal.
- [ ] **Step 6: Commit** "Serve translated Anthropic, Responses and Gemini streams in the go engine, byte for byte with the JS gateway" and push.

### Task 5: Native passthroughs (Anthropic Messages and Responses)

**Files:**
- Create: `gw/engine/passthrough.go`, `gw/engine/passthrough_test.go`
- Modify: `gw/engine/xlate.go` (`planAnthropicNative`, `planResponsesNative` replace their stubs), `xlate_test.go`

**Interfaces:**
- Consumes: Tasks 2–4 (`admit`, `target`, `credential`, `providerHeader`, `MapModel`, `decodeWHATWG`, `ErrCut`).
- Produces: `planAnthropicNative`, `planResponsesNative`, `RespondNative`, `NativeStream`, `NewNativeStream`,
  `(*NativeStream).Run`, `Line`; `UpAnthropicNative`, `UpResponsesNative`; `Plan.AnthropicHeaders`.

- [ ] **Step 1: Failing tests.**

```go
// The client's anthropic-version and anthropic-beta reach the provider as anthropicHeaders keeps them.
func TestAnthropicHeadersKept(t *testing.T) {
	s := xlateState(t)
	r := xlateReq("/v1/messages", `{"model":"an/claude-x","messages":[]}`)
	r.Header.Set("Anthropic-Beta", "context-1m")
	r.Header.Set("Anthropic-Version", strings.Repeat("x", 1001))
	p := s.PlanMessages(r, fixtureNow)
	if p.Upstream != UpAnthropicNative || p.Header.Get("Anthropic-Beta") != "context-1m" ||
		p.Header.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("%+v", p.Header)
	}
}

// A passthrough event's data goes out as it came unless its JSON is the one rewritten.
func TestPassthroughEventRewrite(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"up\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" + "event: x\ndata: not json\n\n"
	got := runNative(t, "/v1/messages", `{"model":"an/claude-x","stream":true,"messages":[]}`, in)
	want := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"an/claude-x\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" + "event: x\ndata: not json\n\n"
	if got != want {
		t.Fatalf("%q", got)
	}
}
```

- [ ] **Step 2: Run** `go -C gw test ./engine -run 'AnthropicHeadersKept|PassthroughEventRewrite'`. Expected: FAIL.

- [ ] **Step 3: Implement the planners.**

```go
// planAnthropicNative is messagesApi's passthrough test and anthropicPassthrough's checks (J4). done false: not a
// passthrough, the caller translates.
func (s *State) planAnthropicNative(r *Request, now int64, key *ipc.GatewayKeyAuth, body *Value, name string) (Plan, bool) {
	model, ok := s.MapModel(name)
	if !ok {
		return handoff("model: mapping needs the model list"), true
	}
	first, why := s.target(model, key)
	if why != "" {
		return handoff(why), true // resolveAll(...)[0]: a route, or no usable target, decides the path; JS answers
	}
	if FormatOf(first.Provider) != "anthropic" || model != first.Name {
		return Plan{}, false
	}
	if Authorize(key, "llm", model, "") != "" {
		return handoff("key: not allowed"), true
	}
	cred, why := s.credential(first)
	if why != "" {
		return handoff(why), true
	}
	if s.tokenSaverOn(r) && hasToolResultBlock(body) {
		return handoff("token saver: tool results"), true
	}
	stream := body.Get("stream").Truthy()
	p := Plan{Key: key, Target: first, Model: model, ClientModel: name, Stream: stream, Format: "anthropic",
		Shape: ShapeAnthropic, Upstream: UpAnthropicNative, GuardFormat: "anthropic", Env: translate.Default,
		URL: first.Provider.BaseURL + "/messages", Header: providerHeader(first, cred), Base: s.base(key, first, model, r),
		Class: map[bool]string{false: SubMessages, true: SubMessagesStream}[stream]}
	for _, k := range []string{"Anthropic-Version", "Anthropic-Beta"} {
		if v := r.Header.Get(k); v != "" && utf16Len(v) <= 1000 && !strings.ContainsAny(v, "\r\n") {
			p.Header.Set(k, v) // the client's own, after the credential headers (they never replace the credential)
		}
	}
	f := "anthropic"
	req := jsSlice(name, 80)
	p.Base.Format, p.Base.Requested, p.Base.RoutedTo = &f, &req, nil // `requested` always (Decision 27)
	if stream {
		v := true
		p.Base.Stream = &v
	}
	out := body.Clone()
	out.Set("model", Str(first.Model)) // guard({...body, model: t.model}) then the same body upstream
	p.build = func(_ *Target, b *Value) []byte { return b.Stringify() }
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(first.Provider, first.Model)) {
		p.Policy, p.pending = &pol, out
	} else {
		p.Body = out.Stringify()
	}
	return p, true
}
```

  `hasToolResultBlock` is true when any message's `content` is an array holding an object with `type ===
  "tool_result"`, or a message is not an object (saveTokens would throw: JS answers). `tokenSaverOn` is the existing
  inline test, factored. `planResponsesNative` mirrors J5: `MapModel`, `target`, `NativeResponses(t.Provider)`
  (`FormatOf == "openai"` and `responses` flag or preset `openai`/`azure-openai`), `Authorize`, `credential`, URL
  `<base>/responses`, guard format `responses`, `requested` only when the mapped model differs, `routedTo` when
  `t.Name != model`, **no token saver** (J5). Add `NativeResponses` to `providers.go` and the preset flag
  `Responses: true` for `openai` and `azure-openai` to `Presets` (and to the `00-presets.json` fixture's generator in
  `gen-chat.js` only if that fixture lists it; else keep the flag in a separate map so `TestChatFixtures` stays
  unchanged).

- [ ] **Step 4: Implement the answers.** `RespondNative(p, status, raw, ms)`:
  - Anthropic: `text := decodeWHATWG(raw)`; `j` parsed (nil when not JSON); tokens only when `j.usage` is truthy
    (`anthropicUsageToOpenAI` then the `usageOf` rule); `error` = `mask(jsSlice(text, 200))` on a non-2xx; the body:
    a 2xx with a string `model` → `JSON.stringify({...j, model: clientModel})` (`jsv` `Clone` + `Set` keeps the key's
    position), else `text` on a 2xx and `mask(text)` otherwise; headers `answerHeader()`.
  - Responses: `error` = `mask(jsSlice((j.error.message) || text, 200))`; tokens `responsesUsage(j.usage)` only when
    `j && ok`; the same model rewrite.
  `NativeStream.Run(body, write)`: `ReadSSE` → for each `(ev, data)`: parse `data` (failure: pass it as it came);
  Anthropic: a `message_start` with a string `message.model` → `event: <ev>\ndata: <{...o, message: {...o.message,
  model}}>\n\n`, else `event: <ev>\ndata: <data>\n\n`; usage `au = {...o.message.usage}` / `au = {...au,
  ...o.usage}` inside a catch; `ttft` on the first `content_block_delta`. Responses: the head is `""` when `ev ==
  "message"`, else `event: <ev>\n`; rewrite `o.response.model`; `ttft` on `/^response\.(output_text|function_call_arguments|custom_tool_call_input)\.delta$/`
  (a fixed set of three strings: compare with `==`); usage from `o.response.usage`. Any read error ends the loop
  quietly (J4/J5 `catch {}`); `Line` is status 200 with `ttft` and the merged usage.
- [ ] **Step 5: Register** passthrough cases (they share `fn` `messages`/`responses`; the plan's `Upstream` picks the
  answer builder) and run `go -C gw test -race ./engine`. Expected: PASS.
- [ ] **Step 6: Commit** "Serve the native Anthropic and Responses passthroughs in the go engine as the JS gateway forwards them" and push.

### Task 6: `count_tokens`, Gemini `countTokens` and embeddings

**Files:**
- Create: `gw/engine/count.go`, `gw/engine/embeddings.go`, `gw/engine/count_test.go`
- Modify: `gw/engine/upstream.go` (`Plan.Timeout`), `gw/engine/guard.go` (a count plan's block or failure is the
  estimate), `gw/engine/xlate.go` (`planGeminiCount`), `xlate_test.go`

**Interfaces:**
- Consumes: `translate.EstimateTokens`, earlier tasks' helpers.
- Produces: `PlanCount`, `planGeminiCount`, `PlanEmbeddings`, `RespondCount`, `RespondEmbeddings`, `UpCount`,
  `UpEmbeddings`, `UpNone`, `Plan.Self`, `Plan.Timeout`.

- [ ] **Step 1: Failing tests.**

```go
func TestCountGuardBlockIsTheEstimate(t *testing.T) {
	s := xlateStateWithPolicy(t, `{"scope":"all","secrets":"block","pii":"off","injection":"off"}`)
	p := s.PlanCount(xlateReq("/v1/messages/count_tokens",
		`{"model":"an/claude-x","messages":[{"role":"user","content":"key sk-ant-api03-`+strings.Repeat("A", 90)+`"}]}`), fixtureNow)
	if a := s.Guard(&p, 0); a == nil || a.Status != 200 || !a.NoLine || !strings.HasPrefix(string(a.Body), `{"input_tokens":`) {
		t.Fatalf("%+v", a)
	}
}

func TestCountTimeoutIs15s(t *testing.T) {
	p := xlateState(t).PlanCount(xlateReq("/v1/messages/count_tokens", `{"model":"an/claude-x","messages":[]}`), fixtureNow)
	if p.Upstream != UpCount || p.Timeout != 15*time.Second {
		t.Fatalf("%+v", p)
	}
}

// /v1/embeddings does not abort on client close (J8): the call goes on and its line is written.
func TestEmbeddingsPlanIgnoresClientClose(t *testing.T) {
	p := xlateState(t).PlanEmbeddings(xlateReq("/v1/embeddings", `{"model":"oa/e1","input":"x"}`), fixtureNow)
	if !p.Detached {
		t.Fatal("embeddings must not follow the client's context")
	}
}
```

  (Build the secret-shaped string by concatenation, as P4e's tests do; never a literal key.)

- [ ] **Step 2: Run** `go -C gw test ./engine -run 'Count|Embeddings'`. Expected: FAIL.

- [ ] **Step 3: Implement `count.go`.**

```go
// estimate is countTokens' estimate(): { input_tokens: estimateTokens({ system, messages, tools }) }.
func estimate(b *Value) *Answer {
	o := jsv.NewObject()
	for _, k := range []string{"system", "messages", "tools"} {
		if v := b.Get(k); v != nil {
			o.Set(k, toJSV(v))
		} // absent stays out: JSON.stringify drops undefined
	}
	body := NewObject()
	body.Set("input_tokens", Num(float64(translate.EstimateTokens(o))))
	return &Answer{Status: 200, Header: answerHeader(), Body: body.Stringify(), NoLine: true}
}

// PlanCount is /v1/messages/count_tokens (J7): an estimate the engine answers, or the native provider's count.
func (s *State) PlanCount(r *Request, now int64) Plan {
	key, b, why := s.admit(r, now, "llm")
	if why != "" {
		return handoff(why)
	}
	self := Plan{Key: key, Upstream: UpNone, Class: SubMessagesCount, Shape: ShapeAnthropic, Self: estimate(b)}
	m := b.Get("model")
	if m == nil || m.Kind != String || m.S == "" {
		return self
	}
	model, ok := s.MapModel(m.S)
	if !ok {
		return handoff("model: mapping needs the model list")
	}
	t, why := s.target(model, key)
	if why == "model: a route" {
		return handoff(why)
	}
	if t == nil || FormatOf(t.Provider) != "anthropic" || Authorize(key, "llm", model, "") != "" {
		return self
	}
	cred, why := s.credential(t)
	if why != "" {
		return handoff(why)
	}
	body := NewObject()
	body.Set("model", Str(t.Model))
	for _, f := range []string{"messages", "system", "tools", "tool_choice", "thinking"} {
		if v := b.Get(f); v != nil {
			body.Set(f, v)
		}
	}
	p := Plan{Key: key, Target: t, Model: model, Upstream: UpCount, Class: SubMessagesCount, Shape: ShapeAnthropic,
		GuardFormat: "anthropic", URL: t.Provider.BaseURL + "/messages/count_tokens", Header: providerHeader(t, cred),
		Timeout: 15 * time.Second, Self: self.Self, build: func(_ *Target, v *Value) []byte { return v.Stringify() }}
	addAnthropicHeaders(&p, r)
	if pol := PolicyFor(s.Gateway.Guardrails, key); checksApply(pol, ScopeTier(t.Provider, t.Model)) {
		p.Policy, p.pending = &pol, body
	} else {
		p.Body = body.Stringify()
	}
	return p
}

// RespondCount: a 2xx JSON with a finite input_tokens is returned re-serialized; anything else is the estimate.
func (s *State) RespondCount(p *Plan, status int, raw []byte) Answer {
	if status >= 200 && status <= 299 {
		if j, err := jsv.Parse([]byte(decodeWHATWG(raw))); err == nil && jsv.Truthy(j) {
			if n, ok := jsv.Get(j, "input_tokens").(float64); ok && !math.IsInf(n, 0) && !math.IsNaN(n) {
				return Answer{Status: 200, Header: answerHeader(), Body: []byte(jsv.MustStringify(j)), NoLine: true}
			}
		}
	}
	return *p.Self
}
```

  `j && Number.isFinite(j.input_tokens)`: `jsv.Get` on a non-object `j` (a number, a string) must not panic — only
  `null`/`undefined` throw, and `Truthy` excludes `null`; `0`, `""` and `false` are falsy, so a truthy primitive
  reaches `Get` and gives `Undefined`. `planGeminiCount`: `admit(r, now, "llm")`, then `Self` = `{"totalTokens":
  EstimateTokens(body.contents || body.generateContentRequest || body)}` (first truthy), `Class: SubGeminiCount`,
  `Shape: ShapeGemini`, `Upstream: UpNone`. Factor `addAnthropicHeaders` out of Task 5's loop.
  `Upstream.Start`: `if p.Timeout > 0 { timeout = p.Timeout }`. A failed count call (throw, timeout) is `*p.Self`.
  `State.Guard` for a plan with `Upstream == UpCount`: a refusal (block or failure) returns `p.Self`; a redaction goes
  upstream (`build`).

- [ ] **Step 4: Implement `embeddings.go`.** `PlanEmbeddings`: `admit(r, now, "llm")`; `model := body.model` must be
  a non-empty string (else hand off: JS answers its 403/404); `Authorize(key, "llm", model, "")`; `target`
  (routes handed off); `credential`; `Upstream: UpEmbeddings`, `Class: SubEmbeddings`, `Shape: ShapeOpenAI`,
  `GuardFormat: "embeddings"`, `Detached: true`; the guarded body `{...body, model: t.model}`; `Base` with `Kind:
  "embeddings"`, **no** `format`, `stream`, `routedTo`, `requested`; the budget uses `model` (`modelOf: () =>
  body.model`, which is `t.Name`). `RespondEmbeddings(p, status, raw, ms)`: `text := decodeWHATWG(raw)`; line
  `{…base, status, input: usage.prompt_tokens || 0 (whole-number rule), output: 0, guard?}`, cost
  `Pricer.Cost(model, input, 0, 0)`, **no error text**; headers: `Content-Type: application/json` and CORS only (no
  `Cache-Control`); body `text` on a 2xx, `mask(text)` otherwise. A thrown call is `shapeError(ShapeOpenAI, 502,
  mask(id+": "+why), "upstream_error", "", "")` with `NoLine` (J8). `Plan.Detached` (new field) tells the serve loop
  to call with `context.WithoutCancel(r.Context())`.
- [ ] **Step 5: Register** `count`, `geminiCount`, `embeddings` and run `go -C gw test -race ./engine`. Expected: PASS.
- [ ] **Step 6: Commit** "Serve count_tokens, Gemini countTokens and embeddings in the go engine as the JS gateway answers them" and push.

### Task 7: The shadow for translated calls (JS capture with a tape; Go replay and compare)

**Files:**
- Modify: `lib/gateway/shadow.js`, `lib/gateway/providers.js` (`request` returns `shadow.teeResponse(r)`),
  `lib/gateway/translate.js` (`rid`, clock reads through the tape), `lib/gateway/usage.js` (`record` notes the line),
  `gw/engine/shadow.go` (`Capture` grows; `Shadow` handles every class; the compare dispatches by sub-class),
  `gw/engine/xlate_test.go` (an unported `fn` now fails)
- Create: `gw/engine/shadow_xlate.go`, `gw/engine/shadow_xlate_test.go`, `test/shadow-capture.test.js`
- **(re-check after P4f merges)** `test/front-shadow.test.js`

**Interfaces:**
- Consumes: every planner and answer builder of Tasks 2–6.
- Produces: `CompareTranslated`, `tapeEnv(c *Capture) (*translate.Env, func() string)` (the second returns a diff
  text when the tape was over- or under-read), `stripPings(s string) string`; JS `teeResponse`, `tapeRandom`,
  `tapeNow`, the capture shape of the Interfaces section.

- [ ] **Step 1: Failing JS test** (`test/shadow-capture.test.js`, `node:test`): run a gateway in process (as
  `gen-xlate.js` does) with a request marked by a shadow id (`req[shadow.SHADOW_ID] = '0123456789abcdef'`), for a
  translated `/v1/messages` stream to an OpenAI provider; then `shadow.take('0123456789abcdef')` (the existing reader
  the internal port uses) must hold `upstream[0]` = `{ status: 200, contentType: 'text/event-stream', body: <the
  provider text>, ended: 'eof', truncated: false }`, `client.status` 200, `client.headers['content-type']`
  `text/event-stream`, `client.body` = what the response received, `usage.format` `anthropic`, and `tape.random`
  holding one 24-hex entry per id in the client text (`msg_…`), `tape.now` the clock reads. A request with no
  shadow id calls nothing new (spy on `crypto.randomBytes`: the same count as before the change).
- [ ] **Step 2: Run** `node --test test/shadow-capture.test.js`. Expected: FAIL (`upstream` undefined).
- [ ] **Step 3: Implement the JS side.**

```js
// lib/gateway/shadow.js (additions)
/** translate.js rid: the random bytes a captured request's translation drew (P4d tape). @param {Uint8Array} buf */
function tapeRandom(buf) {
  const s = als.getStore();
  if (s && s.tape.random.length < 4096) s.tape.random.push(Buffer.from(buf).toString('hex'));
}
/** translate.js: a clock read of a captured request's translation. @param {number} ms */
function tapeNow(ms) {
  const s = als.getStore();
  if (s && s.tape.now.length < 4096) s.tape.now.push(ms);
}
const NULL_BODY = new Set([101, 204, 205, 304]);
/**
 * providers.request: the provider's answer of a captured request, read as it is consumed (status, content type,
 * decoded text up to STREAM_CAP, whether it ended or was cut). The Response handed back reads the same bytes.
 * @param {Response} r
 */
function teeResponse(r) {
  const s = als.getStore();
  if (!s) return r;
  const rec = { status: r.status, contentType: r.headers.get('content-type') || '', body: '', ended: 'cut', truncated: false };
  s.upstream.push(rec);
  if (!r.body || NULL_BODY.has(r.status)) {
    rec.ended = 'eof';
    return r;
  }
  const dec = new TextDecoder();
  const keep = (t) => {
    if (rec.truncated) return;
    if (rec.body.length + t.length > STREAM_CAP) rec.truncated = true;
    else rec.body += t;
  };
  const body = r.body.pipeThrough(
    new TransformStream({
      transform(chunk, ctrl) {
        keep(dec.decode(chunk, { stream: true }));
        ctrl.enqueue(chunk);
      },
      flush() {
        keep(dec.decode());
        rec.ended = 'eof';
      },
    }),
  );
  return new Response(body, { status: r.status, statusText: r.statusText, headers: r.headers });
}
```

  In `withShadow`: the store gains `upstream: []`, `tape: { random: [], now: [] }`, `clientText: ''`,
  `clientTruncated: false`, `clientHeaders: {}`; when `res` is given, wrap `res.writeHead` (record the status and the
  lower-cased `content-type` and `x-zp-dropped` of the headers object), `res.write` and `res.end` (append the chunk as
  text, capped at `STREAM_CAP`), restoring nothing (the response object dies with the request). The `finally` store
  writes `{ providerRequests, providerResponse: null, upstream, client: { status, headers, body: s.clientBody ??
  s.clientText, truncated }, usage, tape }`; `sizeOf` counts the upstream bodies and the client text. `noteAnswer`
  (P4c chat) adds `upstream` and `tape` too and keeps everything else. In `translate.js`:

```js
const shadow = require('./shadow');
const rid = (p) => {
  const b = crypto.randomBytes(12);
  shadow.tapeRandom(b);
  return `${p}${b.toString('hex')}`;
};
const nowSeconds = () => {
  const ms = Date.now();
  shadow.tapeNow(ms);
  return Math.floor(ms / 1000);
};
```

  and replace the four `Math.floor(Date.now() / 1000)` with `nowSeconds()`. `usage.js record` ends with
  `shadow.noteUsage(entry)` (require `./shadow`). `providers.js request`: `return shadow.teeResponse(r)` after
  `noteResponse(p, r)` in `send` (both the first call and the replay after a 401 go through it).

- [ ] **Step 4: Run** `node --test test/shadow-capture.test.js test/gateway*.test.js test/front-shadow.test.js` and
  every `scripts/fixtures/gen-*.js` (the translate fixtures must not change: the tape is a no-op outside a capture).
  Expected: PASS, no fixture diff.

- [ ] **Step 5: Failing Go tests** (`shadow_xlate_test.go`): for each `xlate` stream and whole fixture, build a
  `Capture` from the fixture (`upstream` from the provider; `client` from `want.Client`; `usage` from `want.Usage`;
  `tape.random` = the counter's bytes in hex for as many draws as the JS made — record that count in the fixture:
  add `out.draws` and `out.nowReads` to `gen-xlate.js` by counting calls of the patched `crypto.randomBytes` and
  `Date.now`), then `CompareTranslated` must say `same`; flip one byte of `client.body` → `diff` on `client.body`;
  insert `event: ping\ndata: {"type":"ping"}\n\n` after the first event of an Anthropic stream's client text →
  still `same`; drop one tape entry → `diff` on `tape`.
- [ ] **Step 6: Implement `shadow_xlate.go`.**

```go
// tapeEnv replays a capture's tape: Random gives the recorded bytes in order, Now the recorded clock reads. The
// returned check says how the replay used the tape ("" when exactly).
func tapeEnv(c *Capture) (*translate.Env, func() string) {
	var ri, ni int
	short := false
	env := &translate.Env{
		Random: func(n int) []byte {
			if c.Tape == nil || ri >= len(c.Tape.Random) {
				short = true
				return make([]byte, n)
			}
			b, _ := hex.DecodeString(c.Tape.Random[ri])
			ri++
			return b
		},
		Now: func() int64 {
			if c.Tape == nil || ni >= len(c.Tape.Now) {
				short = true
				return 0
			}
			ni++
			return c.Tape.Now[ni-1]
		},
	}
	return env, func() string {
		switch {
		case short:
			return "the engine drew more than the tape holds"
		case c.Tape != nil && (ri < len(c.Tape.Random) || ni < len(c.Tape.Now)):
			return "the engine drew less than the tape holds"
		}
		return ""
	}
}

const anthropicPing = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

// stripPings removes the Anthropic keep-alive events: they are timing, not content (Decision 21).
func stripPings(s string) string { return strings.ReplaceAll(s, anthropicPing, "") }
```

  `CompareTranslated(class, p, c)`: the hand-off, rate-limit and budget-lag rules of `Compare` first (factor them into
  `compareSkips`); `c.Client.Truncated` or an upstream `truncated` → `skipped` ("the capture is over its limit");
  the provider requests (`len(c.ProviderRequests)` must equal the plan's: 1, or 0 for an estimate; URL, header set,
  body as `Compare` does); then the replay with `p.Env, check = tapeEnv(c)`: whole → the plan's answer builder on
  `upstream[0]` (status, `[]byte(body)`); a failed call (no upstream, a provider request made) → `skipped` as
  `Compare`; stream → `NewTranslatedStream`/`NewNativeStream` (`Open` first), `Run` over
  `strings.NewReader(upstream[0].Body)` followed by `ErrCut` when `ended == "cut"`, collecting the text; chat-anthropic
  → `AnthropicToChatText` then `ChatStream` as P4c. Compare `client.status`, `content-type` and `x-zp-dropped`,
  `stripPings(c.Client.Body)` with `stripPings(goText)` (digest on a diff), the `usageFields` and `usage.error` and
  cost as `Compare`, and `check()` as a `tape` diff. A count estimate compares status and body only. `Shadow` and
  `shadowRead` work for every path class (`PlanFor(class, …)`, `SubClass`), and the compare goroutine calls
  `CompareTranslated` for every sub-class except `chat`/`chat-stream` (unchanged `Compare`). The `Capture` struct
  gains:

```go
	Upstream []struct {
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		Body        string `json:"body"`
		Ended       string `json:"ended"`
		Truncated   bool   `json:"truncated"`
	} `json:"upstream"`
	Tape *struct {
		Random []string `json:"random"`
		Now    []int64  `json:"now"`
	} `json:"tape"`
```

  and `Client` gains `Headers map[string]string` and `Truncated bool`. In `xlate_test.go`, replace the `t.Skip` by
  `t.Fatalf("no replay for fn %q", f.Fn)`.

- [ ] **Step 7: Run** `go -C gw test -race ./engine` and `node --test test/shadow-capture.test.js test/front-shadow.test.js`.
  Expected: PASS.
- [ ] **Step 8: Commit** "Compare translated calls in the shadow by replaying the provider's text with the translation's own random bytes and clock" and push.

### Task 8: The front's classes and the engine's serving loops **(re-check after P4f merges)**

**Files:**
- Modify: `gw/front/route.go` (`Canonical`), `scripts/fixtures/gen-route.js`, `gw/testdata/route/*`,
  `gw/front/route_test.go`, `gw/front/front.go` (`GoClasses`, sub-class lists, dispatch for every go class),
  `gw/engine/serve.go` (the dispatch only), `lib/front/index.js` (`GO_CLASSES`, `SUB_CLASSES`), `test/front.test.js`
- Create: `gw/engine/dispatch.go`, `gw/engine/serve_xlate.go`, `gw/engine/serve_xlate_test.go`

**Interfaces:**
- Consumes: everything before; P4f's `open`, `emitLine(rec, unchecked, call)` and answer/span helper when merged.
- Produces: `(*Engine).Serve` for classes `chat`, `messages`, `responses`, `gemini`, `embeddings`;
  `serveWhole`, `serveTranslatedStream`, `serveNativeStream`; `PingEvery` (a package variable, 10 s; tests shorten
  it); front `Subs` use; JS `GO_CLASSES` with the eleven sub-classes.

- [ ] **Step 1: Route fixtures first.** Add to `RAW` in `gen-route.js`: `'/v1beta/models/oa%2fm1:generateContent'`,
  `'/v1beta/models/caf%C3%A9:generateContent'`, `'/v1/%2e%2E/x'`, `'/v1/.%2e/x'`, `'/v1/%2E/x'`, `'/v1/a%zz'`,
  `'/v1/a%'`. Regenerate on Node and Deno (no diff between them). Failing test in `route_test.go`:

```go
func TestCanonicalPercentEscapes(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1beta/models/oa%2Fm1:generateContent": true, "/v1beta/models/oa%2fm1:generateContent": true,
		"/v1/chat%2Fcompletions": true, "/v1/%2e/x": false, "/v1/%2E%2E/x": false, "/v1/.%2e/x": false,
		"/v1/a%zz": false, "/v1/a%": false,
	} {
		if Canonical(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
	if Classify("/v1beta/models/oa%2Fm1:generateContent") != "gemini" || Classify("/v1/chat%2Fcompletions") != "agents" {
		t.Fatal("classes")
	}
}
```

  The existing fixture check (every canonical path's WHATWG pathname is the path itself) must still pass on the new
  cases; it is the proof that `%XX` is kept by WHATWG.

- [ ] **Step 2: Implement `Canonical`.** Accept `%` followed by two hex digits (skip the two); refuse any other `%`;
  a segment is a dot segment when, after replacing each `%2e`/`%2E` by `.`, it is `.` or `..`.

```go
func Canonical(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~!$&'()*+,;=:@/", c) >= 0:
		case c == '%' && i+2 < len(p) && isHex(p[i+1]) && isHex(p[i+2]):
			i += 2
		default:
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if d := strings.NewReplacer("%2e", ".", "%2E", ".").Replace(seg); d == "." || d == ".." {
			return false
		}
	}
	return true
}
```

- [ ] **Step 3: Front classes.** In `front.go`: `GoClasses` holds every name of `engine.Subs` (import the table from
  `gw/engine`, or keep a copy with a test that compares them; the front must not import the engine if it does not
  today — check `go list -deps` and choose the copy plus test if it would create a cycle); `chatToEngine` becomes
  `toEngine(class)` over `Subs[class]`; `ServeHTTP` calls `Serve`/`Shadow` for any class with sub-classes. In
  `lib/front/index.js`:

```js
const SUB_CLASSES = ['chat-stream', 'chat-anthropic', 'chat-anthropic-stream', 'messages-stream', 'messages-count',
  'responses-stream', 'gemini-stream', 'gemini-count'];
const GO_CLASSES = ['chat', 'chat-stream', 'chat-anthropic', 'chat-anthropic-stream', 'messages', 'messages-stream',
  'messages-count', 'responses', 'responses-stream', 'gemini', 'gemini-stream', 'gemini-count', 'embeddings'];
```

  `test/front.test.js`: `routing({ engines: { messages: 'go' } })` puts `messages` on go behind the gate;
  `frontSection` accepts every name above and still refuses `models: 'go'`.

- [ ] **Step 4: Failing serve tests** (`serve_xlate_test.go`, `httptest` provider and client):

```go
// The Anthropic pings go out every PingEvery while the provider is quiet, between whole events.
func TestTranslatedStreamPings(t *testing.T) {
	old := PingEvery
	PingEvery = 20 * time.Millisecond
	defer func() { PingEvery = old }()
	e, up := xlateEngine(t, slowSSE(150*time.Millisecond, chatChunks("hi")...))
	body := serveText(t, e, "messages", "/v1/messages", `{"model":"oa/m1","stream":true,"max_tokens":5,"messages":[{"role":"user","content":"x"}]}`)
	if strings.Count(body, "event: ping\n") < 2 || !strings.HasPrefix(body, "event: message_start\n") {
		t.Fatalf("%q", body)
	}
	if lines := up.usageLines(); len(lines) != 1 || lines[0].Status != 200 {
		t.Fatalf("%+v", lines)
	}
}

// One usage line however a translated stream ends: provider end, provider cut, client gone, write error.
func TestTranslatedStreamOneLine(t *testing.T) { /* table over the four ends, as TestStreamWriteErrorAndPanicWriteOneLine */ }

// Embeddings: the client leaving does not cancel the call; the line is written (J8).
func TestEmbeddingsLineWhenClientLeaves(t *testing.T) { /* client cancels after sending; the provider answers 200 later; one line */ }

// The engine serves each class only on its sub-class's mode; a hand-off is forwarded unchanged.
func TestServeDispatchByClass(t *testing.T) { /* modes: messages on go, messages-stream on js → a stream is forwarded */ }
```

  Write the bodies of the last three in full following `serve_stream_test.go`'s helpers (copy the helper names that
  exist there; add `xlateEngine`, `serveText`, `slowSSE`, `chatChunks` to `serve_xlate_test.go`).

- [ ] **Step 5: Implement `dispatch.go` and `serve_xlate.go`.** `Serve(class, …)`: `s == nil` or no sub-classes for
  the class → forward. `p := s.PlanFor(class, req, now)`, `sub := SubClass(class, &p, req)`, then exactly today's
  sequence for every class (hand-off on go → forward; not on go → shadow or forward; `Take` → `RateLimited(key, wait,
  p.Shape)`; `p.Upstream == UpNone` → write `*p.Self` (no line, no budget); `Budget` (skipped for `UpCount`) with the
  402 in `p.Shape`; `Guard`; then by `p.Upstream`:
  - `UpChat`, `UpAnthropic` with `p.Format == "openai"`: today's `Serve` body (`serveChat`), the Anthropic leg's
    whole answer through `RespondChatAnthropic`, its stream through `AnthropicToChatText` + `ChatStream`;
  - translated whole: `Upstream.Call` → `RespondTranslated` or the shaped 502 with the 599 line;
  - translated stream: `serveTranslatedStream` (P4c's `serveStream` structure: `track`, headers `text/event-stream`,
    `no-store`, CORS, `X-Zp-Dropped`; a non-2xx or a body-less 2xx → `RespondTranslated` whole; `Open()` written and
    flushed; a goroutine writes `Ping()` every `PingEvery` under the write mutex while `Run` reads; `Run` with a
    `write` that takes the mutex, writes, flushes; the provider body wrapped so a non-EOF read error is `ErrCut`;
    `defer` emits `Line(elapsed())` once);
  - native whole / stream: `RespondNative` / `serveNativeStream` (same skeleton, no pings);
  - `UpCount`: `Call` → `RespondCount`; a failed call → `*p.Self`;
  - `UpEmbeddings`: `Call` with `context.WithoutCancel` → `RespondEmbeddings`; failure → shaped 502, no line.
  Every emitted line goes through `emitLine` (P4f: with the journal's call id from `e.open(&p)` before the provider
  call) and every answer through P4f's answer/span helper when merged. `serve.go` changes only `Serve`'s first lines
  to call `e.serveClass(class, …)` in `dispatch.go`, so the P4f hunks there stay small.
- [ ] **Step 6: Run** `go -C gw test -race ./...`, `GOOS=windows go -C gw vet ./...`, `node --test test/front.test.js`.
  Expected: PASS.
- [ ] **Step 7: Commit** "Route the translated classes to the go engine and serve them, with Anthropic pings and one usage line per stream" and push.

### Task 9: The contract suite in go and shadow modes **(re-check after P4f merges)**

**Files:**
- Modify: `test/contract/harness.js` (`ZP_ENGINE=go` and `shadow` list the eleven sub-classes),
  `test/contract/formats.test.js`, `test/contract/guardrails.test.js`, `test/contract/budgets.test.js`,
  `test/contract/upstream.js` (only if a case needs a provider shape it lacks)

**Interfaces:**
- Consumes: the harness's `entries` helper that asserts `ipc` on go-served lines (P4b/P4e).
- Produces: under `ZP_ENGINE=go`, every format case that the engine should serve asserts its usage line carries `ipc`
  (served by go); every hand-off case (routes, previous_response_id 400, the model cache, `GET /v1beta/models`)
  asserts it does not.

- [ ] **Step 1:** In `harness.js`, the `go` config becomes `gateway: { engine: 'front', engines: Object.fromEntries(GO.map((c) => [c, 'go'])) }`
  and `shadow` `gateway: { engine: 'front', shadow: GO }`, with `GO` the `GO_CLASSES` list imported from
  `lib/front/index.js` (export it there if it is not).
- [ ] **Step 2:** In `formats.test.js`, add `served: true` to each case of the paths of this slice whose model is not a
  route and needs no model cache, and `served: false` to the others; the runner checks `entries` per the flag when
  `ZP_ENGINE === 'go'`. Same in `guardrails.test.js` (the block shapes of `/v1/messages`, `/v1/responses`, Gemini,
  embeddings) and `budgets.test.js` (the 402 shapes).
- [ ] **Step 3: Run**, on Node and Deno, the four modes:

```bash
for m in js front go shadow; do ZP_ENGINE=$m node --test test/contract/; done
for m in js go shadow; do ZP_ENGINE=$m deno test -A --no-check test/contract/; done
```

  Expected: green in every mode; in shadow mode every server stops with no diff line (the harness's existing check);
  `ZP_SHADOW_TALLY=<file>` (P4e) shows `same` lines for each of the eleven sub-classes that has a case.
- [ ] **Step 4:** Add the go and shadow steps for the new classes to CI's `contract-go-engine` job only if they are not
  already covered by `ZP_ENGINE=go|shadow` (they are, through the harness): no workflow change expected; confirm by
  reading `.github/workflows/ci.yml`.
- [ ] **Step 5: Commit** "Run the format, guardrail and budget contract cases of the translated classes on the go engine and in the shadow" and push; dispatch `ci.yml` on the branch and watch it.

### Task 10: Bench knob, results and the program plan

**Files:**
- Modify: `bench/run.js` and `bench/mock-provider.js` (add `ZP_BENCH_FORMAT=anthropic|openai`, default
  `openai`: the request path and body become `/v1/messages` with an Anthropic body, the mock answers OpenAI chunks),
  `docs/migration/2026-11-10-deno-go-migration-plan.md` (§4 owed line, §5 row S5)
- Create: `docs/migration/2026-11-17-p4d-results.md`

- [ ] **Step 1:** Add the knob and run it once **locally at small scale only** (`ZP_BENCH_STREAMS=20`) to prove it
  works; no M3/M4 numbers are recorded from it (owner deferral, Decision 24).
- [ ] **Step 2:** Write the results doc in the P4e layout: what ships (the eleven sub-classes, defaults js), evidence
  (fixture counts, shadow tally per sub-class, contract runs, CI runs), known differences (Decision 26 with any found
  while building), JS quirks copied (Decision 27), hand-offs kept with counts from the shadow tally of one contract
  run, plan deviations, and "Owed at the final quiet-machine step": M3 for `messages` whole and M4 for
  `messages-stream` at 500 streams with `ZP_BENCH_FORMAT=anthropic`, external tier, default guardrail policy, next to
  the chat items P4e listed.
- [ ] **Step 3:** Program plan: the S5 row gets "**met in P4d** (date, results doc) for messages, responses, Gemini,
  embeddings and chat to Anthropic providers; routes, the model cache and the token saver stay on js (owner questions)";
  the §4 "Owed" line adds the translated scenario.
- [ ] **Step 4: Commit** "P4d results: translated classes on the go engine, the hand-offs kept and the benchmarks owed" and push.

## Order and parallelism

Tasks run in order 1 → 10. Tasks 5 and 6 depend on 3 (planners, shapes) but not on each other and may run in
parallel worktrees if the coordinator wants. Task 7's JS part (Steps 1–4) depends on nothing and may start after
Task 1. Tasks 8 and 9 are expected after P4f merges; if P4f has not merged when Task 7 ends, Task 8 builds on today's
`serve.go` and is re-checked after the merge, together with a regeneration of `gw/testdata/xlate` (P4f's
`configPush` adds a provider `span`, which the fixtures embed).

## Plan deviations

Recorded while building Tasks 1–7 (2026-11-17). Tasks 8–10 wait for P4f to merge (coordinator).

1. **One answer path: `State.Deliver(p, ProviderResult, elapsed, ClientWriter)`** (`gw/engine/deliver.go`), not one
   `Respond*` per kind called by the serve loop. The fixtures replay it with a recording writer and the shadow with a
   buffer; Task 8's HTTP writer (flush per write, the ping goroutine behind `Pinger`) is the third user. Evidence: the
   same function proves the bytes in `TestXlateFixtures`, `TestTranslatedStreamSplits` and
   `TestShadowTranslatedFixtures`, so serving cannot drift from what the fixtures prove.
2. **The refusal shapes landed in Task 2**, not Task 3: `RateLimited`, `quota`, `refusal` and `Failed` take the
   plan's `Shape`, because the chat-anthropic plan already needed them. The OpenAI shape gives the bytes it gave
   (`TestChatFixtures`, `TestChatGuardFixtures`, `TestRateLimitedAnswer` unchanged). `Failed` also drops
   `requested` and `savedTokens` from its 599 line, as proxyChat's line has neither.
3. **A plan's provider-body builder takes the plan** (`build func(p *Plan, body *Value) []byte`): a closure over a
   local plan would have written `Dropped` into a copy (plans are returned by value).
4. **`[DONE]` only after a clean end** in `AnthropicToChatText`: callChat enqueues it inside the `try`, so a provider
   cut or a conversion that throws ends the synthesized stream without it (fixture `chat-an-stream-cut`). The plan's
   sketch wrote it always.
5. **The Anthropic leg's id is drawn before the client's stream writer**: `AnthropicToChatText` creates its converter
   synchronously and the stream path builds it before `NewTranslatedStream`, as callChat returns before translated()
   calls `out.stream()` (fixture `resp-an-stream`; with the other order the ids swap).
6. **After an error event a translated stream's line has no time to the first token and no tokens**: translated()
   keeps its first `result` when `pumpChunks` or the whole-JSON branch throws (fixtures `msg-stream-error-chunk`,
   `msg-stream-cut`; `TestTranslatedStreamErrorLine`). The native passthroughs keep what they saw (their catch is
   inside).
7. **`gw/translate.ReadSSE` reads 4 KiB at a time** (was 32 KiB): a held translated stream kept 36 KB of heap, past the
   13 KiB the 80 KiB budget leaves next to P4c's 67 KiB; now 7 KB (`TestMemoryPerHeldTranslatedStream`, heap only;
   the whole held stream through HTTP is measured in Task 8). The events do not depend on the split (stream B's
   fixtures and `TestTranslatedStreamSplits` unchanged).
8. **More fixtures than listed**: 92 cases, including `msg-native-throw` (no line, the Anthropic 502),
   `resp-native-throw` (a 599 line that keeps `requested` and `routedTo`) and `msg-native-stream-cut`. There is no
   `out.whole` TypeError case: `openAIToAnthropic` answers `{"choices":null}` without throwing; the 502 branch stays,
   untested by a fixture.
9. **`hasToolResultBlock` also hands off a `null` message or content block**: `saveTokens(body, 'anthropic')` throws a
   TypeError there and JS answers 500; the engine leaves it to JS.
10. **The capture keeps `client.body` `null` when nothing was written** (`clientBody ?? (text || null)`), so P4e's
    refusal capture reads as before; `test/front-shadow.test.js` asserts the new fields (`upstream`, `tape`,
    `client.headers`, `client.truncated`). **(re-check after P4f merges: P4f edits that test file too.)**
11. **`Capture.Client` is a named type (`CaptureClient`)**, so tests build it without repeating the struct.
12. **The fixture harness classifies `/v1beta/*` as `gemini` itself** (`classOfPath`) until Task 8 lets the front
    classify `%XX` paths (Decision 23).
13. **The Go shadow test builds the tape from the engine's own counter draws** (`xlateCapture`); the JS tape itself is
    proven by `test/shadow-capture.test.js` (every `msg_` id of the client text is on the tape; an unshadowed request
    draws exactly once). A one-byte change, a short tape and a truncated capture are each proven to compare as
    `diff` or `skipped`; an inserted ping compares `same`.
14. **A gofmt-only commit** (`5608b86`) follows Task 3's: the formatter was run after the commit.

### After P4f merged (2026-11-17, `636944a`, merged into this branch as `39a7ad7`)

Re-check of the steps marked "re-check after P4f merges":

- `gw/engine/serve.go`: one conflict (the 429): P4f's `trace`/`spans`/`onTake` kept, the refusal shaped by the plan
  (`RateLimited(key, wait, p.Shape)`). P4f's journal interface is `Calls{Open, End}` (not `NoteClose`), the emit with
  a call id is `emitLine(rec, unchecked, call)`; there is no single `finish` helper: chat calls `e.spans(s, tr, status,
  started, ended, rec, provSpan(p), obs)` after `flushAnswer`. The P4d serve path uses exactly these.
- `gw/engine/chat.go` (`State.spans`): merged without conflict.
- `lib/gateway/shadow.js`: conflict with P4f's final review m3 (a refusal's capture is stored at `res.end`, before the
  handler settles). Merged: the P4d capture fields go into P4f's `unanswered()` store, and the client writer is
  wrapped after P4f's `end` wrapper so the last bytes are kept before a refusal's capture is stored.
- `test/front-shadow.test.js`: P4f's new m3 test asserts the capture shape; it now lists the P4d fields.
- `gw/testdata/xlate`: regenerated (P4f's provider `span` in every pushed config: 92 files, the same bytes on Node and
  Deno).
- `lib/front/index.js`: P4f changed the OTLP note only (`captureContent`); the class lists merge cleanly.
- `test/contract/harness.js`: see Task 9.

### Task 8

15. **An IPC enum grew (Decision 22 corrected):** `config.push` `front.shadow.classes` and `gw/ipc` `RouteClass` list
    route classes and sub-classes, so the eleven sub-classes were added (additive; `ipc/v1` keeps 17 schema files; new
    fixture `valid-front-shadow-p4d-sub-classes.json`). `front.engines` keys were already free.
16. **The front reads its sub-class table from `front.Subs`**, a copy of `engine.Subs` (the engine imports the front, so
    not the other way round); `TestFrontSubsMatchTheEngine` keeps them equal. `chatToEngine` became `toEngine(class)`.
17. **`/v1/chat%2Fcompletions` now classifies as `agents`** (it was `""`): its WHATWG pathname keeps the escape, so it is
    not the chat route on either side; `gw/front/front_test.go` updated. Route fixtures: 41 (7 new), renumbered.
18. **Serving: one function, `serveDeliver`** (`gw/engine/serve_xlate.go`), after `Serve`'s shared rate limit, budget
    and guardrail steps; an `httpWriter` holds a whole answer until its usage line is out (as chat emits before it
    writes) and streams flush per write, pings under the same lock, every byte fed to P4f's observer.
19. **The journal opens every P4d call that writes a usage line**; count_tokens writes none in JS, so a count call is
    not journalled (a crash would otherwise recover a line JS never writes). A call that ends with no line (the native
    Anthropic failure) is `End`ed.
20. **A panic inside `Deliver`** ends the call through the same `finish` (journal `End` when no line was produced, the
    server span), then the panic goes on to net/http. Unlike P4c's chat stream, a panicking translated stream writes no
    usage line (its state is inside `Deliver`); JS has no such path.
21. **The held translated stream through HTTP was not measured separately**: Task 4's heap test (7–9 KB of translated
    state) stands, plus one ping goroutine (~8 KB of stack) for each held Anthropic-format stream. Owed with M4 at the
    final quiet-machine step.
22. **TDD order in Task 8:** the serve tests were written after the serving code (the code came from the merge
    re-check); each was then run under `-race -count=3`.

### Task 9

23. **A contract file of its own, `test/contract/go-formats.test.js`**, instead of a `served` flag on every case of
    `formats.test.js`, `guardrails.test.js` and `budgets.test.js`: thirteen calls covering every new sub-class, each
    asserting under `ZP_ENGINE=go` that its line came from the gateway (`ipc`), a route call that it did not, and
    count_tokens that no line is written. The existing format, guardrail and budget cases run unchanged in go and
    shadow mode with every go class on (`GO_CLASSES` from `lib/front`), which proves their answers equal.
24. **`go-ratelimit.test.js`'s server-side call is now `GET /v1/models`**: its embeddings call is the go engine's since
    P4d, and the test needs one request each engine answers.
25. **`shadow.noteAnswer` keeps the head's headers too** (the P4c chat capture): the chat-anthropic compare reads the
    content type, which the explicit chat capture did not have (7 diffs in the first shadow run, 0 after).


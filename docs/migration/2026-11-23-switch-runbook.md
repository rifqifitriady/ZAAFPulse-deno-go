# Switch runbook: moving the live gateway from js to go, class by class

Date: 2026-11-23. For the owner. It assumes v2 is installed on the live machine (`zaafpulse doctor` shows the gateway
line `found, allowed, <version>`), and it changes `config.json` only. Every setting, command, file and field named
here was checked against the code at `282abfc` (`migrate/deno-go`, P6 merged); what could not be checked is listed at
the end.

Nothing here moves on its own. Every engine switch defaults to `js` and every egress class to `direct`; a v2 install
behaves as v1.1 until the steps below are taken. The egress allow-list enforces and the lock-down is asked for by
default (since v2.0.0), but neither acts while every class is `direct`: no call passes the allow-list, and the
lock-down's conditions (section 6) do not hold.

## 1. The switches

There are two sets, both in `config.json` in the data folder (`zaafpulse doctor` prints it as "Data folder"). There is
no settings page for either: they are hand edits, and hand edits are not in the audit log.

### Gateway route classes (`gateway.engines`)

Each class is in one of three states. "Shadow" is not a value of `gateway.engines`; it is the class's name in the
`gateway.shadow` list while its engine is `js`.

| State | `config.json` | What happens |
|---|---|---|
| js | `"engines": { "<class>": "js" }` or absent | the server answers, as in v1.1 |
| shadow | `"engines": { "<class>": "js" }`, `"shadow": [..., "<class>"]` | the server answers; the gateway builds its own answer from the same provider answer and compares (no second provider call) |
| go | `"engines": { "<class>": "go" }` | the gateway answers itself, once the switch rule is met; until then the class stays on js with a note |

| Class | Calls it covers |
|---|---|
| `chat` | `POST /v1/chat/completions`, whole, to an OpenAI-format provider |
| `chat-stream` | the same with `"stream": true` |
| `chat-anthropic` | `/v1/chat/completions` to an Anthropic-format provider, whole |
| `chat-anthropic-stream` | the same, streamed |
| `messages` | `POST /v1/messages`, whole |
| `messages-stream` | `/v1/messages`, streamed |
| `messages-count` | `/v1/messages/count_tokens` |
| `responses` | `POST /v1/responses` (native passthrough and translated), whole |
| `responses-stream` | `/v1/responses`, streamed |
| `gemini` | Gemini `generateContent` |
| `gemini-stream` | Gemini `streamGenerateContent` |
| `gemini-count` | Gemini `countTokens` |
| `embeddings` | `POST /v1/embeddings` |
| `mcp` | `/mcp` `tools/call` only (`initialize`, `tools/list` and the rest stay on js); the tool still runs in the server |
| `token-saver` | not a path: a call whose tool results the token saver shortens runs on go only when its class **and** `token-saver` are on go |
| `provider-auth` | not a path: a call to an OAuth2 `client_credentials` or Google service-account provider runs on go only when its class **and** `provider-auth` are on go (refresh-token providers always stay on js) |
| `routes` | not a path: a `router/<name>` call runs on go only when its class **and** `routes` are on go |

`models` (`GET /v1/models`), `a2a`, `skills` and `agents` have no Go engine and stay on js whatever is set.

Prerequisite for all of them: `"gateway": { "engine": "front" }`. That value is read when ZAAFPulse starts.

### Egress caller classes (`egress.classes`)

`direct` (the call leaves from the server, as in v1.1), `check` (it still leaves directly, and the gateway records what
its allow-list would have said), `go` (it leaves through the gateway's egress endpoint, once the class's gate is met;
until then it runs as `check`). The twelve classes: `gateway`, `connectors`, `mcp`, `routers`, `pricing`, `evals`,
`agents`, `notify`, `brain`, `public`, `otel`, `runtimes` (what each covers: [docs/governance.md, Network egress
settings](../governance.md#network-egress-settings)). Prerequisite: front mode. Related: `egress.allowList`
(`enforce`, the default, | `report`) and `egress.lockdown` (`true`, the default, | `false`).

## 2. The rule for moving a class

The owner's rule of 2026-11-12, as the code applies it:

- **Gateway classes** (`lib/front/shadow-log.js` `gate`): since the class's last diff, at least
  `gateway.shadowMinCompares` compares (default 20) and `gateway.shadowDays` days (default 1) with no diff, counted from
  the last diff or, when there was none, from the first compare. A hand-off or a skipped call is not a compare.
- **Egress classes** (`lib/egress-switch.js`): the same with `egress.gateMinChecks` (20) and `egress.gateDays` (1). A
  `would-refuse` verdict or a check the client could not ask is a diff. Once met, a class stays on `go` across
  restarts (`.data/egress-gate.json`) until a check of its own disagrees or you set it back.

Leave the defaults as they are. Setting both values of a rule to 0 lifts it for every class at once, not for one.

A class you never use gets no compares and never meets the rule; it stays on js (or on `check`) and that is harmless.
Two consequences:

- Gate G3 asks for every gateway route on `go`. A class with no traffic on the live machine needs traffic to move
  (a few test calls through a key are enough for the count, but the day still has to pass).
- The lock-down needs **all twelve** egress classes asked for `go` and past their gate (section 6). A class with
  nothing configured to call (no remote A2A agent, no monitor webhook, no OTLP export, no remote model server, …) gets
  no checks, so it counts as met for the lock-down (and only for it; the class itself stays on `check` with its note).
  `routers`, `public` and, with price sync on, `pricing` always need their own checks: about ten days for `pricing`,
  20 URL ingests for `public`.

## 3. What to watch

| Where | What it tells you |
|---|---|
| Overview, the provider topology card's chip | `Gateway: Go (front) · ready · v<version>` in green is healthy. Amber: `· stale` (no health for over 5 s) or `· version differs`, or `restarting`. Red: `· fallback` (the gateway failed and the server holds the port itself), `· fallback, restart needed` after a crash loop. The tooltip has uptime, calls in flight, outbox depth, snapshot age, restarts and the last failure. |
| `GET /api/gateway/status` (open `http://localhost:4777/api/gateway/status` in the signed-in browser; owner only) | `engines`: what the gateway was last told. `notes`: why a class asked for go is still on js (`"12 of 20 chat calls compared without a diff since the last one"`, `"the chat shadow is clean for 0.40 of 1 days"`, `"the last chat compare differed"`). `shadow.<class>`: `compared`, `diffs`, `handoffs`, `cleanSince`, `lastDiffAt` (and `routeView` for routes). `egress.engines`, `egress.notes`, `egress.lockdown`. Also `state`, `restarts`, `lastFailure`, `healthAgeMs`, `versionMismatch`. |
| `.data/gateway-shadow.jsonl` | one line per shadowed call; `grep '"outcome":"diff"' .data/gateway-shadow.jsonl \| tail` shows the latest diffs with the field that differed (lengths and a hash prefix, no text) |
| `.data/gateway-usage.jsonl` | a line written by the Go engine carries `"ipc"`: `grep -c '"ipc"' .data/gateway-usage.jsonl` rising is the proof that a class really moved |
| `GET /api/egress` (owner) | each egress class's engine, the allow-list and its mode, the lock-down and why it is not in force, the newest records, the last 24 h per class |
| `.data/egress.jsonl` | one record per outbound call through the gateway and per `check` verdict; look for `"outcome":"would-refuse"` and `"outcome":"refused"` |
| Governance → Compliance | **Network egress is mediated and logged**: how many classes are on go, which are on check or direct, the allow-list mode, the last 24 h of records, the lock-down. **Gateway guardrails** and **Budgets that block or pause** should not change when a class moves. |
| `zaafpulse doctor` | `Gateway (zaafpulse-gw)`: found, allowed, version; `Server` answering; `Service` running; `MCP clients`: no registration needs renewing; `MCP entries`: every entry starts a program that exists |
| Governance → Audit log, Monitors | `gateway.budget_unchecked` entries and the matching alert mean the gateway passed metered calls on a stale budget snapshot (fail open, design rule 2): look at the chip's snapshot age |

## 4. Order

One step at a time. Take the next step only when the class before it has run on go for a while without a change in
the Logs page, the usage counts or the Monitors.

**Step 0. Back up and go to front mode, every class on js.**

```sh
cd "<data folder>"
cp config.json "config.json.pre-switch-$(date +%Y%m%d)"
```

Set `"gateway": { ..., "engine": "front" }` and restart (`zaafpulse service restart`). Check: the chip turns green
with the version, `zaafpulse doctor` shows the gateway found and allowed, the dashboard and the gateway answer as before
(every request still goes to the server). If the chip turns red, read its tooltip; the server already holds the port,
so nothing is down.

**Step 1. Shadow every class and check every egress class.** Both are observation only: the server still answers
every call and every outbound call still leaves directly.

```json
"gateway": {
  "engine": "front",
  "shadow": ["chat", "chat-stream", "chat-anthropic", "chat-anthropic-stream", "messages", "messages-stream",
             "messages-count", "responses", "responses-stream", "gemini", "gemini-stream", "gemini-count",
             "embeddings", "mcp", "token-saver", "provider-auth", "routes"]
},
"egress": {
  "classes": { "gateway": "check", "connectors": "check", "mcp": "check", "routers": "check", "pricing": "check",
               "evals": "check", "agents": "check", "notify": "check", "brain": "check", "public": "check",
               "otel": "check", "runtimes": "check" }
}
```

Restart after this edit: the shadow list reaches the gateway with the next config push, which a restart always makes
(an engine change is picked up within a minute without one, the shadow list is not). Leave `otel.captureContent` off:
while it is on no class can move.

**Step 2. Gateway classes, in this order.** For each: wait until `GET /api/gateway/status` shows no note for the class
once you ask for go (or, before asking, `shadow.<class>.compared` ≥ 20 since `cleanSince`, `diffs` not rising, and
`cleanSince` more than a day ago); then set `"engines": { ..., "<class>": "go" }`. No restart is needed: the routing is
looked at once a minute. Then watch the `"ipc"` count rise and the chip stay green.

1. `chat`: the most used and the easiest to see on the Logs page.
2. `chat-stream`.
3. `embeddings`, `messages-count`, `gemini-count`: no tokens spent on a count, little risk.
4. `chat-anthropic`, `chat-anthropic-stream`.
5. `messages`, `messages-stream`: Claude Code through the gateway.
6. `responses`, `responses-stream`: Codex through the gateway.
7. `gemini`, `gemini-stream`.
8. `mcp`: an AI client's tool calls over `/mcp`. Check one tool call from Claude Code (P4g live check 5).
9. `token-saver`: after the classes it rides on (chat, messages, responses, gemini). Its record counts only compares in
   which the saver shortened something.
10. `provider-auth`: only if you have an OAuth2 or service-account provider, and after its classes. Do the live checks
    1 to 4 of `docs/migration/2026-11-21-p4g-results.md` first (they were never run with real keys).
11. `routes`: last. It acts only on calls whose class is already on go, and it shares state (rotation counters,
    cooldowns, quota readings) with the server, which is replicated both ways whatever the switch says.

**Step 3. Egress classes, in this order.** For each: wait until `GET /api/gateway/status` → `egress.notes` has no
entry for it once asked (or the class has 20 `checked` records and no `would-refuse` over a day in `.data/egress.jsonl`),
then set `"egress": { "classes": { ..., "<class>": "go" } }`. It is picked up within a minute.

1. `connectors`, `mcp`, `routers`: the Integrations page shows each one's health at once.
2. `pricing`: two calls a day, so 20 checks take about ten days; **Refresh now** under **Settings → Model prices**, and
   each restart, add a check.
3. `evals`, `notify`, `otel`, `brain`, `agents`, `runtimes`, `public`: as far as you use them.
4. `gateway`: last. These are the server's own provider calls and token fetches, which are few once the route classes
   are on go.

**Step 4. The allow-list.** It enforces by default: from step 3 on, a call of a class on go to a host off the list is
refused with `EGRESS_REFUSED` and recorded `refused` (an `egress.refused` entry in the audit chain). The gate of step 3
already asks for checks without a `would-refuse`, so a class meets it only when its hosts are on the list. To watch
before refusing, set `"egress": { "allowList": "report" }` first and remove it when `GET /api/egress` shows no
`would-refuse` in the last days. Picked up at the next push; restart to be sure. What the list cannot know (a redirect
to another host, an intranet page ingested with `ZAAFPULSE_ALLOW_PRIVATE_URLS=1`):
[docs/governance.md, Network egress settings](../governance.md#network-egress-settings).

**Step 5. The lock-down.** See section 6.

## 5. Rolling back

**One gateway class.** Set `"engines": { "<class>": "js" }` (or remove the entry). Within a minute the server answers
that class again. Put it back in `gateway.shadow` and restart to compare again. Its shadow record still holds the
clean run it had before, so setting it to go again moves it at once; there is no command that resets a record.

**One egress class.** Set `"classes": { "<class>": "check" }` (or `"direct"`). The client follows within a minute and
the class's sticky pass in `.data/egress-gate.json` is dropped, so a later `go` has to meet the gate again.

**Everything, in one command.** Front mode off and the lock-down off; the server then answers every call and makes
every outbound call itself, as v1.1 did. The other switches stay in the file for later.

```sh
D="<data folder>"; ( umask 077; cp "$D/config.json" "$D/config.json.pre-rollback-$(date +%Y%m%d-%H%M%S)" && jq '.gateway.engine = "js" | .egress.lockdown = false' "$D/config.json" > "$D/config.json.tmp" && mv "$D/config.json.tmp" "$D/config.json" ) && zaafpulse service restart
```

Without `jq`, make the same two edits by hand and run `zaafpulse service restart`. If the service was installed from
source with `deno run` while the lock-down was in force, run `zaafpulse service install` instead of the restart (from
a shell that has your `CLAUDE_CONFIG_DIR`, `CODEX_HOME` and the other variables exported): its launch line holds the
narrowed net grant until it is written again. The Compliance control's evidence says so while that is the case.

When going back to front mode later, set any class you want proven again to `js` first: the shadow records survive.

**v2 itself.** `install.sh --rollback` (or `install.ps1 -Rollback`) puts the install the upgrade replaced back in
place, with the same data folder ([docs/install.md, Rolling back](../install.md#rolling-back)).

## 6. Before the lock-down can apply

`egress.lockdown: true` is in force only when all of these hold (`lib/permissions.js` `lockdownState`):

- `ZAAFPULSE_EGRESS_LOCKDOWN` is not `0`;
- `gateway.engine` is `"front"`;
- all twelve egress classes are set to `go` and each has met its gate (`.data/egress-gate.json` has a `passedAt` for
  each), unless both `egress.gateDays` and `egress.gateMinChecks` are 0; a class with nothing configured to call counts
  as met (`GET /api/egress` names it under `lockdown.quiet`), and loses that as soon as it gets a destination;
- the listen host (`ZAAFPULSE_HOST` or `HOST`) is an address, not a name.

The gateway route classes do **not** have to be on go for the lock-down; with the `gateway` egress class on go, the
server's own provider calls leave through the gateway too.

What it does depends on how ZAAFPulse was started:

- **The installed single-file binary** (what `install.sh` and `install.ps1` put in place): the egress client refuses
  to send any non-loopback call directly; with no ready gateway such a call fails with `EGRESS_UNAVAILABLE`. Deno's net
  grant is the one built into the binary and is not narrowed, so the Compliance control stays `partial` ("enforced by
  the egress client; Deno's grant is not narrowed in the single-file build").
- **A `deno run` launch** (a service installed from source): the launch line gets
  `--allow-net=127.0.0.1,localhost,[::1]` (plus the listen address when it is not loopback). It is computed when the
  server is launched; for a service, when `zaafpulse service install` runs, so run it again after turning the
  lock-down on. Only this launch can show the control `in place`, and only with `allowList: "enforce"`.

The way out is in section 5. `ZAAFPULSE_EGRESS_LOCKDOWN=0` turns it off for a launch from a shell, and for a service
when given to `zaafpulse service install`, which writes it into the service file (the installed binary included); it
stays there until the service is installed again without it.

Owner decision of 2026-11-23: since v2.0.0 `egress.allowList: "enforce"` and `egress.lockdown: true` are the
defaults (`lib/egress.js` `EGRESS_DEFAULTS`). An install with no `egress` section asks for the lock-down, but it comes
into force only when the conditions above hold; with every class `direct` by default, it does not. To keep the
behaviour before v2.0.0, write `"allowList": "report"` and `"lockdown": false`.

## 7. What was not verified

- None of these steps was run on the live dashboard or on a preview for this runbook; the order is a recommendation
  from the results documents (P4g "Suggested switch order", P4h "Switching it on", token saver "Switching it on").
- The one-line rollback was run on a copy of a `config.json` on macOS (jq 1.8.2, zsh): the backup and the new file are
  mode 0600 and only the two values change. It was not run against a live data folder or followed by a real restart,
  and not on Windows; there, make the two edits by hand and restart the service.
- "An engine change is picked up within a minute, the shadow list at the next push" is read from `server.js` (the
  minute loop compares only the engines with what was last pushed); it was not timed.
- `pricing` making two calls a day comes from the sync interval (12 h); the extra checks from a manual refresh were
  not counted.
- The P4g live provider checks (Azure OpenAI with Entra ID, Vertex AI with a service account, a refresh-token
  provider, an Anthropic-format provider behind OAuth2, `/mcp` from Claude Code) are still owed.

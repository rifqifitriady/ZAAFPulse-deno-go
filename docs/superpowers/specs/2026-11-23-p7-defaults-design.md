# P7: egress defaults and the lock-down gate for classes with nothing to send: design

Date: 2026-11-23. Repo `ZAAFPulse-deno-go` (public). Branch `migrate/p7-defaults`, cut from `migrate/p7-docs` at
`bfbe354`. One decision note: the rule the lock-down's gate condition follows for an egress class that this install
has nothing configured to call. Implemented on the same branch.

Inputs:
- Owner decision 8 (plan §11.8, 2026-11-23): at the v2.0.0 cut-over `egress.allowList: "enforce"` and
  `egress.lockdown: true` become the defaults. Made on this branch (`lib/egress.js EGRESS_DEFAULTS`).
- P5 spec Decisions 11 (the per-class gate: 1 clean day and 20 checks, only `/check` verdicts and client check
  errors count, sticky in `.data/egress-gate.json`), 13 (the allow-list, built from config) and 14 (the lock-down:
  in force only with front mode, all twelve classes asked `go` and each past its gate, and an address as the listen
  host).
- Switch runbook §2: "On a machine with no remote A2A agents, no monitor webhook, no OTLP export or no remote model
  server, those classes get no checks, so the lock-down cannot come into force under the default rule."

## The problem

With `lockdown: true` the default, the lock-down is asked for on every install, but `lib/permissions.js lockdownState`
puts it in force only when each of the twelve classes has a pass in `.data/egress-gate.json`. A pass needs 20
`checked` verdicts. A class whose callers have no destination (no remote agent, no OTLP endpoint, no vector store, no
model server on another machine) makes no call, so it never gets a check and never a pass. On a typical install at
least four classes are in that state, so the default would be asked for and almost never in force.

## Options

| | Rule | Safety | Usability |
|---|---|---|---|
| A | Keep the rule: every class needs its own evidence | Strictest | The default lock-down is in force almost nowhere; the owner has to lift the gate (`gateDays: 0`, `gateMinChecks: 0`) for every class at once, which also lifts it for the classes that do send |
| B | A class with **no configured destination** counts as clear for the lock-down's gate condition; a class with one still needs its own pass | A class with nothing configured has nothing to send, so there is nothing its evidence could prove; every class that can send keeps the full rule | In force on an install as soon as the classes it uses have passed |
| C | A class with no traffic in the last N days counts as clear | Weaker: "no traffic yet" is not "nothing to send"; a configured but idle class (a weekly webhook) would skip its gate | Better than B on installs with idle sources |
| D | Lower the gate for every class (fewer checks, shorter wait) | Weaker for every class | Still never met by a class with zero traffic |

## Decision (B)

**A class with nothing configured to call counts as clear for the lock-down's gate condition.** It must still be asked
for `go` (the owner moves every class by hand, as before), and the rest of the rule is unchanged: front mode, the
listen host, `ZAAFPULSE_EGRESS_LOCKDOWN`, the sticky pass, and only `/check` verdicts and client check errors as
evidence. The exemption is never written to `.data/egress-gate.json`: it is computed each time from the configuration,
so it ends the moment a destination of that class is configured.

"Nothing configured" is decided from presence, never from a value that could fail to resolve, and anything unsure
counts as configured (`lib/egress-allow.js quietClasses`):

| Class | Configured when |
|---|---|
| `gateway` | an enabled provider has a non-loopback base URL, or signs in with OAuth2 or a Google service account (remote token endpoint) |
| `connectors` | an enabled Dynatrace, Arize, n8n, HTTP, router or OpenRouter integration has a non-loopback URL (its defaults as the connectors apply them) |
| `mcp` | an enabled upstream MCP integration, or a Heybrain brain source, has a non-loopback URL |
| `routers` | always: the OpenRouter page calls `openrouter.ai` with no setting |
| `pricing` | price sync is on (the default; off only with `pricingSync.enabled: false`) |
| `evals` | `ZAAFPULSE_OLLAMA_URL` or `ZAAFPULSE_LMSTUDIO_URL` names a non-loopback host |
| `agents` | an enabled agent in `agents.json` has a non-loopback `remoteUrl`; also when `agents.json` could not be read |
| `notify` | a monitor webhook URL is set (a secret reference; present is enough, it is not resolved) |
| `brain` | an enabled brain source other than Heybrain is a hosted app (Notion, Roam, Mem, Reflect, Pinecone), has a non-loopback URL, or is pgvector with a stored connection secret |
| `public` | always: a user can ingest any URL at any time; its destinations are never in the configuration |
| `otel` | OTLP export is on (config or `OTEL_EXPORTER_OTLP_*`) with a non-loopback endpoint |
| `runtimes` | a model server you added has a non-loopback health URL |

A loopback destination (`localhost`, `127.0.0.1`, `[::1]`) counts as nothing to send: such calls always go direct, the
lock-down grants exactly those hosts, and the allow-list leaves them out for the same reason.

Consequences, said plainly:

- `routers` and `public` always need their own evidence, and `pricing` does while price sync is on (two calls a day:
  about ten days for 20 checks, faster with **Refresh now**). `public` needs 20 URL ingests checked clean and a day.
  These three are the floor for any install that wants the lock-down under the default gate rule.
- When a class gets its first destination while the lock-down is in force:
  - the single-file build (the lock-down is the egress client's, computed each minute and at each save) drops it at
    once and says why (`classes that have not met their gate: …`); the class then runs on `check`, collects its
    evidence and, once it passes, the lock-down comes back;
  - a `deno run` launch keeps its narrowed net grant until it is launched again (the grant is fixed at launch); every
    call still leaves through Go and the allow-list, which follows the new destination at once. The next launch is not
    narrowed until the class passes, and the status says so.
- The exemption is shown: `GET /api/egress` and the Compliance evidence name the classes counted clear because
  nothing is configured for them (`lockdown.quiet`).

Rejected: C, because a configured source that is merely idle has something to send and its evidence still matters;
D, because it weakens the gate for the classes that do send and still never moves a class with no traffic; A, because
it leaves the owner's default without effect and pushes the owner to lift the gate for every class.

## Also fixed here

- **The launch line read the gate from the wrong file.** `lib/permissions.js selfCommand` (used by
  `zaafpulse service install` and the MCP client entries under `deno run`) read `<data folder>/egress-gate.json`; the
  server writes `<data folder>/.data/egress-gate.json`. A service installed from source therefore never got the
  narrowed grant unless the gate rule was lifted. It now reads `.data/egress-gate.json`, and `agents.json` beside it.

## Tests

`test/egress-quiet.test.js`: each class's "configured" rule (loopback only is quiet, a secret reference is
configured without being resolved, an unreadable `agents.json` is configured, `routers` and `public` never quiet,
`pricing` quiet only with sync off); the lock-down in force with only the configured classes passed; a configured
class without a pass still blocks it, also when the others are quiet; the exemption ends when a destination appears;
the sticky pass and the evidence rule unchanged (`test/egress-switch.test.js` untouched); `selfCommand` reads the gate
and the agents from `.data/`.

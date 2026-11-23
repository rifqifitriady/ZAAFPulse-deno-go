# Governance

The **Governance** menu answers the questions an auditor or a security lead asks first: which AI systems are in use
here, who owns them and how risky they are; which controls are in place, with evidence; and who changed what. It has
three pages:

- **Inventory** (`#governance`): every AI system ZAAFPulse can see on this machine, with your review of each one.
- **Compliance** (`#governance/compliance`): the controls ZAAFPulse provides, their status from your settings, the
  clauses they map to, and an evidence export.
- **Audit log** (`#governance/audit`): every change made by a person or an API key ([below](#audit-log)).

Everything stays on this machine. The Compliance page is evidence for your assessment, not a certification. Other
people can read these pages with an account of their own ([docs/users.md](users.md)): a viewer reads and exports, a
reviewer also saves reviews.

## Inventory

### What is listed

The inventory is built when you open the page, and every 10 minutes in the background for the Monitors rule (the first
build waits for the first scan of the log index, so the tools and models in the logs are in it; during a later rescan
the previous scan is used), from what ZAAFPulse already reads plus
the AI clients' MCP settings:

| Kind | From | Shown in "where" as |
|---|---|---|
| `app` | the apps running and installed (as on the Live page), the indexed Claude Code and Codex logs, the Trace explorer's sources, the desktop apps under Settings → Local models | `running`, `installed`, `logs`, `trace`, `runtimes` |
| `mcp` | the MCP servers in the AI clients' config files ([below](#mcp-servers-in-your-ai-clients)), and the `mcp__<server>__…` tools in the indexed logs | `<client> config`, `logs` |
| `provider` | the gateway's providers | `gateway` |
| `model` | the models the gateway served this month (a route, `router/<name>`, is not a model) and the models in the indexed logs | `gateway usage`, `logs` |
| `agent`, `skill`, `workflow`, `knowledge` | the gateway's agents, the ZAAFPulse agent skills, the workflows and the knowledge bases | `gateway` |
| `runtime` | local model servers: Ollama, LM Studio, the llama.cpp server, Jan, LocalAI and your own | `running`, `installed`, `runtimes` |
| `integration` | the integrations in `config.json`, upstream MCP servers included | `integrations` |
| `key` | ZAAFPulse API keys: the consumers | `gateway` |

One system is one item: Claude Code running, installed, in the logs and in the Trace explorer is one `app:claude`. An
item's id is `<kind>:<name>`, lowercased, the name being the system's own id where it has one (`key:key_ab12`,
`provider:openai`). Its state is the best one a source gives: `running`, then `used` (in the last 30 days: the logs,
the gateway's usage this month, a key's last use), `configured`, then `idle` (installed but not running, switched off,
revoked or expired). The vendor is shown where it is known. An agent is read for its id, name, state and remote
address only, never its system prompt; a key for its id, name and dates, never its secret.

A source that fails, or takes longer than 6 seconds, is named on the page and the list is built from the rest. So is an
AI client file that could not be read (by the client's name; the page also shows its `~` path).

### MCP servers in your AI clients

ZAAFPulse reads the files `zaafpulse register-mcp` knows: Claude Code (`~/.claude.json`: the user's servers and each
project's), Claude Desktop, Cursor, Kiro, Antigravity, VS Code (`servers`), Windsurf / Devin Desktop (and the folder
from before the rename), Gemini CLI, Zed (`context_servers`) and Codex CLI (the `[mcp_servers.<name>]` tables of
`config.toml`). Comments and trailing commas are fine in a file of at most 2 MB; a larger file must be plain JSON. A
client file must be a regular file of at most 20 MB. A file that cannot be read or parsed shows as `unreadable`, and the
other clients are still listed. A Codex file that is ambiguous is `unreadable` too: a server written inline rather than
as a `[mcp_servers.<name>]` table, or a table with a `__proto__`, `constructor` or `prototype` key. In the JSON
clients' files an ambiguous command (a path with spaces followed by more words) gives no command; the file is still
read. The same server in several clients is one item that names every client. A
project's own `.mcp.json` or `.cursor/mcp.json` is not read.

Of a server only this is kept: its name, the clients that have it, the transport (`stdio` or `http`), the command's
base name (`npx`) and the first argument that looks like a package name (`npx -y @scope/pkg` gives `@scope/pkg`), or
for an HTTP server its URL's origin (`https://mcp.example.com`: no path, query or user). Never an env value, a header,
a flag, the argument after a flag whose name looks secret (`--token`, `--api-key`, `--password`, `--auth` …), or
anything that looks like a token, an email-like `user@host` or a UUID. Only those secret-looking flags skip the next
argument: the package is the first other argument that looks like a package name, so it can be a plain flag's value
(`--port 8080` gives `8080`). ZAAFPulse's own entry is marked and is never counted as new. A project's folder shows as a `~` path on the
dashboard only; the evidence export leaves it out. Demo mode reads no client file.

### Reviews

**Review** on a row opens a drawer: owner, purpose, risk (`minimal`, `limited`, `high` or `unacceptable`, the EU AI
Act tiers, or none), decision (`approved`, `restricted` or `blocked`, or none) and a note, at most 200 characters each.
A review is saved in `config.json` as `governance.inventory["<id>"] = { owner, purpose, risk, decision, note,
reviewedAt }`, so every save is a `config.change` entry in the audit log. An item without one is **unreviewed**. A
review whose item is no longer seen is kept and shown as **gone**; **Clear review** removes it. At most 2000 reviews.

The decision is a record: blocking a system stays with API keys and the guardrails.

Only the owner and reviewers review ([docs/users.md](users.md)); a viewer sees the reviews without the **Review**
button. The owner field suggests the display names of the owner and the enabled dashboard accounts, and any text is
still allowed.

### First and last seen

`.data/governance/seen.json` holds, for each item id, the first and the last day (UTC) the inventory saw it, and
nothing else (the state last seen and a baseline mark), plus whether the baseline is done and, while it is open,
`baselineSince`: the time the file was first written. It is written when a build
changes it, and an entry not seen for 400 days is dropped. The page never fails on it.

**Baseline.** Everything found before the first complete build is the baseline: there is no seen file yet, and the
baseline ends after a build in which no source failed, or 24 hours after the file was first written, whichever comes
first. Baseline items show as unreviewed, with today as their first seen, but are never counted as new, so installing or
upgrading does not raise an alert for everything already here. If the file is there but cannot be read, nothing is
baseline and every item counts as new: damaging or emptying the file raises the alert instead of silencing it.

When a source fails, a reviewed item that source would have found is kept as it was last seen and marked `stale`
("not confirmed this build"), not `gone`. The same goes for a reviewed MCP server while any AI client file is
`unreadable`: the server may be in that file.

### The Monitors rule

`inventory_unreviewed` (on by default, never pauses) is triggered while an item first seen in the last 7 days has no
review and is not part of the baseline: a new AI system nobody has looked at (shadow AI). Its value is how many, its
title their kinds (`mcp, provider`): the alert carries a count and kinds only, never a name. ZAAFPulse's own MCP entry
does not count, nor does a skill of ZAAFPulse's catalogue that is not installed. The rule reads the last build, so it needs no open page.

## Compliance

The page lists a fixed set of controls, each with a status worked out from your settings and state now (`in place`,
`partial`, `off` or `unknown`), its evidence (a setting's value, a count, a date) and a link to the page that shows it. The
tables are grouped by framework, clause by clause:

- **EU AI Act**, Regulation (EU) 2024/1689 as amended by Regulation (EU) 2026/1744: Art. 12 (record-keeping), Art. 14
  (human oversight), Art. 26 (deployer obligations: logs kept at least 6 months, oversight assigned, monitoring), Art.
  26(2) (human oversight assigned to competent persons) and Art. 50 (transparency, where your use applies).
- **NIST AI RMF 1.0**: GOVERN 1.4, 1.6 and 2.1, MAP 1.1, MEASURE 2.1, 2.3, 2.4, 2.7 and 2.10, MANAGE 2.4 and 4.1.
- **ISO/IEC 42001:2023**, Annex A: A.2, A.3, A.4, A.5, A.6, A.7, A.8, A.9 and A.10.

It is evidence for your assessment, not a certification: a status says what ZAAFPulse does here, not whether your use
of AI meets a law or a standard.

### The controls

| Control | In place when | Partial when | Off when |
|---|---|---|---|
| Gateway guardrails (A.9) | secrets and personal data are `redact` or `block` and prompt injection is `block` | secrets are `redact` or `block` but another check only logs or is off, or an active key sets a check weaker than the global one, or the keys could not be read | secrets are only logged or not checked |
| Governance audit log | entries are written and the last verify found the chain intact (kept at least 6 months) | not verified yet | writing fails, or the chain is broken |
| Gateway request log | always: every gateway request is a line, never its text | the log reached its size cap and rotated, and reaches back under 183 days | |
| External providers need approval (A.2) | every enabled external provider is approved or waits for approval | one is served without a recorded approval (set up before approvals existed) | |
| API keys with scopes, budgets and expiry | every active key has a budget or an expiry (or there is no active key) | some do | none does |
| Human approval in workflows | every workflow that sends, changes or decides (tier R2 or R3) has an approval step (or there is none) | some do | none does |
| Pause and stop an agent | agent control is on | it is on, but no session can be linked to its process: on Linux under Deno, which cannot read `/proc`, with no `ps` or `lsof` installed | it is off: server mode, password or no sign-in, Docker |
| Monitors rules | `guardrail_block`, `audit_chain_broken` and `inventory_unreviewed` are on | one or two of them are off | all three are off |
| Evals running | a judge model is ready, judge evaluators are on and the judge is not paused | the judge is off, waiting for a model or paused by its daily or cost cap (the code evaluators always run) | |
| Dataset experiments | an experiment finished in the last 90 days | datasets, but no experiment finished in the last 90 days | no dataset |
| Budgets that block or pause | a budget blocks, or pauses while agent control is on | budgets only alert (a pause budget counts for nothing while agent control is off or could not be read) | no budget |
| Secrets in the OS store (A.4) | the Keychain or the keyring | the encrypted secret file | |
| Dashboard sign-in | this OS account (local) or a password | | no sign-in |
| Dashboard roles and access reviews (Art. 26(2), GOVERN 2.1, A.3) | sign-in is required and no account that may sign in has gone unused for over 90 days (or there is none) | one has: an access review is due (the count, never a name) | no sign-in |
| Content capture off | the OpenTelemetry export sends no prompt or reply | | it does (`otel.captureContent`) |
| OpenTelemetry export | spans are exported | the exporter reports failed exports since start | not set up |
| Trace retention | traces are kept at least 183 days (6 months) | they are kept less | tracing is off |
| AI inventory reviewed | every item is reviewed, every high or unacceptable one has a decision and every unacceptable one is blocked, every inventory source was read and every AI client file could be read | some are reviewed, or a source or an AI client file could not be read ("N AI client files could not be read") | none is |
| Runtime permissions (MEASURE 2.7, A.6) | ZAAFPulse runs on Deno, every permission (read, write, net, env, run, sys) is an allow-list and run holds no interpreter or launcher (never the case today) | it runs on Deno and a permission is unrestricted or all but denied entries (net's denied hosts; write in the single-file build, which Deno reports as partial), run includes an interpreter or launcher (node, osascript, powershell, cmd, open, brew, claude, codex …), or (Linux) agent control is lost because `/proc` is unreadable and `ps` or `lsof` is missing | it runs on Node.js: no runtime permission sandbox |
| Network egress is mediated and logged (MEASURE 2.7, A.6, Art. 12) | every outbound caller class is on `go` (its calls leave through the Go gateway's egress endpoint, each one recorded), the allow-list enforces, and the server's Deno net grant is narrowed to loopback | the gateway runs in front mode but a class is still `direct` or `check`, the allow-list only reports, or the lock-down is not in force (the evidence says why) | no gateway runs (js mode): every outbound call leaves directly |

**Runtime permissions.** Under Deno ZAAFPulse runs with the permission set of its entry point in `deno.json`
(`server`, `cli`, `mcp-stdio`): read all files, write only the data folder, the temp folder, the skill folders and the
AI clients' config folders (computed at each `deno run` launch; the single-file build writes anywhere for now), every
host except the cloud metadata addresses (`169.254.169.254`, `metadata.google.internal`, `[fd00:ec2::254]`,
`169.254.170.2`, `100.100.100.200`; the gateway's egress path refuses the same), every environment
variable (so a secret reference to any variable keeps working), a fixed list of programs and of system values. The
evidence names what is unrestricted, what is all but denied and what is allow-listed; the reason for each entry is in
`lib/permissions.js`. On Linux, Deno refuses every read under `/proc` unless the process holds all permissions (no
scoped grant reaches it), so ZAAFPulse lists processes with `ps` and finds an agent's working folder with `lsof`, and
the evidence says so. Without `ps` or `lsof` (procps and lsof packages), agent control and linking a session to its
process are unavailable: this control and **Pause and stop an agent** turn `partial`, and the server log says why
once. Signals (pause, resume, stop) go through the `kill` program, since Deno allows its own only with every program
allowed. On Windows there is no `kill` program and no pause: whether a process is still alive is asked of `tasklist`
(on the run list for that), and a stop goes through `taskkill`, as under Node. The single-file build reports its
unscoped write as "all but denied entries", like net with its denied hosts.

**Network egress.** In front mode the Go gateway offers an egress endpoint on `127.0.0.1`; each outbound caller class
(`gateway`, `connectors`, `mcp`, `routers`, `pricing`, `evals`, `agents`, `notify`, `brain`, `public`, `otel`,
`runtimes`) moves to it with `config.json` `egress.classes.<class>`: `direct`, `check` (the call goes direct and the
gateway records what its allow-list would say) or `go`, after 1 clean day and 20 checks (a class that passed stays on
`go` until a check disagrees). Every call through the gateway, and every provider call the gateway makes itself, is
recorded in `.data/egress.jsonl` (time, purpose, method, host, port, status, outcome, bytes, duration; never a path, a
query or a header); a call the allow-list refuses also gets an `egress.refused` entry in the audit chain. The
allow-list is built from `config.json` (`egress.allowList`: `enforce`, the default since v2.0.0, or `report`).
`GET /api/egress` (owner) shows the classes, the allow-list, the lock-down and the last records. `egress.lockdown`
(asked by default since v2.0.0) narrows the server's Deno net grant to `127.0.0.1`, `localhost` and `[::1]` at a
`deno run` launch once every class is on `go` and the gateway runs in front mode; turn it off with `"lockdown": false`
and a restart (a service: install it again) or `ZAAFPULSE_EGRESS_LOCKDOWN=0`. The single-file build keeps the net grant it was built with and applies the lock-down
in the egress client only. Programs ZAAFPulse starts (Claude Code, Codex, model servers) are outside Deno's
permissions and outside the gateway. Every setting, the gate and the lock-down's conditions:
[Network egress settings](#network-egress-settings); the gateway's own route switches:
[Gateway engine switches](#gateway-engine-switches).

**Run is not a sandbox boundary.** The run list holds interpreters and launchers that ZAAFPulse needs: `node` (a
Claude Code installed as `cli.js`), `osascript`, `powershell`, `cmd`, `open`, `xdg-open`, `brew`, `claude`, `codex`,
`launchctl`, and for the CLI `wscript.exe`, `explorer.exe`, `schtasks`, `systemctl`. Each runs any code it is given, outside Deno's
permissions, so code that can spawn one can also write anywhere. The run and write scopes guard against accidents and
against code paths that were never meant to spawn or write, not against a determined attacker inside the process. The
evidence names the launchers the running process holds ("run includes interpreters and launchers (…): not a sandbox
boundary; the write scope guards against accidents, not a determined attacker") and the control stays `partial` while
it holds any. The reason for each is in `lib/permissions.js` `WHY.launchers`.

**Launch lines are checked.** A `deno run` launch (the service, an MCP client's entry) carries `--allow-write` and
`--allow-run` lists built from the environment (`ZAAFPULSE_DATA`, `CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME`,
`ZAAFPULSE_CLAUDE_BIN`, …) and from `config.json`. Only owner model servers approved in Settings, as their command is
now, are added to `--allow-run`. An entry holding a comma (it would split into a second grant: a data folder named
`x,/` would grant `/`), a double quote or a control character is refused before anything is written, with an error
naming the value. Other characters a name can hold (`O'Brien`, `AT&T`, `$`, `^`, `%`) are passed as they are: the launch
is an argument list, and the plist, the systemd unit, the MCP client JSON and the Codex TOML each escape them. The
Windows launcher refuses an argument with a quote or `%` (cmd.exe cannot take them safely inside quotes), and the
systemd unit writes `%` as `%%` and `$` as `$$`.

Guardrails are the strictest of these: the default configuration (secrets `redact`, personal data `off`, injection
`log`) shows `partial`, and `in place` needs personal data on and injection set to `block`.

**Unknown.** A control whose state could not be read, or whose check failed, is `unknown` with "could not be read" as
the evidence, not `off`: nothing says it is switched off. The same goes for an inventory in which every source failed
and for OpenTelemetry settings that could not be used. The page and the exports count all four statuses:
"<n> in place · <n> partial · <n> off · <n> unknown".

### Network egress settings

All in `config.json` under `egress`; there is no settings page for them. With no `egress` section every class is
`direct`, the allow-list enforces and the lock-down is asked for (the defaults since v2.0.0, owner decision of
2026-11-23). Neither changes anything on its own: with every class `direct` no call goes through the gateway, so the
allow-list has nothing to refuse, and the lock-down is not in force until its conditions below hold. Only the value
`"report"` makes the allow-list report and only the JSON value `false` turns the lock-down off; any other value is the
default. The rollback to the behaviour before v2.0.0 is `"allowList": "report"` and `"lockdown": false`. One exception
to "nothing changes": the Go engine's own provider calls and token fetches (gateway route classes on `go`) are
checked against the list whatever the class switches say, so with `enforce` a provider host that is not on the list is
refused there. Every provider's base and token URL is on the list by construction, and a provider on this machine
(`localhost`, `127.0.0.1`, `[::1]`: Ollama, LM Studio, a local proxy) is never checked.

| Setting | Values | Default |
|---|---|---|
| `egress.classes.<class>` | `direct`, `check` or `go` (anything else is `direct`) | `direct` |
| `egress.gateDays` | days without a disagreeing check before a class asked for `go` moves (0 lifts this part) | `1` |
| `egress.gateMinChecks` | checked calls since the last disagreement before it moves (0 lifts this part; both 0 lift the gate) | `20` |
| `egress.allowList` | `enforce` (a host off the list is refused, `EGRESS_REFUSED`) or `report` (it is recorded `would-refuse` and passes) | `enforce` (before v2.0.0: `report`) |
| `egress.lockdown` | `true` narrows the server's Deno net grant to loopback when the conditions below hold; `false` never | `true` (before v2.0.0: `false`) |
| `ZAAFPULSE_EGRESS_LOCKDOWN=0` | environment variable: no lock-down for this launch, whatever `config.json` says; `zaafpulse service install` carries it into the service file | not set |

The classes and what each covers: `gateway` (the server's own provider calls and token fetches), `connectors`
(Dynatrace, Arize, n8n, HTTP, the router connector, OpenRouter), `mcp` (upstream MCP servers), `routers` (OmniRoute,
the OpenRouter page), `pricing` (price sync), `evals` (the judge's model servers), `agents` (remote A2A agents),
`notify` (monitor webhooks), `brain` (second-brain apps and vector stores, Postgres through a tunnel), `public` (URL
ingest: public addresses only, checked when Go connects, never matched against the allow-list), `otel` (OTLP export)
and `runtimes` (model servers on another machine). Calls to `localhost`, `127.0.0.1` and `[::1]` always go direct.

**The gate.** A class asked for `go` runs on `check` until its gate is met: its evidence is the `check` verdicts in
`.data/egress.jsonl` (`checked` agrees, `would-refuse` disagrees) and the checks the client could not ask, which also
count as a disagreement. Once met, the class stays on `go` across restarts (`.data/egress-gate.json`) until a check of
its own disagrees or you set it to `direct` or `check`. `GET /api/gateway/status` (owner) shows each class's engine
under `egress.engines` and why one asked for `go` is still on `check` under `egress.notes`; that is the switch after its
gate, not where a call goes right now: with no ready gateway (`egress.port` `null`) every class goes direct whatever it
says. The endpoint exists only in front mode (`gateway.engine: "front"`): in js mode every class goes direct, and while
the gateway restarts a class on `go` goes direct too, unless the lock-down holds.

**The allow-list** is built from `config.json` and pushed to the gateway on every change: the providers' base and token
URLs, the integrations, the brain sources, remote agents (`agents.json`), the monitor webhook, the OTLP endpoint, the
model servers you added, `ZAAFPULSE_OLLAMA_URL` and `ZAAFPULSE_LMSTUDIO_URL`, and `openrouter.ai`. A rule names a host
and an exact port. There is no way to add a host by hand: a host belongs on the list because something in the settings
calls it. The cloud metadata addresses and names (`169.254.169.254`, `metadata.google.internal` and the others listed
under **Runtime permissions**) are refused on every path in every mode, the redirect hops included, and the allow-list
cannot open them. A redirect to another origin drops `Authorization`, `Cookie` and `Proxy-Authorization`.

The list is rebuilt and pushed when a source changes: a saved setting at once, a remote agent saved or removed at once,
and a change the server does not see being saved (an environment variable, a secret changed in the OS store) within a
minute (a resolved secret is kept for five minutes). A Google service account whose JSON names its own `token_uri`
puts that host on the list too. What an enforcing list can still refuse, because no setting names the host:

- a redirect to another host on a call that follows redirects (price sync, OTLP export, OmniRoute, remote agents,
  monitor webhooks, the evals' model servers); the connectors, upstream MCP servers, brain apps and vector stores
  follow only same-origin redirects themselves, and Roam's peer hosts (`*.roamresearch.com`, any port) and Pinecone's
  index hosts (`*.pinecone.io`) are on the list by wildcard;
- URL ingest with `ZAAFPULSE_ALLOW_PRIVATE_URLS=1` once `public` is on `go` (or the lock-down holds): that call leaves
  without public mode, so it is matched against the list, and a page on an intranet host is refused;
- a source changed without a save, in the minute before the list follows.

Each such refusal is recorded `refused` with an `egress.refused` entry in the audit chain and shows on
`GET /api/egress`; under `check` the same host shows as `would-refuse` before a class moves. None of this applies to a
class on `direct` or `check`: those calls never pass the allow-list. The Go engine's own provider calls and token
fetches (gateway route classes on `go`) are checked too, whatever the class switches say; their hosts come from the
same provider settings (disabled providers included), a loopback host is never checked, and the engine follows no
redirect. When a gateway generation cannot be built (a broken `config.json`), the gateway gets an empty list in the
default mode, so calls of a class on `go` are refused until the configuration builds again.

**The lock-down** applies only when all of these hold, else `GET /api/egress` and the control's evidence say which one
does not:

- `egress.lockdown` is `true` and `ZAAFPULSE_EGRESS_LOCKDOWN` is not `0`;
- `gateway.engine` is `"front"`;
- every one of the twelve classes is asked for `go` and has met its gate (with both gate parts at 0 no pass is needed).
  A class this install has nothing configured to call counts as met: no destination of it outside `localhost`,
  `127.0.0.1` and `[::1]` (no remote agent, no OTLP endpoint, no webhook, no vector store, …), so it has nothing to
  send. It is computed each time and never written to `.data/egress-gate.json`, so it ends when the class gets a
  destination; a secret reference or an `agents.json` that cannot be read counts as configured. `routers` (the
  OpenRouter page), `public` (URL ingest) and, while price sync is on, `pricing` always need their own pass. The
  evidence names the classes counted clear this way. The rule and the options weighed:
  [P7 defaults design](superpowers/specs/2026-11-23-p7-defaults-design.md);
- the listen host (`ZAAFPULSE_HOST` or `HOST`, default `127.0.0.1`) is an address, not a name.

A `deno run` launch of the server then gets `--allow-net=127.0.0.1,localhost,[::1]` (plus the listen address when it
is not loopback); the line is computed when the server is launched, and for a service when it is installed. While no
gateway is ready a locked client fails with `EGRESS_UNAVAILABLE` and never goes direct. The single-file build (what the
installers put in place) keeps the net grant it was built with: the lock-down there is the egress client's alone, so the
**Network egress** control stays `partial` with the evidence "enforced by the egress client; Deno's grant is not
narrowed in the single-file build". Only a `deno run` launch can show `in place`.

To leave the lock-down, set `"lockdown": false` and restart; a `deno run` service needs `zaafpulse service install`
again, because its launch line holds the narrowed grant (the control's evidence says so while a narrowed grant
outlives the setting). `ZAAFPULSE_EGRESS_LOCKDOWN=0` works for a launch from a shell and for a service:
`ZAAFPULSE_EGRESS_LOCKDOWN=0 zaafpulse service install` writes the variable into the launchd plist, the systemd unit or
the Windows launcher (and, from source, an unnarrowed launch line), so the running service sees it too, the installed
single-file build included. It stays there until the service is installed again without it.

### Gateway engine switches

In front mode the Go gateway can answer gateway calls itself instead of handing them to the server. Which calls is set
per route class in `config.json`. These settings decide where the checks run, not which checks run: both engines apply
the same keys, guardrails, budgets and usage lines, and the server stays the only writer of the usage log, the cost
ledger and the audit chain.

| Setting | Values | Default |
|---|---|---|
| `gateway.engine` | `"js"` (no Go process) or `"front"` (the Go gateway holds the main port); read at start | `"js"` |
| `gateway.engines.<class>` | `"js"` or `"go"` | every class `"js"` |
| `gateway.shadow` | a list of classes to compare while they are on `js` | `[]` |
| `gateway.shadowDays` | days without a diff before a class asked for `go` moves (0 lifts this part) | `1` |
| `gateway.shadowMinCompares` | compared calls since the last diff (0 lifts this part; both 0 lift the rule) | `20` |
| `gateway.memLimitMB` | the gateway's soft memory limit in MiB, 0 for none; read at start | `64` |

The classes that can run on `go`: `chat`, `chat-stream`, `chat-anthropic`, `chat-anthropic-stream` (chat calls to an
Anthropic-format provider), `messages`, `messages-stream`, `messages-count`, `responses`, `responses-stream`,
`gemini`, `gemini-stream`, `gemini-count`, `embeddings`, `mcp` (`/mcp` `tools/call` only; every other `/mcp` method
stays on js), and three switches that are not paths:

- `token-saver`: a call in which the token saver shortens a tool result runs on `go` only when its own class and
  `token-saver` are both on `go`;
- `provider-auth`: a call to an OAuth2 `client_credentials` or Google service-account provider runs on `go` only when
  its class and `provider-auth` are both on `go`; providers with a `refresh_token` grant always stay on js;
- `routes`: a call to a route (`router/<name>`) runs on `go` only when its class and `routes` are both on `go`.

`models`, `a2a`, `skills` and `agents` are route classes too, but have no Go engine: they always run on js, and a
`"go"` for them is ignored.

**The switch rule.** A class asked for `go` moves only when the shadow has, since its last diff, compared at least
`shadowMinCompares` of its calls and seen no diff for `shadowDays` days; until then it stays on js and
`GET /api/gateway/status` says why under `notes` (`"12 of 20 chat calls compared without a diff since the last one"`);
once the note is gone, `engines.<class>` there turns `"go"` at the next minute tick. A class on `go` is no longer
compared, except its calls that `token-saver`, `provider-auth` or `routes` still keep on js. While OpenTelemetry export
captures content (`otel.captureContent`), every class stays on js. The routing is looked at once a minute, so a class
whose rule is met moves within a minute.

**The shadow log**, `.data/gateway-shadow.jsonl`, holds one line per shadowed call: the class, the outcome (`same`,
`diff`, `handoff` or `skipped`) and, for a diff, the field, the lengths and a hash prefix, never prompt or answer
text. `GET /api/gateway/status` shows per class `compared`, `diffs`, `handoffs`, `cleanSince` and `lastDiffAt`. A
hand-off (the engine would have handed the call to the server) and a skipped call (the provider call failed, or a
rate limit answered) never count as compares.

Changes to these settings are hand edits of `config.json`, so they are not recorded in the audit log (see
[What is recorded](#what-is-recorded)). The owner's procedure for moving classes, and for moving them back, is in
[docs/migration/2026-11-23-switch-runbook.md](migration/2026-11-23-switch-runbook.md).

### Evidence export

**Export Markdown** and **Export JSON** download `GET /api/governance/evidence?format=md|json`
(`zaafpulse-evidence-<day>.md` or `.json`): the inventory with its reviews, the controls with status, evidence and
clauses, the audit log's retention and last verify, the time and the ZAAFPulse version, and the AI clients whose files
could not be read (`unreadableClients`, by name). It holds no path: an item's project folders and the client files are
left out. Markdown in a table cell is escaped, so a name or a note cannot make a link or an image. Review notes (owner, purpose, note) are exported verbatim, so do
not paste a secret into one. Each export is recorded as a `governance.export` entry, with the format and who asked.

### Inventory and compliance API

All behind the dashboard sign-in; no gateway key or MCP client reaches them.

| Route | What |
|---|---|
| `GET /api/governance/inventory` | `{ generatedAt, items, counts, failed, clients }`: the items, their counts (`unreadableClients` among them), the sources that failed, and the AI clients' files (`state` `ok` or `unreadable`, `~` paths) |
| `POST /api/governance/inventory` | `{ id, owner, purpose, risk, decision, note }` saves a review, `{ id, clear: true }` removes it; a bad field is a 400 that names it (`risk: minimal, limited, high, unacceptable or empty`) and nothing is written |
| `GET /api/governance/controls` | `{ generatedAt, notice, frameworks, clauses, controls: [{ id, title, status, evidence, link, clauses }] }` |
| `GET /api/governance/evidence?format=json\|md` | a download (`json` by default); any other format is a 400 |

### Known limits of the inventory

- An owner is free text: the field suggests the display names of the owner and the enabled accounts
  ([docs/users.md](users.md)), but any text is kept as typed, not linked to an account.
- Only the client files listed above are read; a project's own `.mcp.json` or `.cursor/mcp.json` is not.
- The package is the first argument that looks like one, so `docker run …` shows `run`, and a plain flag's value can
  be taken for it (`--port 8080` shows `8080`); only flags whose name looks secret skip the argument after them.
- No questionnaire, impact assessment workflow or external attestation. A decision is a record, not enforcement.

## Audit log

One record of the changes made to ZAAFPulse from the dashboard, its settings and sign-in, and of the changes an API
key makes to runs, knowledge bases and sessions: who, when, what, and the outcome (the list below says exactly what).
It is kept for at least six months, chained so that an edited or deleted line shows, and exportable. It lives in
`.data/audit/` next to your other data and never leaves the machine.

Open it under **Governance → Audit log** (`#governance/audit`) in the sidebar.

### What is recorded

| What | Action | Notes |
|---|---|---|
| Every change made from the dashboard (`POST`, `PUT`, `PATCH`, `DELETE` under `/api/`) | `POST /api/gateway/keys`, `POST /api/monitors/:id` … | the route with ids as `:id`, the body's `action`/`op` word (`op`), the target's id and name, the outcome and the HTTP status |
| Every write of `config.json`, from any settings page or the integrations editor | `config.change` | the changed fields as `{ path, from, to }`, secrets masked |
| Sign-in | `auth.login`, `auth.login_failed`, `auth.lockout`, `auth.logout`, `auth.local_code`, `auth.password_set` | the account and its role, the browser's known-device id (password mode) and its address; a failure names the account it was for as its target when that account exists (`owner` for the admin password); never a password or a cookie; `auth.logout` only when the request held a session |
| Dashboard accounts ([docs/users.md](users.md)) | `user.create`, `user.update`, `user.password_reset`, `user.delete`, `account.password_change` | the account as the target; an update lists the role, display name or disabled as `changes`; never a password |
| A request a reviewer's or a viewer's role does not allow | the route, as `GET /api/trace/content` or `POST /api/config` | refused, 403, reads included |
| API keys | `key.create`, `key.update`, `key.revoke`, `key.delete` | the key's id and name; an update lists the fields that changed |
| Collaboration runs | `run.approve`, `run.reject`, `run.cancel` | from the dashboard, `/v1/runs/<id>/resume|cancel` and A2A, with the key that asked; an approval a key may not give is recorded as refused; a run id that is not id-shaped (`A-Z a-z 0-9 _ . : -`, at most 80) is left out of the target |
| Knowledge and sessions, with an API key | `knowledge.ingest`, `session.delete` | a document added through `POST /v1/knowledge/<kb>/documents` (the base's id, never the text) and a session deleted through `DELETE /v1/sessions/<id>`, with the key |
| Agent and runtime control | `agent.pause`, `agent.stop`, `runtime.start` … | mirrored from `.data/agent-audit.jsonl`, which stays; a rule's own pause names the rule |
| A secret shown in the Trace explorer | `trace.reveal` | which content and which mask, never the value |
| The log itself | `audit.key_created`, `audit.rekey`, `audit.prune`, `audit.gap` | see below |

Not recorded:

- read requests, including reads that travel as `POST`: polling or listing runs, reading a session, testing a provider,
  an integration or a second-brain source, listing a provider's models, reading or searching a knowledge base, the
  gateway logs and quota, and the previews `/api/evals/preview`, `/api/cost/settings/preview` and
  `/api/cost/restate/preview`;
- requests refused before sign-in, and a sign-out from a browser that held no session;
- gateway model calls (`/v1/chat/completions`, `/v1/messages`, `/v1/responses`, embeddings …), which are already in
  `gateway-usage.jsonl`;
- the work an API key starts (`/v1/agents/<id>/run`, `/v1/orchestrate`, `/v1/collaborate`, `/v1/workflows/<id>/run`,
  A2A tasks) and the sessions and memory that work writes; the runs are in the run store and the Trace explorer;
- MCP tool calls, which stay in `.cache/mcp-audit.jsonl`, whose lines now carry the `keyId` when the call came with an
  API key;
- changes made outside the server: a hand edit of `config.json` or of the data folder, and the CLI's commands other
  than `set-password`.

A request body is never copied; only the short fields named above are kept. `GET /api/gateway/<route>` is a 405 for
every route but `logs`, so a read cannot change anything there.

#### Who acted

| `actor.via` | Fields | When |
|---|---|---|
| `dashboard` | `user` (the account, `owner` for the owner; `null` before sign-in), `role` (`owner`, `reviewer` or `viewer`), `mode` (`local`, `password` or `none`), `device` (password mode: the browser's known-device id), `ip` | a signed-in dashboard request, and sign-in itself |
| `apikey` | `keyId` | a gateway request with a ZAAFPulse API key, and everything it starts |
| `cli` | none | `zaafpulse set-password` (recorded when the server next starts) |
| `system` | none | ZAAFPulse itself: retention, the key, a gap, background work |
| `rule` | `rule` | a Monitors rule that paused an agent |

#### An entry

One JSON object per line, for example:

```json
{"seq":42,"ts":"2026-11-03T09:12:40.118Z","action":"config.change","actor":{"via":"dashboard","user":"owner","role":"owner","mode":"local","device":null,"ip":"127.0.0.1"},"outcome":"ok","target":{"type":"config","id":"gateway"},"changes":[{"path":"gateway.guardrails.secrets","from":"redact","to":"block"}],"prev":"5f0c…","mac":"a91e…"}
```

`outcome` is `ok`, `refused` (a 4xx, or an admin answer with `errors`) or `error` (a 5xx). A change list holds at most
50 changes, then `truncated` says how many more there were. A value under a key that looks secret (`key`, `token`,
`secret`, `password`, `auth`, `header`, `credential`) or a secret reference is written as `‹changed›`, numbers
included: a token limit under a `token`, `key` or `auth` name shows as `‹changed›`. Booleans stay readable. So do
the guardrails modes at any `…guardrails.secrets` path (`gateway.guardrails.secrets`, or a key's own) and
`cost.keyCenters` (key ids). Other text is cut to 120 characters, after the credentials of a URL in it become
`scheme://‹changed›@host` (a password with an `@` in it included) and the values of its query parameters named
`api_key`, `apikey`, `key`, `token`, `access_token`, `secret`, `password`, `auth`, `signature` or `sig` become
`‹changed›`.

### The chain

Every entry has `seq` (1, 2, 3 … across months), `prev` (the `mac` of the entry before it; `genesis` for the very
first) and `mac` = HMAC-SHA256(key, `prev` + newline + the entry without `mac` as canonical JSON, keys sorted),
computed over the JSON as it is stored. Editing a field, deleting a line anywhere, moving one or writing a line that
does not parse breaks the chain at that entry. The oldest kept entry must be seq 1 (`genesis`) or the `keptFrom`
recorded by the latest prune, so deleting a whole month without a prune entry shows on verify too. Entry 1 of a log is
always `audit.key_created`. A torn last line left by a crash is dropped before the next entry is appended.

The key is 32 random bytes, made on the first entry, in the OS secret store as `acc.audit-chain` (the Keychain,
the keyring or the encrypted secret file, as for provider keys). When the store cannot hold it, it is
`.data/audit/chain.key` (mode 0600) and every entry says `"keyed": "file"`. Before the first key goes to the store,
ZAAFPulse checks that the store works with a probe item, `acc.audit-chain.probe`, which may stay in the store. A new
data folder that finds a key in the store already (another checkout or data folder of the same user) uses that key,
and its log still starts with `audit.key_created`.

What the chain protects depends on where the key is. With the key in the store, someone who can change the files in
`.data/audit/` but cannot read the store cannot edit, delete or move an entry unseen. With `chain.key`, whoever can
read the data folder can read the key too and rewrite the whole log; the chain then only shows changes made without
the key, such as a hand edit or a deleted line. Whoever can read the store's item can rewrite the log either way.

A store that does not give the key back never makes a new key while entries exist: a locked Keychain, a read that
fails, a secret the encrypted file cannot decrypt (`ZAAFPULSE_MASTER_KEY` changed or missing), or an item that holds
something other than a key. The log degrades (see below) and recovers when the store answers again; the first entry
written then is followed by a verify, so `audit_chain_broken` clears without waiting for the daily pass. Only a store
that shows it works (the probe item reads back) and still has no key on a second read counts the key as gone. While
the log is degraded every change still reads the store once (the probe and second read run at most once a minute):
the Linux keyring tool gives up after 5 seconds, but the macOS `security` read has no timeout, so a hung Keychain can
hold a change until it answers.

**Verify** (the button on the page, `GET /api/governance/audit/verify`, and once a day on its own) checks every kept
entry and answers `{ ok, checked, from, to }`, or `brokenAt` (the first seq that fails) and `reason`: `entry changed`,
`chain broken`, `sequence broken`, `unreadable line` or `key missing`. A running server also knows the last entry it
wrote, so lines cut from the end show as `sequence broken`. A log whose entry 1 is not `audit.key_created`, or is one
keyed in `chain.key` while the store holds a key, shows as `chain broken` at entry 1: that is a log written again from
scratch. The page shows the server's last result, whether it came from the button or the daily pass.

When the key is gone but entries exist (a restored data folder without its Keychain item), verify says `key missing`.
The next entry does not seal over the old chain: an `audit.rekey` entry with `prev: "rekeyed"` starts a new part with
a new key, which is kept in `.data/audit/chain.key`, never in the store. Each part is checked with the key it names
(`store` or `file`), so a part sealed with a key of someone's own vouches for nothing before it: the old part is
still checked with the old key whenever that key can be read (it comes back to the store, say). Entries whose key
cannot be read, including a part keyed in an older `chain.key` that a later rekey replaced, are counted as
`unverified` and still checked for their order. The first part is checked with the store's key whenever that key
reads, whatever `keyed` its entries carry: a new key after a rekey always goes to `chain.key`, so only the first part
can be sealed with the store's key, and entries relabelled `"keyed": "file"` and sealed again with a key of someone's
own show as `entry changed`. Whenever the kept log holds a rekey, verify reports the latest one as `rekeyedAt` (its
seq) and `rekeyedTs` (the later of its time and the time of the entry before it), and the page shows "The audit
key was replaced at entry N (date). Entries before it are checked only when the old key can still be read."

A rekey always alerts, through `audit_chain_broken` (below), because it is the one change to the chain that someone
who can write `.data/audit/` but not read the store can make look valid: a rekey of their own, sealed with a
`chain.key` of their own, lets them replace every entry after it. A genuine rekey (the key really went missing) alerts
the same way, so the owner can confirm it was one.

### Retention

The files are monthly: `.data/audit/governance-YYYY-MM.jsonl` (UTC months, mode 0600, folder 0700), with no size
rotation. A month is deleted once it ended more than `governance.audit.retentionMonths` months ago (default 12, never
below 6, at most 120). The `audit.prune` entry naming the months, and the `keptFrom` seq, is written before any month
is deleted; if a delete then fails, the log shows a broken sequence until the next pass finishes the job. Verify
starts from the oldest kept entry, which the latest prune entry vouches for, so pruning never breaks it.

Change it on the page or with `POST /api/governance/settings` `{ "audit": { "retentionMonths": 24 } }`; a value
outside 6 to 120 is a 400 and nothing is written (a body that is not JSON is a 415, one that is too large a 413). The
change is itself a `config.change` entry. A lowered retention takes effect at the next daily pass (every 24 hours from
start) or at a restart.

### The daily pass

At start and every 24 hours the server imports the events the CLI left, prunes the months past the retention and
verifies the chain. Each step runs on its own, so one that fails never skips the others; a failure is logged by its
code only. A pending file that is not valid is dropped without being recorded.

### Export

**Export CSV** and **Export JSONL** download the entries of the chosen range (`GET
/api/governance/audit/export?from&to&format=csv|jsonl`), oldest first. JSONL lines are byte for byte what the log
holds, so a copy can be verified with the same key. The CSV has one line per entry (`seq, time, actor, actor_id, ip,
action, op, target_type, target_id, target_name, outcome, status, changes, prev, mac`; `actor_id` is the account of a
dashboard entry, else the key, the rule or the device); a cell that starts with `=`,
`+`, `-`, `@`, a tab or a carriage return gets a leading `'`, as the cost export does. At most 100,000 entries per
export (413 beyond: narrow the range).

### When an entry cannot be written

A full disk, a permission problem or a locked secret store never stops the change itself. The server remembers
since when writing fails and how many entries it missed (`missed` counts entries: a dashboard config change is two,
its `config.change` and its request); the page shows "Degraded since …", `GET /api/governance/audit/status` returns
`degraded: { since, missed }`, and the Monitors rule `audit_degraded` alerts. Once writing works again, one
`audit.gap` entry records how many entries were missed and since when, then the chain goes on.

### Monitors

- `audit_degraded` (on by default): triggered while entries cannot be written; its value is the number of entries
  missed.
- `audit_chain_broken` (on by default): triggered when the last verify failed; its value is the seq where the chain
  broke (0 when the key is missing) and its title the reason. Also triggered, titled `rekeyed` with the rekey's seq,
  while the chain holds a rekey whose entry is less than 7 days old: one alert per rekey, which clears after 7 days
  unless the chain is otherwise broken.

Neither pauses anything, and neither alert carries anything from an entry.

### API

All behind the dashboard sign-in; no gateway key or MCP client reaches them.

| Route | What |
|---|---|
| `GET /api/governance/audit?from&to&actor&user&action&outcome&limit&before` | entries, newest first, at most 500 per page (default 100); `from`/`to` are `YYYY-MM-DD` (a UTC day; `to` is the end of it) or ISO times with a zone (`Z` or `+07:00`), `actor` a kind (`dashboard`, `apikey`, `cli`, `system`, `rule`) or a key id, `user` an account name (`owner` included), `action` text found in the action or its `op`; the answer is `{ entries, next, skipped }` (`next` is the `before` of the next page, `skipped` counts unreadable lines) |
| `GET /api/governance/audit/export?from&to&format=jsonl\|csv` | a download |
| `GET /api/governance/audit/verify` | `{ ok, checked, from, to, brokenAt?, reason?, unverified?, rekeyedAt?, rekeyedTs? }` |
| `GET /api/governance/audit/status` | `{ retentionMonths, months: [{ month, bytes }], bytes, degraded, lastVerify }` |
| `POST /api/governance/settings` | `{ "audit": { "retentionMonths": n } }` (6 to 120; JSON only: 415 otherwise, 413 for a body that is too large) |

A bad query value is a 400 that names it, as `limit: a whole number from 1 to 500`.

### Known limits

- Entries from before dashboard accounts ([docs/users.md](users.md)) have no `user` or `role`: they say `dashboard`,
  the sign-in mode, the device and the address.
- The chain is anchored nowhere outside this machine. Someone with the key and the files can rewrite both.
- Someone who can write `.data/audit/` but not read the store can still cut the chain at an entry of their choosing:
  an `audit.rekey` of their own, sealed with a `chain.key` of their own, and entries of their own after it. What comes
  before the cut is still checked with the store's key and cannot be changed unseen; what came after it can be
  replaced. Such a rekey alerts (`audit_chain_broken`, `rekeyed`) and shows on the page; one while the store still
  holds its key is worth a look. The alert's 7 days count from `rekeyedTs`, which is never before the entry the rekey
  follows; a rekey dated in the future keeps alerting until 7 days after that date.
- Removing the whole log while the server is stopped is caught only by the rule for entry 1: a log written again by
  hand shows (its entry 1 is not `audit.key_created`, or is keyed in `chain.key` while the store holds a key). A folder
  emptied and left for the server to start over is not caught: the new log begins with its own `audit.key_created`,
  as a first start does. While the server runs, the last entry it wrote catches it.
- Two rare cases look like a rewrite once the store's key reads, because the first part is then checked with it:
  - a log whose first key went to `chain.key` (the store was locked or could not take it) while the store held a key
    of its own (another data folder's): `chain broken` at entry 1, or `entry changed` at the first kept entry once
    entry 1 has been pruned;
  - a genuine rekey whose store-sealed months have all been pruned, when the old key later comes back to the store:
    the first kept part was sealed with `chain.key`, so it shows as `entry changed`.
- The degraded state (`since`, `missed`) is kept in memory: a restart while degraded loses it, so no `audit.gap`
  entry records the entries missed before the restart.
- `zaafpulse set-password` leaves its event in `.data/audit/pending/`; it becomes an entry when the server starts or
  runs its daily pass.
- The server is the only writer: one data folder is served by one server process. Two servers on one folder would each
  continue the chain from where they last saw it.
- Lines cut from the end of the newest month while the server was stopped hold together as a chain; only a running
  server notices them (it knows the last entry it wrote).

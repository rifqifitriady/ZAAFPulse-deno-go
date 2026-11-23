# Upgrading from v1.1.x to v2

Date: 2026-11-23. For users of ZAAFPulse v1.1.x who move to v2.0.0 from this repository's release. How to install,
upgrade and roll back is in [docs/install.md](../install.md), and what v2.0.0 holds and what is still being verified in
[its release notes](../release-notes/v2.0.0.md); this note says what changes for you and what to check.

## In short

- v2 is two programs instead of one Node.js app: `zaafpulse`, the dashboard and everything it did before, now a Deno
  binary, and `zaafpulse-gw`, a Go gateway it starts and watches.
- Your data folder, `config.json`, keys, history, budgets and audit log carry over unchanged, and v1.1.0 can still
  read the folder after v2 has used it.
- Out of the box v2 behaves as v1.1: the Go gateway is not in front (`gateway.engine` is `"js"`), and every outbound
  call leaves directly. The Go parts are opt-in, class by class. The egress allow-list enforces and the network
  lock-down is asked for by default, but both act only on calls that go through Go (and every provider's host is on
  the list), so nothing changes until you move a class
  ([docs/governance.md](../governance.md#network-egress-settings)).
- After the upgrade, run `zaafpulse doctor`. The installer registers your AI clients' MCP entries again; doctor's
  **MCP entries** row says when one still starts a program that is gone.

## What changes

**Install and platforms.**

- The installers and the release come from
  [rifqifitriady/ZAAFPulse-deno-go](https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases), not from
  `raw.githubusercontent.com/rifqifitriady/ZAAFPulse/main`. The v1 installers do not move you to v2.
- No Node.js is needed or downloaded. A release is one archive per platform holding both programs (34 to 40 MB, where
  the v1.1.0 download was 84 to 128 MB); there are no loose binaries.
- No v2 build exists for Macs with an Intel CPU, Alpine and other musl Linux, armv7l, ppc64le, s390x or 32-bit
  Windows: a release has five targets (darwin-arm64, linux-x64, linux-arm64, win-x64, win-arm64). The v2 installer
  stops there and prints the v1.x command, which keeps working; an Intel Mac stays on v1.x.
- `--source` and `--version main` are gone from `install.sh`; v2 installs releases only.
- `--uninstall` keeps your data (v1 deleted the Linux data folder inside the install folder); `--purge` deletes it.
- The Docker image still runs on Node.js for now.

**Runtime permissions (Deno).** The dashboard runs inside a Deno permission set: it may write only the data folder,
the temp folder, the skill folders and the AI clients' config folders, and run only a listed set of programs. What it
may not do fails loudly with "Requires ... access to ..." in the page or the log.

- In the installed binary, a program outside the list is refused: a Claude Code that is installed only inside an IDE
  extension folder, or a model server of your own that is not one of the presets. `zaafpulse doctor` and the
  Compliance page show what the process holds.
- A service started from source with `deno run` has its write and run lists written into the service file when it is
  installed. Run `zaafpulse service install` again after approving a model server, moving Claude Code or changing
  `CLAUDE_CONFIG_DIR` ([docs/install.md](../install.md#on-deno-permissions-are-fixed-when-the-service-is-installed)).
- On Linux, agent control needs `ps` and `lsof` installed (Deno cannot read `/proc`); without them **Pause and stop an
  agent** turns `partial` and the server log says why.

**Compliance.** Governance → Compliance has twenty controls instead of eighteen. The two new ones:

- **Runtime permissions** is `partial` on a normal install: the run list holds launchers such as `osascript` and
  `claude`, which can run any code, and the evidence says so.
- **Network egress is mediated and logged** is `off` until the Go gateway is in front, then `partial` until every
  outbound class goes through it. On the installed binary it stays `partial` even then (the lock-down there is the
  egress client's alone; see [docs/governance.md](../governance.md#network-egress-settings)).

**Requests.** A request whose target is in absolute form (`GET http://host/path HTTP/1.1`, as sent to a forward proxy)
is refused with 400; v1.1 answered it. Browsers and API clients never send that form.

**URLs given to the RAG service** (knowledge ingest from a URL): the set of private
addresses refused is stricter than in v1.1, whichever way the call leaves. Among the additions: IPv4-compatible IPv6
(`::a.b.c.d`), NAT64's local-use range, site-local `fec0::/10`, Teredo, and 6to4 addresses that embed a private IPv4
address.

**MCP client entries.** Each AI client's ZAAFPulse entry names the program that runs the MCP server. An entry made by
v1.1 names v1's Node.js and its `app/mcp.js`, which the upgrade moves into `rollback/`, so that client could no longer
start ZAAFPulse's MCP server. The v2 installers therefore register again every client that holds an entry, and only
those (`zaafpulse register-mcp registered`), keeping the data folder each entry named. A system install as root does not
write the users' configs: it prints what each user runs. `zaafpulse doctor` has an **MCP entries** row that names every
client whose entry starts a program or script that no longer exists. To do it by hand:

```sh
zaafpulse register-mcp registered   # every client that holds an entry; or one client: claude, cursor, codex, ...
```

After a rollback to v1.1, register again with v1's `zaafpulse register-mcp <client>`; after a rollback to another v2
version the installer does it.

## What stays the same

- The dashboard, its pages, the API, the port (4777) and sign-in.
- The data folder and everything in it. v2 adds files there (`.data/egress.jsonl`, `.data/gateway-shadow.jsonl`,
  `.data/egress-gate.json`, the gateway's outbox) that v1.1 ignores. The upgrade check (`scripts/upgrade-check.js`:
  v1.1.0, then v2, then v1.1.0 again on one data folder) passed on macOS: the audit chain verified, the usage totals
  counted both versions' calls, and every `config.json` key v1 wrote was unchanged.
- The Agent Gateway's answers. With the Go gateway in front, a class moves to Go only after it has been compared on
  your own calls for a day and 20 calls without a difference.

## What to check after the upgrade

1. `zaafpulse doctor`:
   - `ZAAFPulse <version> (single-file build)` and `Deno <version>`;
   - `Gateway (zaafpulse-gw)  <path> — found, allowed, <version>`. "not allowed" means the folder of the programs is
     not first on `PATH`: start through the installer's `zaafpulse` command;
   - `Gateway IPC  v1, <n> schemas`;
   - `Server` answering and `Service` running;
   - `MCP clients`: when it names clients, run `zaafpulse register-mcp <client>` for each;
   - `MCP entries`: `every entry starts a program that exists`; when it names clients, run
     `zaafpulse register-mcp registered`.
2. If the installer could not register your AI clients again (it says so), run `zaafpulse register-mcp registered`.
3. Open the dashboard with `zaafpulse open` and check that the Overview shows your history.
4. Governance → Compliance: expect the two new controls as described above.

## Rolling back

`install.sh --rollback` (`install.ps1 -Rollback` on Windows) stops v2 and puts the install it replaced back in place,
on the same data folder. The v1.1.x installer works too, after `zaafpulse service uninstall`; on Windows also delete
`zaafpulse.exe` and `zaafpulse-gw.exe` from `%LOCALAPPDATA%\ZAAFPulse\bin`, or `zaafpulse` keeps starting v2. Both ways,
step by step: [docs/install.md, Rolling back](../install.md#rolling-back). Then register your AI clients again with
v1's `zaafpulse register-mcp <client>`.

If you turned on the Go gateway and only want that off, you do not need to roll back: set `"gateway": { "engine":
"js" }` and `"egress": { "lockdown": false }` in `config.json` and run `zaafpulse service restart`
([the switch runbook](2026-11-23-switch-runbook.md#5-rolling-back)).

## Differences you may notice once the Go gateway is in front

None of these apply while `gateway.engine` is `"js"`, the default.

**The gateway process.**

- The Overview's topology card gets a chip, `Gateway: Go (front) · ready · v<version>`. When the gateway cannot run the
  chip turns red (`fallback`) and the dashboard serves the port itself, so nothing goes down.
- A request with a malformed target gets Go's plain-text `400 Bad Request`, and plain HTTP sent to the TLS port gets
  a 400 text where v1.1 closed the connection.
- `gateway.memLimitMB` (default 64) is the gateway's soft memory limit.

**Calls the Go engine answers** (only the classes you moved):

- The usage line carries `"ipc"`; the numbers are the same.
- Budgets are checked from a snapshot the dashboard sends every second. Between snapshots the gateway counts its own
  calls at list price, so a budget priced otherwise can lag by up to a second. When the snapshot is older than 5 s,
  metered calls still pass (as v1.1 did when it could not check), each marked `budget: "unchecked"`, with an alert and
  an audit entry. A budget the snapshot shows as used up is still refused.
- Go trusts the operating system's root certificates plus `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS` and `DENO_CERT`. A
  root that is only in the OS store (a company's TLS-inspection root, for one) is trusted by Go and not by the
  dashboard's own calls.
- Streams: Go stops reading the provider while your client is not reading; a single provider line or event over 16 MiB
  ends the stream.
- OAuth2 and service-account providers: Go keeps its own token, so a provider may see up to twice the token requests,
  and the Providers page shows the dashboard's token expiry, not Go's.

**Outbound calls through Go** (only the egress classes you set to `go`):

- Requests ask for `gzip` only, and a gzip answer arrives decoded. The recorded byte counts are of the decoded body.
- A gateway restart cuts the outbound calls in flight.
- The allow-list enforces by default: a call to a host that no setting names (a redirect to another host, an intranet
  page ingested with `ZAAFPULSE_ALLOW_PRIVATE_URLS=1`) is refused with `EGRESS_REFUSED` and recorded. `"egress": {
  "allowList": "report" }` records it and lets it pass instead.
- The cloud metadata addresses are refused whatever the allow-list says.
- A redirect to another origin drops `Authorization`, `Cookie` and `Proxy-Authorization`.
- Local model servers on another machine are probed over `https` too when their URL says so.
- Under `egress.lockdown`, an outbound call made while the gateway is not running fails with `EGRESS_UNAVAILABLE`
  instead of going direct.

The full lists, with the reasons, are in the results documents of each phase in this folder (P4a to P4h, the token
saver, P5, P6).

# Installing ZAAFPulse

ZAAFPulse v2 runs on these systems:

- **macOS** 12 or newer, on Apple silicon or Intel
- **Linux** with glibc: Ubuntu, Debian, RHEL 8/9, Rocky, AlmaLinux, Fedora, Amazon Linux and SUSE, on x64 or arm64
- **Windows** 10/11, on x64 or arm64

A release is two programs, `zaafpulse` (the dashboard, a Deno binary) and `zaafpulse-gw` (the Go gateway), packed in one archive per system. They need nothing else: no Node.js, no Deno, no npm packages. The installers download no Node.js any more.

**Only v1.x runs on** Alpine and other musl Linux, armv7l, ppc64le, s390x and 32-bit Windows. There is no v2 build for them, and the v2 installers stop there and print the v1.x command. Use the v1.x installer from [rifqifitriady/ZAAFPulse](https://github.com/rifqifitriady/ZAAFPulse):

```bash
curl -fsSL https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/main/install.sh | sh
```

## Pick a way

| You want | Do this |
|---|---|
| It on your laptop | `curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh \| sh` (macOS/Linux) or `irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1 \| iex` (Windows PowerShell) |
| A shared server | `curl -fsSL …/install.sh \| sudo sh -s -- --server` |
| Containers | `docker compose up -d` with [compose.yaml](../compose.yaml) |
| No installer | a `zaafpulse-<version>-<os>-<cpu>` archive from [Releases](https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases), unpacked ([Release packages](#release-packages)) |
| To hack on it | `git clone …`, then `npm start` |

## The installers

Both installers are release files themselves, so the `releases/latest/download/` address always gets the installer of the latest release, and `releases/download/vX.Y.Z/` the one of that release.

### macOS and Linux: `install.sh`

```bash
curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sh
```

The installer:

1. Picks the package for your OS and CPU (`darwin` or `linux`, `x64` or `arm64`), and stops on a system that only v1.x supports.
2. Downloads it with `SHA256SUMS.txt` from the latest release (or the one named with `--version`) and checks it. A mismatch, or a package missing from `SHA256SUMS.txt`, installs nothing.
3. Unpacks it, clears the macOS quarantine flag on both programs, and runs the new `zaafpulse version`. When that fails it stops with "nothing was changed".
4. Keeps an earlier install (v1.x or v2) for `--rollback`, see [Upgrading from v1.x](#upgrading-from-v1x).
5. Puts both programs in the install folder and adds the `zaafpulse` command.
6. Starts ZAAFPulse in the background, and again at every login or boot.
7. Prints a summary: the dashboard address, the commands, and the rollback and uninstall lines.

| | As your user | As root (`sudo`) on Linux |
|---|---|---|
| Programs | `~/.local/share/zaafpulse/bin/zaafpulse` and `zaafpulse-gw` | `/opt/zaafpulse/bin` |
| Command | `~/.local/bin/zaafpulse` (added to PATH in your shell profile) | `/usr/local/bin/zaafpulse` |
| Earlier install, kept for `--rollback` | `~/.local/share/zaafpulse/rollback` | `/opt/zaafpulse/rollback` |
| Data | macOS: `~/Library/Application Support/ZAAFPulse`; Linux: `~/.local/share/zaafpulse/data` | `/var/lib/zaafpulse` (owned by the `zaafpulse` system user) |
| Background | macOS: LaunchAgent `com.zaaf.aipulse.server`; Linux: systemd user service `zaafpulse` | systemd service `zaafpulse`, running as the `zaafpulse` user with `ProtectSystem`, `ProtectHome` and `NoNewPrivileges` |

The `zaafpulse` command is a two-line wrapper:

```sh
#!/bin/sh
PATH="/home/you/.local/share/zaafpulse/bin:$PATH"; export PATH
exec "/home/you/.local/share/zaafpulse/bin/zaafpulse" "$@"
```

It puts the install folder first on `PATH` because Deno allows the dashboard to start `zaafpulse-gw` by name, and finds that name through `PATH` when it starts. `zaafpulse service install` writes the same folder first into the service's `PATH`.

`ZAAFPULSE_PREFIX` moves the install folder of a user install (`ZAAFPULSE_PREFIX=/opt/zp sh install.sh`). The installer writes the folder as its physical path, with links resolved (`/tmp` becomes `/private/tmp` on macOS), because Deno compares the folder on `PATH` with the path the binary was started by. The folder may not contain `"`, `$`, `` ` `` or `\`.

The options go after `sh -s --`:

| Option | |
|---|---|
| `--server` | Listen on all interfaces (`0.0.0.0`) and ask for the admin password |
| `--host ADDR` / `--port N` | Listen on a specific address and port (default `127.0.0.1:4777`) |
| `--version vX.Y.Z` | A specific release. v2 installs releases only: `--version main` is gone |
| `--archive FILE` [`--sums FILE`] | Install a package you already have (offline, CI). Without `--sums` it warns that the package is not checksum-verified |
| `--verify-attestation` | Also check the package's attestation with `gh attestation verify`. Stops when `gh` is missing or the check fails |
| `--rollback` | Go back to the install this one replaced ([Rolling back](#rolling-back)) |
| `--no-service` | Do not start it in the background, and leave a running service alone |
| `--no-modify-path` | Leave your shell profile alone |
| `--uninstall [--purge]` | Remove it; `--purge` also deletes the data |

An install made as root (`sudo sh`, the systemd system service) must be upgraded and uninstalled as root too: run without `sudo`, the installer works on your user install in your home folder and leaves the system one, and its service, as they are.

`--source DIR` is gone: a source tree needs Deno and Go to build. Build a package with `deno task build --package` and install it with `--archive`.

`--uninstall` stops the service and removes the command and only the folders the installers put in the install folder (`bin`, `rollback`, `rollforward`, and v1's `app` and `node`), then the install folder itself when it is empty. On Linux the data folder `~/.local/share/zaafpulse/data` sits inside the default install folder, so the data is kept. `--purge` deletes the data too.

On Linux the user service keeps running after you log out only with lingering enabled. The installer tries to enable it; if that fails, run `sudo loginctl enable-linger $USER`.

Machines without systemd (some containers, WSL1) get the command but no service. Run `zaafpulse start` under your own supervisor instead.

### Windows: `install.ps1`

In PowerShell (no administrator needed):

```powershell
irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1 | iex
```

With options:

```powershell
& ([scriptblock]::Create((irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1))) -Server -Port 4777
```

It works like `install.sh`: it picks the `win-x64` or `win-arm64` package, checks it against `SHA256SUMS.txt`, runs `Unblock-File` on both `.exe` files (a browser download marks them as from the internet), runs the new `zaafpulse.exe version` before it changes anything, keeps an earlier install for `-Rollback`, and prints a summary.

Where things go:

- `zaafpulse.exe` and `zaafpulse-gw.exe` go in `%LOCALAPPDATA%\ZAAFPulse\bin`, and an earlier install in `%LOCALAPPDATA%\ZAAFPulse\rollback`.
- That `bin` folder is added to your user PATH, once and spelled exactly as the installer runs the binary (Deno's PATH match is case-sensitive). This is what lets `zaafpulse.exe` start `zaafpulse-gw.exe`.
- Data lives in `%APPDATA%\ZAAFPulse`.

How it runs:

- ZAAFPulse starts now and at every logon, through a hidden launcher in your Startup folder. The launcher restarts it if it exits.
- A Start menu shortcut runs `zaafpulse open`, so the dashboard opens signed in.
- `-System` (from an elevated PowerShell) runs it instead as a Task Scheduler task at boot, as SYSTEM, for a server that has no logged-on user.

Other options, as in `install.sh`: `-HostAddress`, `-Version vX.Y.Z`, `-Archive FILE [-Sums FILE]`, `-VerifyAttestation`, `-Rollback`, `-NoService`, `-Uninstall [-Purge]`. `-Source` is gone.

An install made with `-System` must also be given `-System` on upgrades and with `-Uninstall` (from an elevated PowerShell): without it the installer stops and installs the Startup entry, not the scheduled task, so the task keeps running and an upgrade adds a Startup entry next to it. `-Uninstall` without `-System` warns when the `ZAAFPulse` scheduled task is still there; remove it from an elevated prompt with `schtasks /Delete /TN ZAAFPulse /F`.

Before it moves an earlier install, the installer stops its service through that install's own command, then ends what still runs from `%LOCALAPPDATA%\ZAAFPulse` (a running `.exe` cannot be moved), waiting up to 10 seconds. With `-NoService` it leaves the service and running processes alone.

`-Uninstall` removes the programs, the saved installs, the PATH entry and the Start menu shortcut, and keeps the data in `%APPDATA%\ZAAFPulse`. `-Purge` deletes that folder and `%LOCALAPPDATA%\ZAAFPulse` too.

## Upgrading from v1.x

Run the v2 installer, with the same options you installed with (`sudo` and `--server` for a server):

```bash
curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sh
```

```powershell
irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1 | iex
```

The installer:

1. Finds the current install: v1.x has `<prefix>/app` (and `<prefix>/node` when it downloaded Node.js); v2 has `<prefix>/bin/zaafpulse`.
2. Stops its service through the old `zaafpulse` command, before that command is replaced.
3. Moves it into `<prefix>/rollback`: v1's `app`, `node` and command, or v2's `bin` and command. `<prefix>/rollback/VERSION` holds the old version (`v1.1.0`, or `unknown`). One install is kept; an earlier saved one is replaced.
4. Installs v2 and its service through the new command.

`<prefix>` is `~/.local/share/zaafpulse`, `/opt/zaafpulse` as root, and `%LOCALAPPDATA%\ZAAFPulse` on Windows.

The data folder is never touched: settings, keys, history, budgets and the audit log carry over, and v1.1.0 can still read the folder after v2 has used it. Upgrading from an earlier v2 works the same way.

An upgrade cut off half way (power loss, Ctrl-C) leaves `<prefix>/rollback.new`. The next run of the installer finishes the move or puts the old install back, whichever applies.

## Rolling back

There are two ways back to the install the upgrade replaced.

**1. `--rollback`** (`-Rollback` on Windows):

```bash
curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.sh | sh -s -- --rollback
```

```powershell
& ([scriptblock]::Create((irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/latest/download/install.ps1))) -Rollback
```

It stops the v2 service, moves the current install into `<prefix>/rollforward`, puts `<prefix>/rollback` back in place (with its `zaafpulse` command), reinstalls the service through that command and says which version is active. When nothing is saved it says "nothing to roll back to" and changes nothing. One rollback is possible: `<prefix>/rollback` is removed by it. The next install removes `rollforward`; running the v2 installer again is the way forward.

**2. The v1.1.x installer**, from [rifqifitriady/ZAAFPulse](https://github.com/rifqifitriady/ZAAFPulse). It does not stop a running v2 service, so stop it first:

```bash
zaafpulse service uninstall
curl -fsSL https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.sh | sh -s -- --version v1.1.0
rm -rf ~/.local/share/zaafpulse/bin      # the v2 programs; /opt/zaafpulse/bin as root
```

```powershell
zaafpulse service uninstall
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.ps1))) -Version v1.1.0
Remove-Item "$env:LOCALAPPDATA\ZAAFPulse\bin\zaafpulse.exe", "$env:LOCALAPPDATA\ZAAFPulse\bin\zaafpulse-gw.exe"
```

The v1.1.x installer writes its own `app` folder and `zaafpulse` command, so on macOS and Linux the leftover `bin` folder is inert; delete it anyway. **On Windows the two `.exe` files must be deleted:** the v1 installer puts `zaafpulse.cmd` into the same `bin` folder, and Windows runs `zaafpulse.exe` before `zaafpulse.cmd` (`.EXE` comes first in `PATHEXT`), so `zaafpulse` would still start v2. If the v2 installer runs later, it treats the v1 `app` as the current install.

## Release packages

Each [release](https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases) has one archive per platform, the two installers and `SHA256SUMS.txt`:

| File | For |
|---|---|
| `zaafpulse-<v>-darwin-arm64.tar.gz` / `-darwin-x64.tar.gz` | macOS on Apple silicon or Intel |
| `zaafpulse-<v>-linux-x64.tar.gz` / `-linux-arm64.tar.gz` | Linux on glibc (not Alpine) |
| `zaafpulse-<v>-win-x64.zip` / `-win-arm64.zip` | Windows 10/11 |
| `install.sh`, `install.ps1` | The installers of this release |
| `SHA256SUMS.txt` | The SHA-256 of every other file |

Each archive holds one folder, `zaafpulse-<v>-<os>-<cpu>/`, with `zaafpulse` (`zaafpulse.exe`), `zaafpulse-gw` (`zaafpulse-gw.exe`) and `LICENSE`. There are no loose binaries.

`zaafpulse` contains the Deno runtime and the app, and serves the app from inside itself: nothing is unpacked. It is the whole dashboard: `zaafpulse start`, `mcp`, `doctor` and `service install` are subcommands of the one file. `zaafpulse service install` points launchd, systemd or the Startup folder at the binary itself, with the data folder as the service's working folder, and puts the binary's folder first in the service's `PATH` when `zaafpulse-gw` sits next to it. `zaafpulse doctor` prints `Deno <version>`, `(single-file build)` and a line `Gateway (zaafpulse-gw): <path> — found, allowed, <version>`.

### Running from an unpacked archive

```bash
tar -xzf zaafpulse-2.0.0-linux-x64.tar.gz
cd zaafpulse-2.0.0-linux-x64
D="$(pwd -P)"
PATH="$D:$PATH" "$D/zaafpulse"                       # http://localhost:4777
PATH="$D:$PATH" "$D/zaafpulse" service install       # run in the background and at startup
```

```powershell
Expand-Archive zaafpulse-2.0.0-win-x64.zip .
$d = (Resolve-Path .\zaafpulse-2.0.0-win-x64).Path
$env:Path = "$d;$env:Path"
& "$d\zaafpulse.exe"
```

The folder goes first on `PATH`, spelled as the binary is started: Deno allows `zaafpulse` to start `zaafpulse-gw` only when that name is found there, in that folder, first. The installer's `zaafpulse` command does this for you. Without it, everything works except the gateway in front (`gateway.engine` `"front"`): the dashboard falls back to its built-in gateway with the reason "zaafpulse-gw is next to zaafpulse but not allowed to run: put its folder first on PATH (the installer's zaafpulse command does)", and `zaafpulse doctor` shows "not allowed".

To build packages yourself: `deno task build --package` builds this machine's package (it needs Go on `PATH`), and `--target x86_64-unknown-linux-gnu` or `--all` builds others. On Windows `--package` builds only the `win-*` packages.

## Unsigned binaries: first start and verifying a download

The macOS binaries are ad-hoc signed, not notarized; the Windows binaries are not signed. Your system may stop them the first time when they come from a browser. Their integrity comes from `SHA256SUMS.txt` and from the build attestations.

- **macOS Gatekeeper:** a browser download is quarantined, and macOS refuses to open it. In the unpacked folder, run once:

  ```bash
  xattr -d com.apple.quarantine zaafpulse zaafpulse-gw
  ```

  Or try to start `zaafpulse`, then open **System Settings → Privacy & Security**, choose **Open Anyway** next to the message about it, and confirm; do the same for `zaafpulse-gw`. The installer's `curl` download is not quarantined, and the installer clears the flag of a package given with `--archive`, so an install needs neither.
- **Windows SmartScreen:** on the first start of each `.exe` from a browser download, choose **More info → Run anyway**: once for `zaafpulse.exe`, once for `zaafpulse-gw.exe`. The installer's download is not marked, and it runs `Unblock-File` on a package given with `-Archive`.

**Check the checksum.** Download `SHA256SUMS.txt` next to the archive, then in that folder:

```bash
shasum -a 256 -c --ignore-missing SHA256SUMS.txt      # macOS
sha256sum -c --ignore-missing SHA256SUMS.txt          # Linux
```

Each file you have prints `OK`. In PowerShell, compare the hash with the archive's line in `SHA256SUMS.txt` (case does not matter):

```powershell
(Get-FileHash zaafpulse-2.0.0-win-x64.zip -Algorithm SHA256).Hash
Select-String zaafpulse-2.0.0-win-x64.zip SHA256SUMS.txt
```

The checksum comes from the same GitHub release, so it protects against damaged or altered downloads, not against a compromised GitHub account.

**Check the attestation.** Every archive, both binaries inside each, `install.sh`, `install.ps1` and `SHA256SUMS.txt` have a build-provenance attestation (Sigstore), signed as made by this repository's release workflow from a version tag. With the [GitHub CLI](https://cli.github.com):

```bash
gh attestation verify zaafpulse-2.0.0-linux-x64.tar.gz --repo rifqifitriady/ZAAFPulse-deno-go
```

A pass prints `✓ Verification succeeded!` and names the workflow and the tag it ran on:

```text
- Attestation #1
  - Build repo:..... rifqifitriady/ZAAFPulse-deno-go
  - Build workflow:. .github/workflows/release.yml@refs/tags/v2.0.0
  - Signer repo:.... rifqifitriady/ZAAFPulse-deno-go
  - Signer workflow: .github/workflows/release.yml@refs/tags/v2.0.0
```

Check that the tag is the version you downloaded. A file uploaded by hand, even with a stolen token, has no valid attestation, and the check fails. `install.sh --verify-attestation` (`install.ps1 -VerifyAttestation`) runs this check before it installs.

## Docker

```bash
echo "ZAAFPULSE_ADMIN_PASSWORD=$(openssl rand -base64 18)" > .env
echo "ZAAFPULSE_MASTER_KEY=$(openssl rand -base64 32)" >> .env
docker compose up -d
```

The image is `node:24-alpine` with only the app files: it stays on Node for now, because an Alpine image has no glibc for the v2 binaries. It runs as the unprivileged `node` user and has a healthcheck.

- **Data:** everything lives in the `/data` volume, including the encrypted secret file.
- **`ZAAFPULSE_MASTER_KEY`:** encrypts provider and integration keys. Without it, a key file is created in the volume.
- **Reverse proxy:** set `ZAAFPULSE_PUBLIC_URL` and `ZAAFPULSE_TRUST_PROXY=1` when ZAAFPulse sits behind one.

## Who can open the dashboard

There are three sign-in modes. ZAAFPulse picks one by itself:

| Mode | When | How you get in |
|---|---|---|
| **local** | 127.0.0.1, on every OS (the default) | `zaafpulse open`, or `zaafpulse start --open`. Other OS accounts on the machine and their programs cannot get in, unless the owner gave them a dashboard account: those sign in with a name and a password ([docs/users.md](users.md)). |
| **password** | Any address but 127.0.0.1, a reverse proxy (`ZAAFPULSE_TRUST_PROXY`, `ZAAFPULSE_PUBLIC_URL` or `ZAAFPULSE_ALLOWED_HOSTS`), or `ZAAFPULSE_AUTH=1` | The admin password |
| **none** | Only with `ZAAFPULSE_AUTH=none` on 127.0.0.1, and demo mode (`--demo`) on a single-user machine | Open http://localhost:4777 |

Without sign-in, any program and any other OS account on the machine could use the dashboard as you: create gateway keys, read your history and use your stored provider keys. That is why `local` is the default even on a laptop. Agent control and the Start and Stop of local model servers are off without sign-in (outside demo mode), since they signal processes and run commands.

### Getting in with local sign-in

- **After `zaafpulse start`:** run `zaafpulse open` in another terminal, or start with `zaafpulse start --open`. The browser opens signed in.
- **Background service:** after `zaafpulse service install`, or after a reboot, run `zaafpulse open` once.
- **macOS autostart** (`scripts/install-autostart.sh`): at login and on wake, the opener focuses the dashboard tab in Chrome. A new tab, or a tab that shows the sign-in page, is signed in by itself.
- **Windows:** use the ZAAFPulse Start menu shortcut, or run `zaafpulse open` in a terminal. Both sign the browser in, also after the 30-day sign-in has expired.
- **Docker and servers** listen on another address, so they always use the admin password.
- **Demo mode** (`npm run demo`, `zaafpulse start --demo`) needs no sign-in on a single-user machine, because its data is synthetic and it never reads your logs, keys or settings. On Linux with other login users it uses local sign-in too.

How it works:

- `zaafpulse open` writes a one-time code (valid for 2 minutes) into your private data folder, inside a small HTML file only your account can read. It opens that file, which forwards the browser to the dashboard.
- Only the file's path goes on the command line, never the code. Other users on Linux can read every process's command line (`/proc/<pid>/cmdline`), so they never see the code.
- The server swaps the code for an HttpOnly, SameSite=Strict cookie that lasts 30 days and survives restarts. Each code works once. The file is deleted when it is used.
- **Sign out** revokes every local-sign-in cookie at once (all browsers). Run `zaafpulse open` to sign in again.
- The sign-in cookie carries a version. After upgrading from 1.0.x, sign in once with `zaafpulse open`.

### Requests from other sites

- In every mode, a browser request whose `Origin` names another site is refused with 403. This covers the dashboard, `/v1`, `/mcp` and `/a2a`.
- The site's own pages are always allowed: the Host the request was sent to, a trusted proxy's `X-Forwarded-Host`, `ZAAFPULSE_PUBLIC_URL` and `ZAAFPULSE_ALLOWED_HOSTS`.
- On 127.0.0.1, pages on any local port are allowed too, for example a web app on http://localhost:3000.
- A browser app on another site that calls the gateway with an API key needs its origin in `ZAAFPULSE_ALLOWED_ORIGINS=https://app.example.com`. A value of `*` allows every origin, which leaves only the API key to protect the gateway.

While sign-in is off (`none`), ZAAFPulse also refuses requests that carry proxy headers (`X-Forwarded-*`, `Forwarded`, `X-Real-IP`, `Via`), browser requests whose `Referer` names another host, and cross-site requests.

This catches the usual proxy mistakes and any browser that reaches the dashboard through a proxy. It cannot catch a non-browser client behind a proxy that strips all those headers and rewrites the Host to `localhost`: such a client looks exactly like a program on this machine.

**Any reverse proxy in front of ZAAFPulse needs sign-in.** Set `ZAAFPULSE_TRUST_PROXY=1` and an admin password, or keep local sign-in.

## Server mode

ZAAFPulse listens on `127.0.0.1` unless told otherwise. Any other address, or a reverse proxy in front of it, turns on password sign-in:

- **Admin password:** the dashboard and its `/api` need one, of at least 10 characters. Set it with `zaafpulse set-password` (stored as a scrypt hash) or with `ZAAFPULSE_ADMIN_PASSWORD` (or `ZAAFPULSE_ADMIN_PASSWORD_FILE`). ZAAFPulse does not start without one, or with a shorter one.
- **Sessions:** a session is an HttpOnly, SameSite=Strict cookie, which is also `Secure` over HTTPS. It lasts 12 hours and is renewed while it is used, up to 7 days.
- **Wrong passwords:**
  - One sign-in is checked at a time per address. Every attempt counts before it is checked, so parallel guessing does not help.
  - After five failures an address waits 30 seconds. Each further failure doubles the wait, up to one hour. A day without failures clears the count.
  - IPv6 addresses count per /64, and each /56 shares a budget of 20 failures, so one network cannot rotate through its addresses.
  - When 50 failures come from all addresses within 15 minutes, every address gets one free try instead of five. This slows a distributed attack down. It never refuses a fresh address its first try, so you can still sign in with the right password while someone else is guessing.
  - A browser that signed in before gets a signed "known browser" cookie (180 days). It has its own count, so guesses from its address (a shared office or carrier connection) or from everywhere at once do not lock it out.
  - Behind a proxy, the address is the right-most `X-Forwarded-For` entry.
  - Each dashboard account (not the owner) has a budget of its own: after 10 failures for one account name, from any addresses, a fresh browser waits before it tries that account again ([docs/users.md](users.md#signing-in-and-passwords)).
- **Other people:** give them an account of their own, with a role, under **Settings → Users** ([docs/users.md](users.md)). They sign in with a name and a password; an empty name is the admin password.
- **Apps:** `/v1`, `/mcp` and `/a2a` need ZAAFPulse API keys, as they always have.
- **Host names:** with sign-in on, any host name is accepted. To restrict it, set `ZAAFPULSE_ALLOWED_HOSTS=ai.example.com,10.0.0.5`.
- **Browser protection:** every dashboard response carries a strict Content-Security-Policy, `X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`.

## Built-in safeguards

- **Stored secrets stay with their address.** Changing the URL of a provider, integration or second-brain source requires entering its secret again. A stored key is never sent to a new host.
- **Secret references:**
  - In the dashboard, a secret is either pasted (it is then saved for that entry only) or kept as it is.
  - Pointing an entry at another secret item or at an environment variable is done by hand in `config.json`. A dashboard user, or a program that reaches it, cannot borrow a secret that was saved for something else.
  - References may only name ZAAFPulse's own items (`acc.*`), and never ZAAFPulse's own settings (`ZAAFPULSE_*`, which hold the admin password and master key).
  - A remote A2A agent's token comes from a `ZP_SECRET_…` environment variable. It is only sent to the address it was set for.
  - Credentials never follow a redirect to another host.
- **Agents and tools:**
  - An agent can only run the integration tools in its allow list.
  - A key needs the `mcp` scope for its agents to use integration tools.
  - Tools that change something run only after a person approves the exact call:
    - In a workflow step with an approval gate, the agent first proposes its actions (tool and arguments). Nothing changes yet.
    - The run waits until you approve those calls in the dashboard, all of them or some by number.
    - Only the approved calls run, exactly as proposed.
    - Outside such a step the agent describes the change instead of making it.
  - Approving is a person's decision in the dashboard. An app's API key may reject or stop its own run, but approves it only when the key has **May approve its own paused runs** (for a pipeline that has its own human approval).
  - Notes agents save under one API key are not visible to another key.
  - A key can be limited to some knowledge bases, which applies to `/v1/knowledge` and to the agents run with that key.
  - A key with a monthly budget can only use models whose spend can be counted: a list price, or a private model on your own hardware. Its embeddings count toward the budget too.
- **Approvals:** approving a paused run keeps the limits of the key that started it. A run of a deleted, revoked or expired key cannot continue.
- **URL ingestion:** URLs given to the RAG service cannot reach this machine, private networks or cloud metadata, including through redirects or a second DNS answer. Pages are capped at 5 MB. For an on-premises intranet, set `ZAAFPULSE_ALLOW_PRIVATE_URLS=1`.
- **Files are private.** Everything ZAAFPulse creates in its data folder is readable only by its own account.
- **Installer integrity:**
  - The installers verify the release package against `SHA256SUMS.txt`, and install nothing when it does not match or is not listed.
  - They install releases only; there is no development-branch install in v2.
  - They run the new `zaafpulse version` before they change anything, and keep the install they replace for `--rollback`.
  - `--verify-attestation` (`-VerifyAttestation`) also checks the package's attestation with `gh`.
- **Verify a download yourself:** checksums and attestations, step by step, are in [Unsigned binaries: first start and verifying a download](#unsigned-binaries-first-start-and-verifying-a-download).
  - Once immutable releases are on for this repository ([docs/release-hardening.md](release-hardening.md)), its releases also pass `gh release verify v<v> --repo rifqifitriady/ZAAFPulse-deno-go` and `gh release verify-asset v<v> <file> --repo rifqifitriady/ZAAFPulse-deno-go`. The files and tag of such a release cannot be changed after publication.
  - Version tags are never moved. A fix ships as a new version.
- **Single-file executables serve the app from inside themselves.** `zaafpulse` reads its app files from within the binary; nothing is unpacked to a cache folder where it could be changed. What protects the app is the binary itself: check it against `SHA256SUMS.txt` and its attestation (the installers check the checksum, and the attestation with `--verify-attestation`).

### HTTPS

Choose one of these:

- **Built in:** `ZAAFPULSE_TLS_CERT=/path/fullchain.pem ZAAFPULSE_TLS_KEY=/path/privkey.pem`.
- **Reverse proxy:** use nginx, Caddy or Traefik in front, with ZAAFPulse on `127.0.0.1`, plus `ZAAFPULSE_TRUST_PROXY=1` and `ZAAFPULSE_PUBLIC_URL=https://ai.example.com`.

```nginx
location / {
  proxy_pass http://127.0.0.1:4777;
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-For $remote_addr;
  proxy_set_header X-Forwarded-Proto https;
  proxy_buffering off;           # streaming answers (SSE)
}
```

Open the port on the firewall:

| OS | Command |
|---|---|
| RHEL family | `firewall-cmd --add-port=4777/tcp --permanent && firewall-cmd --reload` |
| Ubuntu | `ufw allow 4777/tcp` |
| Windows (elevated) | `New-NetFirewallRule -DisplayName ZAAFPulse -Direction Inbound -Protocol TCP -LocalPort 4777 -Action Allow` |

## Where secrets are kept

| OS | Store |
|---|---|
| macOS | the login Keychain |
| Linux desktop | the system keyring through `secret-tool` (GNOME Keyring, KWallet) |
| Linux server, Docker | `.data/secrets.json`, encrypted with AES-256-GCM |
| Windows | `.data/secrets.json`, encrypted with AES-256-GCM, with its key sealed by Windows DPAPI for your user |

For the encrypted file:

- The key comes from `ZAAFPULSE_MASTER_KEY` or `ZAAFPULSE_MASTER_KEY_FILE`. Otherwise ZAAFPulse creates `.data/secret.key`, readable only by its user.
- Each value is bound to its name, so values cannot be swapped between names.

To choose a store explicitly, set `ZAAFPULSE_SECRET_STORE=keychain|libsecret|file`.

`config.json` only ever holds references to secrets, never their values.

## Commands

```text
zaafpulse [start] [--host H] [--port P] [--data DIR] [--demo] [--open]
zaafpulse set-password
zaafpulse service install|uninstall|status|restart [--system]
zaafpulse status | doctor | open | version | help   # doctor: where the logs are read from, how many, how fresh
zaafpulse register-mcp <claude|kiro|antigravity|cursor|vscode|claude-desktop|codex|gemini|windsurf|zed|all> [--remove]
zaafpulse mcp
```

The server stops cleanly on `SIGTERM`, Ctrl-C (`SIGINT`) and `SIGHUP`: it cancels a running judge call and flushes
its logs. Because it handles `SIGHUP` itself, a server started with `nohup node server.js &` still exits when that
terminal closes. Use `zaafpulse start` in a terminal you keep open, or `zaafpulse service install` for one that keeps
running.

## Environment variables

| Variable | Default | |
|---|---|---|
| `ZAAFPULSE_HOST` | `127.0.0.1` | Address to listen on; anything else is server mode |
| `ZAAFPULSE_PORT` | `4777` | Port (`PORT` also works) |
| `ZAAFPULSE_DATA` | see the tables above | Folder for `config.json`, `.data/` and `.cache/` |
| `ZAAFPULSE_ADMIN_PASSWORD` (`_FILE`) | | Admin password for server mode |
| `ZAAFPULSE_AUTH` | automatic | `1` (password), `local` (only this OS account, with `zaafpulse open`; the default on 127.0.0.1) or `none` (no sign-in) |
| `ZAAFPULSE_ALLOWED_ORIGINS` | | Comma-separated origins of browser apps on other sites that may call ZAAFPulse (`*` for any) |
| `ZAAFPULSE_ALLOW_PRIVATE_URLS` | | `1` lets RAG URL ingestion fetch private and intranet addresses |
| `ZAAFPULSE_TLS_CERT`, `ZAAFPULSE_TLS_KEY` | | PEM files for built-in HTTPS |
| `ZAAFPULSE_PUBLIC_URL` | | The address apps should use, shown in snippets |
| `ZAAFPULSE_TRUST_PROXY` | | `1` behind a reverse proxy (X-Forwarded-For and -Proto) |
| `ZAAFPULSE_ALLOWED_HOSTS` | | Comma-separated host names to accept in server mode |
| `ZAAFPULSE_SECRET_STORE` | per OS | `keychain`, `libsecret` or `file` |
| `ZAAFPULSE_MASTER_KEY` (`_FILE`) | | Key for the encrypted secret file |
| `CLAUDE_CONFIG_DIR` | | Claude Code's config folder: one folder, exactly as Claude Code reads it; logs are read from `<folder>/projects`. See [Where the logs are read from](#where-the-logs-are-read-from) |
| `ZAAFPULSE_CLAUDE_DIRS` | | Several Claude Code config folders for ZAAFPulse to read, comma-separated; logs are read from each `<folder>/projects`. Claude Code itself does not read it |
| `XDG_CONFIG_HOME` | `~/.config` | Parent of Claude Code's `claude/projects` folder, as newer Claude Code versions use it |
| `CODEX_HOME` | `~/.codex` | Codex's folder; logs are read from `<folder>/sessions` |
| `ZAAFPULSE_CLAUDE_BIN` | found on its own | The `claude` binary for the Claude Code judge. See [docs/evals.md](evals.md#claude-code) |
| `DEMO` | | `1` for synthetic data |

## Where the logs are read from

ZAAFPulse reads Claude Code and Codex transcripts from these folders, in this order, and uses every one that exists:

| Tool | Folders |
|---|---|
| Claude Code | each `ZAAFPULSE_CLAUDE_DIRS` entry plus `/projects` (a comma-separated list; when it is set, only these are read), otherwise `$CLAUDE_CONFIG_DIR/projects` (one folder; when it is set, only that one is read), otherwise `$XDG_CONFIG_HOME/claude/projects` (`~/.config/claude/projects`), then `~/.claude/projects` |
| Codex | `$CODEX_HOME/sessions`, otherwise `~/.codex/sessions` |

`CLAUDE_CONFIG_DIR` is one folder, as Claude Code reads it (the same variable also picks Claude Code's login, and `zaafpulse register-mcp` reads its `.claude.json` there). A value with a comma in it is ignored with "one folder only; use ZAAFPULSE_CLAUDE_DIRS for several": put several folders in `ZAAFPULSE_CLAUDE_DIRS` instead.

Only absolute folders are used (`~` counts as your home folder). A relative value, or one that would put the logs folder directly in your home folder or at the top of a drive (`CLAUDE_CONFIG_DIR=~`, `CODEX_HOME=/`, or a link that leads there), is ignored and shown as "ignored" in `zaafpulse doctor` and on the Overview. Folders that are links to one another count once.

`zaafpulse doctor` prints the folders it resolved, whether each exists, how many `.jsonl` files it holds, how old the newest one is, and whether `ZAAFPULSE_CLAUDE_DIRS`, `CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME` and `CODEX_HOME` are set. When there are no logs at all, the Overview says so and lists the same folders. Claude Desktop, ChatGPT and other chat apps keep no readable logs; they appear as apps only.

### The background service does not see your shell variables

launchd, systemd and the Windows startup entry start ZAAFPulse without your shell profile. A `CLAUDE_CONFIG_DIR`, `ZAAFPULSE_CLAUDE_DIRS`, `CODEX_HOME` or `XDG_CONFIG_HOME` you export in `~/.zshrc` or `~/.bashrc` works for `zaafpulse start` in that terminal, but the service never sees it on its own, so the Overview stays empty.

`zaafpulse service install` copies these variables, with the other settings it carries (port, data folder and so on), from the shell it runs in into the launchd plist, the systemd unit or the Windows launcher. So set them first, then install:

```bash
export CLAUDE_CONFIG_DIR=~/work-claude
zaafpulse service install
```

Run `zaafpulse service install` again after you change them. It rewrites the service file, so the new values replace the old ones. To set them by hand instead, edit the service file and restart the service:

- **macOS**: add the variable to the `EnvironmentVariables` dictionary of `~/Library/LaunchAgents/com.zaaf.aipulse.server.plist`, then `launchctl unload` and `launchctl load` that file (launchd reads the plist only when it is loaded):
  ```xml
  <key>EnvironmentVariables</key>
  <dict>
    <key>CLAUDE_CONFIG_DIR</key><string>/Users/you/work-claude</string>
  </dict>
  ```
- **Linux (systemd)**: add `Environment=` lines to the unit, or run `systemctl --user edit zaafpulse` (`sudo systemctl edit zaafpulse` for a system service) and put them under `[Service]`, then `systemctl --user daemon-reload` and restart:
  ```ini
  [Service]
  Environment=CLAUDE_CONFIG_DIR=/home/you/work-claude
  Environment=CODEX_HOME=/home/you/.codex-work
  ```
  A system service runs as its own user with a protected home, so it cannot read your logs unless you also grant access; run ZAAFPulse as your user for that.
- **Windows**: set a user environment variable, then sign out and in again (or restart the startup entry) so the launcher inherits it:
  ```powershell
  [Environment]::SetEnvironmentVariable('CLAUDE_CONFIG_DIR', 'C:\Users\you\work-claude', 'User')
  ```

Run `zaafpulse doctor` afterwards to check the folders from your shell; the Overview shows what the running service itself sees.

### On Deno, permissions are fixed when the service is installed

Under Deno, `zaafpulse service install` and `zaafpulse register-mcp` write the permissions of the process they set up into the service file and into each AI client's MCP entry: the folders it may write (the data folder, the temp folder, the skill folders and the AI clients' config folders, found from `CLAUDE_CONFIG_DIR` and the other variables), the programs it may run (the `deno.json` list, the owner model servers approved in Settings, `ZAAFPULSE_CLAUDE_BIN` and where Claude Code, node, brew and the model servers were found) and the `PATH` of the shell you install from. Nothing updates them afterwards. Run `zaafpulse service install` (and `zaafpulse register-mcp` for the MCP entries) again after you:

- approve a model server in Settings, or change one;
- install or move Claude Code, node, Homebrew or a model server;
- change `CLAUDE_CONFIG_DIR`, `ZAAFPULSE_CLAUDE_DIRS`, `CODEX_HOME`, `XDG_CONFIG_HOME` or `ZAAFPULSE_DATA`.

Until then an action outside the old grant fails with "Requires ... access to ..." in the dashboard or the log. The service gets the install-time `PATH` (only absolute folders without a comma, a double quote or a control character, each once, then the usual system folders), because launchd and systemd start a service with a minimal one, and Deno grants a program name only when it finds it on `PATH` when it starts; the single-file binary looks its built-in program names up the same way, on the `PATH` it starts with.

- **Linux, `--system`:** the install runs as root, so the skill and AI client folders in the grant are root's (`/root/.claude/skills` and so on), not yours; the service itself runs as the `zaafpulse` user with its data in `/var/lib/zaafpulse`. Install the skills and register the MCP clients from your own account.
- **Windows:** the Startup launcher runs one `cmd.exe` line, which cannot carry a `%` or a `"` safely. If a folder in the launch (your user folder, the data folder, the app folder) holds a `%`, `service install` stops with an error that names it, before anything is written; move the data folder (`--data`), or start ZAAFPulse yourself.
- **A comma, a double quote or a control character** in any folder of the grant stops the install with an error that names it (a comma would split one grant into two).

## What works where

| | macOS | Linux | Windows |
|---|---|---|---|
| Claude Code and Codex usage, cost and quality | ✓ | ✓ | ✓ |
| Live monitor (running AI apps, CPU, memory) | ✓ `ps`, `vm_stat` | ✓ `/proc` | ✓ CIM |
| Second brain: Obsidian vault discovery | ✓ | ✓ (also Flatpak and Snap) | ✓ |
| Agent Gateway, agents, workflows, RAG, MCP, A2A | ✓ | ✓ | ✓ |
| Register the MCP server in Claude Code, Kiro, Antigravity | ✓ | ✓ | ✓ |
| Code tools setup | ✓ | ✓ | ✓ |
| Background service | LaunchAgent | systemd (user or system) | Startup folder, or Task Scheduler as SYSTEM |

On a server, the usage pages show the Claude Code and Codex logs of the user ZAAFPulse runs as. A system service has none, so it serves mainly as the agent gateway and agentic workspace.

# Release checklist: v2.0.0

For the coordinator and the owner. The steps in order, with the exact commands for `rifqifitriady/ZAAFPulse-deno-go`.
Once the tag ruleset and immutable releases are on, **a pushed `v2.0.0` tag can never be moved or deleted, and a
published v2.0.0 can never have its files changed** (only its title and notes). Everything has to be right before
step 5.

Set once in the shell used for all steps:

```bash
R=rifqifitriady/ZAAFPulse-deno-go
```

## 1. Merge to `main`

The release branch (`migrate/release-2-0-0`) goes into `migrate/deno-go`, and `migrate/deno-go` into `main`, through
pull requests. Then check what `main` holds:

```bash
git fetch origin
C="$(git rev-parse origin/main)"; echo "$C"
git show origin/main:package.json | jq -r .version                    # 2.0.0
git cat-file -e origin/main:docs/release-notes/v2.0.0.md && echo notes # notes
git show origin/main:.github/workflows/release.yml | grep -c 'skip-darwin-x64: true'   # 1
gh run list -R "$R" --branch main --workflow ci.yml -L 1 --json headSha,conclusion,url
```

The last line must show `headSha` = `$C` and `conclusion` `success`. Do not tag a commit whose CI did not pass.

## 2. Tag ruleset: `v*` tags can never be moved or deleted

Needs admin rights on the repository. Check first that there is none:

```bash
gh api repos/$R/rulesets          # [] before this step
```

```bash
gh api --method POST repos/$R/rulesets --input - <<'JSON'
{
  "name": "Release tags are permanent",
  "target": "tag",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": { "include": ["refs/tags/v*"], "exclude": [] }
  },
  "rules": [
    { "type": "update" },
    { "type": "deletion" },
    { "type": "non_fast_forward" }
  ]
}
JSON
```

Verify (replace `<ID>` with the id it printed):

```bash
gh api repos/$R/rulesets --jq '.[] | select(.target == "tag") | {id, name, enforcement}'
gh api repos/$R/rulesets/<ID> \
  --jq '{target, enforcement, include: .conditions.ref_name.include, rules: [.rules[].type], bypass: .bypass_actors}'
# {"bypass":[],"enforcement":"active","include":["refs/tags/v*"],"rules":["update","deletion","non_fast_forward"],"target":"tag"}
```

Do not test the rule with a `v*` tag: it could never be deleted again.

## 3. Immutable releases

```bash
gh api --method PUT repos/$R/immutable-releases       # HTTP 204, no output
gh api repos/$R/immutable-releases                    # {"enabled":true,"enforced_by_owner":false}
```

Background and the undo commands: [docs/release-hardening.md](release-hardening.md).

## 4. Recommended: one dry run on `main` (needs the owner's OK under the CI policy)

The install tests of `install.ps1`, the Windows install paths and the macOS launchd path have never run in CI. A dry
run builds and install-tests exactly what the tag would, publishes nothing, attests nothing and creates no tag; a
failure there costs nothing, while a failure after the tag leaves `v2.0.0` pinned to that commit (step 7).

```bash
gh workflow run release.yml -R "$R" --ref main -f tests=false
gh run list -R "$R" --workflow release.yml -L 1 --json databaseId,headSha,status,url
gh run watch <run-id> -R "$R" --exit-status
```

It must end green with the artifact `release-dry-run` (five archives, `install.sh`, `install.ps1`, `SHA256SUMS.txt`;
no `darwin-x64`):

```bash
gh run download <run-id> -R "$R" -n release-dry-run -D "$(mktemp -d)/dry"
```

## 5. Tag

Tag the commit checked in step 1, and push only that tag:

```bash
git tag v2.0.0 "$C"
git push origin v2.0.0
```

## 6. Watch `release.yml`

```bash
gh run list -R "$R" --workflow release.yml -L 1 --json databaseId,headSha,event,status,url
gh run watch <run-id> -R "$R" --exit-status
```

The jobs, in order: `tag matches package.json, notes exist` (`version`), `test` (ci.yml's suites), `build` (five
packages), `install-test` (five legs), `publish`. `publish` runs only when all of them passed.

## 7. A failure before `publish`

Nothing is published and nothing is attested, but the tag stays: the ruleset forbids moving or deleting it.

- **A transient failure** (a runner, a download, a flaky test that passes alone): re-run the failed jobs on the same
  commit; `publish` runs once they pass.

  ```bash
  gh run rerun <run-id> -R "$R" --failed
  ```

- **A failure that needs a code change:** `v2.0.0` cannot point at the fix. Ship `v2.0.1`: set `2.0.1` in
  `package.json` and `package-lock.json`, add `docs/release-notes/v2.0.1.md` (saying why there is no v2.0.0 release),
  merge to `main`, and go on from step 5 with `v2.0.1`. Do not delete the ruleset to move the tag.
- **`publish` itself failed after its upload began:** `gh release create` uploads into a draft and publishes last.
  Check with `gh release list -R "$R"`; a **draft** (never a published release) may be deleted with
  `gh release delete v2.0.0 -R "$R" --yes` (without `--cleanup-tag`), then `gh run rerun <run-id> -R "$R" --failed`.

## 8. Verify the release

```bash
gh release view v2.0.0 -R "$R"
gh api repos/$R/releases/tags/v2.0.0 --jq '{tag_name, immutable, draft, prerelease, assets: [.assets[].name]}'
# immutable true, draft false; 8 assets: the five archives, install.sh, install.ps1, SHA256SUMS.txt; no darwin-x64
gh release verify v2.0.0 -R "$R"

D="$(mktemp -d)" && cd "$D"
gh release download v2.0.0 -R "$R"
shasum -a 256 -c SHA256SUMS.txt                     # every line OK (sha256sum -c on Linux)
for f in zaafpulse-2.0.0-* install.sh install.ps1 SHA256SUMS.txt; do
  gh attestation verify "$f" --repo "$R" --signer-workflow "$R/.github/workflows/release.yml" \
    --source-ref refs/tags/v2.0.0 || echo "FAIL $f"
  gh release verify-asset v2.0.0 "$f" -R "$R" || echo "FAIL $f"
done
```

Each attestation must name `.github/workflows/release.yml@refs/tags/v2.0.0`. The binaries inside the archives are
attested too:

```bash
for a in zaafpulse-2.0.0-*.tar.gz; do tar -xzf "$a"; done
for a in zaafpulse-2.0.0-*.zip; do unzip -q "$a"; done
for b in zaafpulse-2.0.0-*/zaafpulse zaafpulse-2.0.0-*/zaafpulse-gw zaafpulse-2.0.0-*/*.exe; do
  gh attestation verify "$b" --repo "$R" || echo "FAIL $b"
done
```

## 9. Install on a test prefix, no service

From the published installer, into throwaway folders: `HOME` is set too, because the installer writes its
`zaafpulse` command to `$HOME/.local/bin`, and nothing may touch a real install, port 4777 or a running service.

```bash
T="$(mktemp -d)"; mkdir "$T/home"
curl -fsSL https://github.com/$R/releases/download/v2.0.0/install.sh -o "$T/install.sh"
HOME="$T/home" ZAAFPULSE_PREFIX="$T/prefix" sh "$T/install.sh" \
  --version v2.0.0 --no-service --no-modify-path --verify-attestation
HOME="$T/home" "$T/home/.local/bin/zaafpulse" version              # 2.0.0
HOME="$T/home" "$T/home/.local/bin/zaafpulse" doctor               # Gateway (zaafpulse-gw) ... found, allowed, 2.0.0
"$T/prefix/bin/zaafpulse-gw" -version                               # 2.0.0
rm -rf "$T"
```

`doctor` reports the server as not answering and no service: that is expected with `--no-service`. On an Intel Mac,
the same `sh "$T/install.sh"` must stop with "no v2 build for Intel Macs (darwin-x64)" and the v1.x command.

## 10. The live upgrade and the rollback

Use a test user account or a VM first, not the owner's live install: the launchd label and the port are shared. The
owner's own dashboard (a source checkout run by launchd as `com.zaaf.aipulse.server` on port 4777) is not an
installer-made install; moving it is the owner's separate decision, and nothing in this checklist touches it.

**macOS (Apple silicon) and Linux:**

```bash
# 1. A v1.1.0 install to upgrade from (on a machine with nothing installed)
curl -fsSL https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.sh | sh -s -- --version v1.1.0
# 2. Open http://localhost:4777 once (zaafpulse open), so there is data
# 3. Upgrade
curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/download/v2.0.0/install.sh | sh
zaafpulse doctor
cat ~/.local/share/zaafpulse/rollback/VERSION       # v1.1.0 (/opt/zaafpulse/rollback/VERSION as root)
# 4. Roll back, and check that v1.1.0 serves the same data
curl -fsSL https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/download/v2.0.0/install.sh | sh -s -- --rollback
zaafpulse register-mcp <client>                     # v1's command, for each AI client you use
```

**Windows (PowerShell):**

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/rifqifitriady/ZAAFPulse/v1.1.0/install.ps1))) -Version v1.1.0
# open the dashboard once, then:
& ([scriptblock]::Create((irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/download/v2.0.0/install.ps1))) -Version v2.0.0
zaafpulse doctor
Get-Content $env:LOCALAPPDATA\ZAAFPulse\rollback\VERSION     # v1.1.0
& ([scriptblock]::Create((irm https://github.com/rifqifitriady/ZAAFPulse-deno-go/releases/download/v2.0.0/install.ps1))) -Rollback
```

To leave v2 installed but take the Go gateway out of the path, no rollback is needed:
`"gateway": { "engine": "js" }` and `"egress": { "lockdown": false }` in `config.json`, then
`zaafpulse service restart` ([switch runbook §5](migration/2026-11-23-switch-runbook.md#5-rolling-back)).

## 11. After the release

- Record the results of steps 8 to 10 in `docs/migration/2026-11-23-p6-results.md` (the public-release leg) and in
  the program plan's P6 exit.
- The final quiet-machine benchmarks (owner decision 10): `deno task bench:final` on a quiet machine
  ([docs/migration/final-benchmarks.md](migration/final-benchmarks.md)), then publish the M3, M4, M5, M6 and M7 rows.
  The notes of an immutable release can still be edited, so they can be added to v2.0.0's "Verification status" with
  `gh release edit v2.0.0 -R "$R" --notes-file docs/release-notes/v2.0.0.md` after the file is updated on `main`.

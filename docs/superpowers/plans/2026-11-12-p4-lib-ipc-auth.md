# P4 Stage 1 Stream C (IPC entry conditions and provider auth) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Meet the P4f entry conditions (outbox `Close` under the send lock and idempotent; marks re-read at each new
session; a decided store-before-mark durability order) and add the P4g provider-auth library `gw/provauth` (OAuth2
`client_credentials` / `refresh_token`, Google service-account RS256 JWT, a shared token cache), proven equal to the JS
reference by generated fixtures.

**Architecture:** `gw/ipc/outbox.go` gains a `closed` state: `Close` takes `sendMu` then `mu`, and every call after it
(`Append`, `AppendSend`, `Replay`, `Ack`, a second `Close`) returns without touching a file. On the JS side,
`recoverMarks` returns its Map with a non-enumerable `reread()` that runs the same recovery again; `createIngest` keeps
that function and calls it in `seed()`, which the supervisor already calls once per session (on `hello`), so a new
session raises every in-memory mark to what the stores and the mark file hold now. `createMarkFile().save` merges the
file on disk before it writes, so two servers never lower each other's marks. No wiring file changes (`server.js`,
`lib/ipc/supervisor.js`, `lib/ipc/session.js` stay as they are). `gw/provauth` is a new standard-library package with
no import from any other package of the module; `scripts/fixtures/gen-provauth.js` runs the real
`lib/gateway/providers.js` `accessToken` (fake `fetch`, fixed clock, fixed test key) and writes
`gw/testdata/provauth/<case>.json`, which `gw/provauth/fixtures_test.go` replays and `test/provauth-fixtures.test.js`
regenerates and compares (the drift check, on both runtimes).

**Tech Stack:** Go 1.27.3 standard library only (`crypto/rsa`, `crypto/sha256`, `crypto/x509`, `encoding/pem`,
`net/http`, `net/http/httptest`); Node 24 and Deno 2.9.8, CommonJS, `node:test`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-11-12-p4-stage1-design.md`, sections "Equivalence by generated fixtures", "Stream
  C interfaces" and "Rules for all three streams" (binding). The `deno` and `migrate-to-deno` skills bind all Deno work;
  the `go-stack-defaults` skill binds Go work, with the P0 decision on top: **Go is standard library only**.
- Scope (spec table, stream C): `gw/ipc/` (outbox only), `lib/ipc/` (marks only: `marks.js`, `markfile.js`, and the
  marks table of `ingest.js`), `gw/provauth/` (new), `scripts/fixtures/gen-provauth.js` (new), `gw/testdata/provauth/`
  (new), new test files. **Not touched:** `server.js`, `lib/ipc/supervisor.js`, `lib/ipc/session.js`, `gw/cmd/`,
  `gw/front/`, `gw/engine/`, `lib/gateway/providers.js`, `.github/workflows/ci.yml`. A needed change there: stop and
  report.
- Every task ends green on, with `export GOTOOLCHAIN=local ZAAFPULSE_DATA="$(mktemp -d)" HOME="$(mktemp -d)"` first:
  `go -C gw vet ./...`, `GOOS=windows go -C gw vet ./...`, `go -C gw test -race -count=1 ./...`,
  `test -z "$(gofmt -l gw)"`, and the task's JS tests on Node (`node --test <files>`) and Deno
  (`deno test -q --no-check --no-prompt -P=test <files>`). The full Node and Deno suites run once, in Task 7.
- The P3/P4a exactly-once tests stay green on both runtimes after Tasks 1–3: `test/ipc-*.test.js` and
  `test/front-kill.test.js` (they build the Go stub; Go must be on PATH).
- Prettier: `npx --yes prettier@3 --write <file>` only on files this plan creates or on files `--check` reports clean
  before the edit (measured clean at 063d39f: `lib/ipc/marks.js`, `lib/ipc/markfile.js`, `lib/ipc/ingest.js`,
  `test/ipc-hardening.test.js`). `*.json` and `*.md` are in `.prettierignore`.
- Commits: one plain English sentence, like the repo history; **no** `Co-Authored-By`, no AI attribution. Never commit
  `.superpowers/`. Each task ends with its commit and `git push origin migrate/p4-lib-ipc-auth`. No other branch is
  pushed or created.
- Tests never reach the network (httptest on 127.0.0.1 only), never touch ports 4777 or 4787, and stop every process
  they start before removing its folder, in one `t.after` hook (`test/ipc-stub.js tmpDir(t, name, stops)`).
- Full matrix once, after Task 7: `gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref migrate/p4-lib-ipc-auth`.

## Decisions

- **D1 Outbox after `Close`.** `Close` is the last writer: it takes `sendMu` (waits for a send in progress, so the
  exit-3 emitter cannot append after it), then `mu`, marks the outbox closed, closes the segment, writes `stream.json`
  only while it still holds the lock, and releases the lock. Afterwards `Append`, `AppendSend`, `Replay` and `Ack`
  return `ErrOutboxClosed` and touch no file; a second `Close` returns nil and writes nothing (T1-a, T1-b).
- **D2 Marks per session.** The re-read is triggered by the call the supervisor already makes once per session
  (`ingest.seed(stream, outboxFrom - 1)` on `hello`), and the recovery function travels with the marks Map that
  `server.js` already passes (`marks: recoverMarks(files)`): no wiring file changes. A plain Map (tests, the in-process
  paths) keeps today's behaviour. A re-read only raises marks (the larger of memory, the stores' tails and the mark
  file); a re-read that throws is logged once and the session goes on with the marks in memory (they are never ahead
  of the stores, only possibly behind, and the outbox's `Acked` seed still applies).
- **D3 Mark file shared by two servers.** `save` reads the file, keeps the larger mark per stream, then writes: a
  server never lowers a mark another server wrote.
- **D4 Power loss: documented, no fsync (spec: "the plan decides and says which").** The durability target since P3 D4
  is a killed process, not a power loss, and nothing in the chain is fsynced (Go outbox segments and `stream.json`,
  the four JS stores, the mark file; the `js` engine's own stores too). After a kill of either process the page cache
  holds every completed write, so "store, then mark, then ack" holds (proven by `test/ipc-kill.test.js`,
  `test/front-kill.test.js`, `test/ipc-hardening.test.js`). After a power loss, fsyncing only the stores before the
  mark would close one of three holes: the outbox can still lose events that were sent but not stored, and
  `stream.json` / the segments can roll back so that new events reuse seqs that are already marked (they are then
  skipped as duplicates). Closing all three needs an fsync on the gateway's request path (every outbox append) and on
  every ack: a program-level cost decision, not a P4f one. Every hole loses events of the last moments before the
  power cut, the same window the `js` engine loses; none counts an event twice. The decision is written into
  `lib/ipc/markfile.js` and `docs/migration/2026-11-12-p4f-mark-durability.md`, and is the owner's to reverse.
- **D5 `provauth` API.**
  - `type Source interface { Token(ctx context.Context) (string, time.Time, error) }`
  - `type Secret func() (string, error)`: read at every refresh, as `providers.js` resolves the secret at every
    refresh; the wiring reads the latest `secrets.handoff`. Never the environment.
  - `type Clock func() time.Time` (nil: `time.Now`).
  - `OAuth2(cfg OAuth2Config, secret Secret, hc *http.Client, clock Clock, opts ...Option) *Cached`
  - `GoogleServiceAccount(saJSON Secret, scope string, hc *http.Client, clock Clock, opts ...Option) *Cached`
  - `WithID(id string)` (the message prefix, `providers.js` uses the provider id), `WithMask(func(string) string)`,
    `MaskValues(values []string) func(string) string` (the handoff's `mask` list, replaced by `***` as
    `lib/secrets.js mask` does).
  - `(*Cached).Token(ctx)` (cached while more than `Lead` is left) and `(*Cached).Refresh(ctx)` (forced, for the replay
    after a 401 and the background refresh, as `accessToken(p, true)`). Concurrent callers share one refresh; a
    caller's context ends only its own wait, never the shared refresh, which has its own 20 s timeout.
  - `hc` is copied with `CheckRedirect` set to `http.ErrUseLastResponse`: a redirect is refused, never followed (the
    body holds a client secret or a signed assertion).
- **D6 Fixtures run the real JS.** The generator calls `createProviders({ getConfig, secrets, fetchImpl, now })` and
  its exported `accessToken(p, force)`, with a fake `fetchImpl` that records each request and answers from a queue,
  and reads the expiry from `quota()`. `providers.js` is not changed. Each file is
  `{ "fn": "accessToken", "in": { id, auth, secret, mask, responses, steps }, "out": { requests, results } }`; a step
  that throws has `{ "throws": "<message>" }` in `results`. The Go test replays `in` through a recording
  `http.RoundTripper` and compares `out` after canonical JSON encoding (`json.Marshal` of the decoded value: sorted
  keys).

## Spec deviations

1. **Token lead is 5 minutes, not 60 s.** The spec says "cached until 60 s before expiry"; `providers.js` uses
   `TOKEN_LEAD_MS = 5 * 60 * 1000`, and the JS is the reference until G3 (spec, "Equivalence"). `provauth.Lead` is 5
   minutes, and the fixtures pin the boundary.
2. **The regenerate-and-diff check is a test file, not a new CI step.** `.github/workflows/ci.yml` belongs to no
   stream, and stream B needs the same check: two streams editing one workflow file would collide. The check is
   `test/provauth-fixtures.test.js`, which the existing Node and Deno CI steps run (`test/*.test.js`) on every OS;
   the Go replay runs in the existing go job.
3. **Constructor signatures take options and a `Secret` function.** The spec's `OAuth2(cfg, secret, httpClient,
   clock)` and `GoogleServiceAccount(saJSON, scope, httpClient, clock)` keep their four leading parameters; `secret`
   and `saJSON` are functions (read at each refresh, D5), and the provider id and the mask set come as options.
   `(*Cached).Refresh` is added for the forced refresh the JS has.
4. **Fixture shape.** One file runs a sequence of calls (cache hits, the lead boundary, refresh-token rotation), so
   `out` holds the recorded requests and one result per step, and `throws` sits per step inside `results` instead of
   at the top level.
5. **Inputs the Go port refuses where the JS has a quirk** (none can come from `cleanProvider` or a real service
   account; each is a Go unit test, none is in the corpus):
   - a stored secret that is JSON `null`: JS throws a `TypeError` text, Go reads it as no fields;
   - non-string `client_secret`, `refresh_token`, `access_token`, `error`, `client_email` or `token_uri` values: JS
     converts them with `String()`, Go refuses them (`access_token`) or leaves them out (`error`), or fails with
     "the service account's <field> is not a string";
   - a service-account credential that is valid JSON but not an object: JS fails inside `crypto.sign` with a runtime
     text, Go with "the credential is not a service account JSON";
   - a private key that is not an RSA key in PEM: JS fails with the runtime's crypto text, Go with "the service
     account's private_key is not an RSA private key in PEM";
   - a token body that is JSON `null`: JS throws a `TypeError` text, Go reports `token refresh failed (<status>)`;
   - an `expires_in` that is not a whole number of milliseconds or exceeds about 31 years: Go rounds to the
     nanosecond / caps at 1e9 s.
   Network errors keep each runtime's own text (as `fetch` does in Node versus Deno).

## File structure

| File | Responsibility |
|---|---|
| `gw/ipc/outbox.go` (modify) | `closed` state, `ErrOutboxClosed`, `Close` under `sendMu` |
| `gw/ipc/outbox_close_test.go` (create) | the `Close` tests |
| `lib/ipc/marks.js` (modify) | `recoverMarks` returns a Map with `reread()` |
| `lib/ipc/markfile.js` (modify) | `save` merges the disk file; the D4 durability note in the header |
| `lib/ipc/ingest.js` (modify) | `seed` re-reads through `marks.reread` |
| `test/ipc-marks-session.test.js` (create) | the marks-per-session and shared-mark-file tests |
| `docs/migration/2026-11-12-p4f-mark-durability.md` (create) | D4 |
| `gw/provauth/provauth.go` (create) | `Source`, `Secret`, `Clock`, options, `Cached` (cache, single flight, the POST) |
| `gw/provauth/js.go` (create) | JS-compatible helpers: form encoding, `JSON.stringify` strings, `Number()` |
| `gw/provauth/oauth2.go` (create) | `OAuth2Config`, `OAuth2` |
| `gw/provauth/google.go` (create) | `GoogleServiceAccount`, the RS256 JWT |
| `gw/provauth/*_test.go` (create) | httptest and unit tests; `fixtures_test.go` replays the corpus |
| `scripts/fixtures/gen-provauth.js` (create) | the generator (`generate()` and a CLI that writes the files) |
| `gw/testdata/provauth/*.json` (create) | the generated corpus |
| `test/provauth-fixtures.test.js` (create) | regenerate and compare (drift check) |

---

### Task 1: Outbox `Close` under the send lock, idempotent

**Files:**
- Modify: `gw/ipc/outbox.go` (struct, errors, `append`, `Replay`, `Ack`, `Close`)
- Create: `gw/ipc/outbox_close_test.go`

**Interfaces:**
- Produces: `ipc.ErrOutboxClosed`; `(*Outbox).Close() error` safe to call twice and concurrently with `AppendSend`.

- [ ] **Step 1: Write the failing tests** (`gw/ipc/outbox_close_test.go`)

```go
package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// T1-b: a second Close must not write stream.json: by then another gateway may own the outbox, and the old state
// would roll its ack back.
func TestOutboxSecondCloseWritesNothing(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 3)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := openOutbox(t, dir) // the lock is free again
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Ack(3); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if err := o.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if string(before) != string(after) {
		t.Fatalf("the second Close rewrote stream.json: %s -> %s", before, after)
	}
}

// T1-a: after Close nothing is appended, sent, replayed or acknowledged, and no file is created.
func TestOutboxRefusesEverythingAfterClose(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 2)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	entries := func() int { e, _ := os.ReadDir(dir); return len(e) }
	n := entries()
	sent := false
	typ, body := Synthetic(3, DefaultEmitBase)
	if _, err := o.AppendSend(typ, body, 1, func([]byte) error { sent = true; return nil }); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("AppendSend after Close: %v", err)
	}
	if _, _, err := o.Append(typ, body, 1); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Append after Close: %v", err)
	}
	if err := o.Replay(1, func([]byte) error { sent = true; return nil }); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Replay after Close: %v", err)
	}
	if err := o.Ack(2); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("Ack after Close: %v", err)
	}
	if sent || entries() != n {
		t.Fatalf("sent %v, files %d -> %d", sent, n, entries())
	}
}

// T1-a: Close waits for a send in progress (it takes the send lock), so the event it stored is in stream.json's next.
func TestOutboxCloseWaitsForASendInProgress(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	inSend, release := make(chan struct{}), make(chan struct{})
	go func() {
		typ, body := Synthetic(1, DefaultEmitBase)
		_, _ = o.AppendSend(typ, body, 1, func([]byte) error { close(inSend); <-release; return nil })
	}()
	<-inSend
	closed := make(chan error, 1)
	go func() { closed <- o.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during a send: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	again, err := openOutbox(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Next() != 2 {
		t.Fatalf("next %d after reopen, want 2", again.Next())
	}
}

// A killed outbox (the test helper drops the lock without writing) is not written by a later Close either.
func TestOutboxCloseAfterKillWritesNothing(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, o, 1)
	before, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	kill(o)
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stream.json"))
	if string(before) != string(after) {
		t.Fatalf("Close without the lock wrote stream.json")
	}
}
```

- [ ] **Step 2: Run, expect failures**: `go -C gw test -race -count=1 -run 'TestOutbox(SecondClose|Refuses|CloseWaits|CloseAfterKill)' ./ipc/`
  → `ErrOutboxClosed` undefined (compile error).

- [ ] **Step 3: Implement** in `gw/ipc/outbox.go`:

```go
	closed     bool                                // Close ran: nothing is written or sent any more
```
(in the struct, under `mu`), the error

```go
	// ErrOutboxClosed: Close ran. Nothing is appended, sent, replayed or acknowledged after it, and no file is written.
	ErrOutboxClosed = errors.New("outbox: closed")
```

`append` and `Ack` return `ErrOutboxClosed` first thing under `mu` when `o.closed`; `Replay` checks it under `mu`
right after taking `sendMu`; and `Close` becomes:

```go
// Close writes stream.json, closes the open segment and releases the lock, under the send lock: a send in progress
// finishes first, and nothing is appended or sent after it (P4f entry 4, T1-a). A second Close does nothing (T1-b):
// stream.json is written only while this outbox holds the lock.
func (o *Outbox) Close() error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	var err error
	if o.lock != nil {
		err = o.save()
	}
	o.release()
	return err
}
```

Update the type comment's last paragraph: "Close is the last writer (P4f): it waits for a send in progress, and every call
after it returns ErrOutboxClosed."

- [ ] **Step 4: Run**: the four tests pass; then `go -C gw test -race -count=1 ./...`, `go -C gw vet ./...`,
  `GOOS=windows go -C gw vet ./...`, `gofmt -l gw` (empty).
- [ ] **Step 5: Rerun the exactly-once tests** on Node and Deno: `test/ipc-*.test.js test/front-kill.test.js`.
- [ ] **Step 6: Commit and push**: `git commit -m "Outbox Close takes the send lock and is safe to call twice (P4f entry 4, T1-a and T1-b)"`.

### Task 2: Marks re-read at each new session; the mark file never lowered

**Files:**
- Modify: `lib/ipc/marks.js` (`recoverMarks`), `lib/ipc/ingest.js` (`seed`, header), `lib/ipc/markfile.js` (`save`)
- Create: `test/ipc-marks-session.test.js`

**Interfaces:**
- Consumes: `createIngest`, `openLocalStores`, `synthetic`, `tmpDir` (existing).
- Produces: `recoverMarks(files, bytes?)` returns `Map<string, number>` with a non-enumerable `reread(): Map`;
  `ingest.seed(stream, upTo)` raises every mark to the re-read value first when the marks came with `reread`.

- [ ] **Step 1: Write the failing tests** (`test/ipc-marks-session.test.js`)

```js
// P4f entry 4: the marks are read again when a new session starts, so a second server on the same data folder cannot
// make this one store again what that one stored; and a server never lowers a mark another one wrote to the mark file.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('path');
const { createIngest } = require('../lib/ipc/ingest');
const { recoverMarks } = require('../lib/ipc/marks');
const { createMarkFile, readMarkFile } = require('../lib/ipc/markfile');
const { openLocalStores } = require('../lib/ipc/local');
const { synthetic, tmpDir } = require('./ipc-stub');

const S = '0f1e2d3c4b5a69788796a5b4c3d2e1f0';
const T = 'f'.repeat(32);
const usage = (seq) => ({ v: 1, type: 'usage.report', seq, ts: 0, body: synthetic(1)[1] });
const lines = (file) => require('fs').readFileSync(file, 'utf8').split('\n').filter(Boolean);

test('a new session reads the marks again: what another server stored meanwhile is a duplicate here', (t) => {
  const root = tmpDir(t, 'reread');
  const a = openLocalStores({ root });
  const first = createIngest({ ...a.sinks, marks: recoverMarks(a.files), log: () => {} });
  first.seed(S, 0);
  for (let seq = 1; seq <= 3; seq++) assert.equal(first.handle(usage(seq), S), true);
  // A second server on the same folder stores 4 and 5 of the same stream.
  const b = openLocalStores({ root });
  const second = createIngest({ ...b.sinks, marks: recoverMarks(b.files), log: () => {} });
  second.seed(S, 3);
  for (let seq = 4; seq <= 5; seq++) assert.equal(second.handle(usage(seq), S), true);
  assert.equal(first.mark(S), 3, 'memory alone is stale');
  first.seed(S, 0); // the next session's hello (the outbox was not acknowledged past 0)
  assert.equal(first.mark(S), 5);
  assert.equal(first.handle(usage(4), S), false, 'a duplicate, not stored again');
  assert.equal(lines(a.files.usageFile).length, 5);
});

test('the re-read includes the mark file; it only raises marks', (t) => {
  const root = tmpDir(t, 'rereadfile');
  const s = openLocalStores({ root });
  const ingest = createIngest({ ...s.sinks, marks: recoverMarks(s.files), log: () => {} });
  ingest.seed(T, 9);
  createMarkFile(s.files.marksFile).save(S, 7);
  ingest.seed(T, 0);
  assert.equal(ingest.mark(S), 7);
  assert.equal(ingest.mark(T), 9, 'never lowered');
});

test('a re-read that fails is logged once and the seed still applies', () => {
  const marks = new Map([[S, 2]]);
  Object.defineProperty(marks, 'reread', {
    value: () => {
      throw new Error('EIO');
    },
  });
  const logged = [];
  const sinks = { recordUsage: () => {}, recordGovernance: () => {}, recordMcp: () => {}, writeSpans: () => {} };
  const ingest = createIngest({ ...sinks, marks, log: (l) => logged.push(l) });
  ingest.seed(S, 4);
  ingest.seed(S, 5);
  assert.equal(ingest.mark(S), 5);
  assert.equal(logged.length, 1);
  assert.match(logged[0], /EIO/);
});

test('recoverMarks: reread is not an entry and runs the same recovery again', (t) => {
  const root = tmpDir(t, 'rr');
  const s = openLocalStores({ root });
  const marks = recoverMarks(s.files);
  assert.deepEqual([...marks], []);
  assert.deepEqual(Object.keys(marks), []);
  createMarkFile(s.files.marksFile).save(S, 3);
  assert.deepEqual([...marks.reread()], [[S, 3]]);
  assert.equal(typeof marks.reread().reread, 'function');
});

test('the mark file: a server never lowers or drops a mark another server wrote (D3)', (t) => {
  const file = path.join(tmpDir(t, 'shared'), '.data', 'ipc-marks.json');
  const one = createMarkFile(file);
  const two = createMarkFile(file); // read before one wrote anything
  one.save(S, 5);
  two.save(T, 2);
  assert.deepEqual(Object.fromEntries(readMarkFile(file)), { [S]: 5, [T]: 2 });
  two.save(S, 3);
  assert.equal(readMarkFile(file).get(S), 5);
  assert.deepEqual(Object.fromEntries(two.get()), { [S]: 5, [T]: 2 });
});
```

- [ ] **Step 2: Run on Node, expect failures**: `node --test test/ipc-marks-session.test.js` → tests 1, 2, 4, 5 fail
  (`first.mark(S)` 3, `reread` not a function, `S` dropped from the file).

- [ ] **Step 3: Implement.** `lib/ipc/marks.js`:

```js
function recoverMarks(files, bytes = TAIL_BYTES) {
  const { usageFile, governanceDir, mcpAuditFile, traceDir, marksFile } = files;
  const marks = marksFile ? require('./markfile').readMarkFile(marksFile) : new Map();
  // ... the tails, as before ...
  for (const f of list) marksIn(tailLines(f, bytes), marks);
  // The same recovery again, for the ingest at each new session (P4f entry 4): not an entry of the Map.
  Object.defineProperty(marks, 'reread', { value: () => recoverMarks(files, bytes) });
  return marks;
}
```

`lib/ipc/ingest.js` (header paragraph and `seed`):

```js
  const reread = typeof marks.reread === 'function' ? marks.reread : null;
  // ...
  function raise(stream, upTo) {
    if (!Number.isSafeInteger(upTo) || upTo <= (hwm.get(stream) || 0)) return;
    hwm.set(stream, upTo);
    const waiting = unstored.get(stream);
    if (waiting !== undefined && waiting <= upTo) unstored.delete(stream);
  }
  /**
   * A new session (the supervisor calls this on hello): the marks are read again from the stores and the mark file
   * when they came from recoverMarks (another server on the same folder may have stored more since), then the mark of
   * `stream` is raised to at least `upTo` (seqs the gateway says were acknowledged, so stored). Marks only go up.
   */
  function seed(stream, upTo) {
    if (reread) {
      try {
        for (const [s, m] of reread()) raise(s, m);
      } catch (err) {
        once(`reread ${causeOf(err)}`, `ipc ingest: the marks could not be read again for the new session, so the marks in memory are used: ${err && err.message}`);
      }
    }
    raise(stream, upTo);
  }
```

`lib/ipc/markfile.js` `save`:

```js
    save(stream, upTo) {
      // Another server on the same folder may have written since: keep the larger mark of every stream (D3).
      const merged = new Map(marks);
      for (const [s, m] of readMarkFile(file)) if (m > (merged.get(s) || 0)) merged.set(s, m);
      marks = merged;
      if (!(upTo > (merged.get(stream) || 0))) return;
      const next = new Map(merged).set(stream, upTo);
      fs.mkdirSync(path.dirname(file), { recursive: true });
      const tmp = `${file}.tmp`;
      fs.writeFileSync(tmp, `${JSON.stringify(Object.fromEntries(next))}\n`, { mode: 0o600 });
      fs.renameSync(tmp, file); // replaces the old file on Windows too
      marks = next; // only once it is on disk: a failed write leaves memory at what the file holds
    },
```

- [ ] **Step 4: Run** `node --test test/ipc-marks-session.test.js test/ipc-hardening.test.js test/ipc-ingest.test.js`
  and the same under `deno test -q --no-check --no-prompt -P=test`; prettier `--write` the four JS files.
- [ ] **Step 5: Rerun** `test/ipc-*.test.js test/front-kill.test.js` on both runtimes.
- [ ] **Step 6: Commit and push**: `git commit -m "Read the IPC marks again at each new session and never lower another server's mark (P4f entry 4)"`.

### Task 3: The store-before-mark order on power loss, decided and documented

**Files:**
- Modify: `lib/ipc/markfile.js` (header comment)
- Create: `docs/migration/2026-11-12-p4f-mark-durability.md`

- [ ] **Step 1:** Write D4 (above) into the decision record, with: the target (P3 D4), what is and is not fsynced
  (by file), the kill case and its tests, the three power-loss holes and why fsyncing the stores alone closes one,
  the consequence (at-most-once for the last moments, never twice), and what reversing it would take (fsync on outbox
  append, `stream.json` save, every store append on the IPC path, the mark file; one owner decision).
- [ ] **Step 2:** Replace the header sentence of `lib/ipc/markfile.js` that ends "...so it never runs ahead of the
  stores" with the same decision in three lines and a pointer to the record.
- [ ] **Step 3:** `npx --yes prettier@3 --check lib/ipc/markfile.js`; `node --test test/ipc-hardening.test.js`.
- [ ] **Step 4: Commit and push**: `git commit -m "Decide and document the mark file's power-loss order: no fsync, at most once for the last moments (P4f entry 4)"`.

### Task 4: `provauth`: the token cache and the OAuth2 source

**Files:**
- Create: `gw/provauth/provauth.go`, `gw/provauth/js.go`, `gw/provauth/oauth2.go`
- Test: `gw/provauth/oauth2_test.go`, `gw/provauth/js_test.go`

**Interfaces:**
- Produces (used by Tasks 5 and 6): `Source`, `Secret`, `Clock`, `Option`, `WithID`, `WithMask`, `MaskValues`, `Lead`
  (`5 * time.Minute`), `Cached` with `Token` and `Refresh`, `OAuth2Config{TokenURL, ClientID, Scope, Grant string}`,
  `OAuth2(...)`; package-internal `newCached(form formFunc, hc, clock, opts)`, `type field struct{ name, value string }`,
  `type formFunc func(refresh string, now time.Time) (endpoint string, fields []field, err error)`,
  `type formError string` (a message that gets the id prefix), `encodeForm`, `jsString`, `jsNumber`.

- [ ] **Step 1: Write the failing tests.** `js_test.go`: `encodeForm` against the WHATWG values measured on Node and Deno
  (`{a: "x y~*-._!'()é+&="}` → `a=x+y%7E*-._%21%27%28%29%C3%A9%2B%26%3D`); `jsString("\u2028\u001f<>&é\"\\")` →
  `"\u2028\u001f<>&é\"\\"` with the U+2028 raw and `\u001f` escaped; `jsNumber` over `" 60 "`→60, `"0x10"`→16,
  `"1e3"`→1000, `""`→0, `"60s"`→NaN, `"inf"`→NaN, `"Infinity"`→+Inf, `true`→1, `nil`→NaN, `3.5`→3.5.
  `oauth2_test.go` (httptest, `127.0.0.1` only):
  - client credentials: the POST has `content-type: application/x-www-form-urlencoded`, `accept: application/json`,
    body `grant_type=client_credentials&client_id=app-1&client_secret=s3cret&scope=x%2F.default`; token and expiry
    `now + 3600 s`; a second `Token` 1 s later is a cache hit (one request); at `exp - Lead` it refreshes.
  - refresh token rotation: the second refresh sends the `refresh_token` the first answer returned; an answer without
    one keeps it.
  - concurrent callers: 50 goroutines call `Token` while the endpoint blocks; one request; all get the same token.
  - one caller's context ends: it gets `context.Canceled`, the refresh goes on and the next caller gets its token.
  - a redirect (307 to another httptest server that counts hits): `"<id>: the token endpoint answered with a redirect
    (307); it is not followed"`, the other server is never hit.
  - timeout: the endpoint never answers, `c.timeout = 50 * time.Millisecond`: `"<id>: token endpoint timed out"`.
  - errors: `401 {"error":"invalid_client"}` → `"<id>: token refresh failed (401 invalid_client)"`; masked by
    `MaskValues([]string{"s3cret"})` when the error echoes the secret; no secret → `"<id>: no credential stored for
    oauth2"`; refresh grant without a refresh token → `"<id>: no refresh_token in the stored credential"`; a
    `Secret` error is returned as is; the cache keeps the old token after a failed refresh.
- [ ] **Step 2: Run, expect a compile failure** (`go -C gw test ./provauth/`).
- [ ] **Step 3: Implement** `provauth.go` (cache, single flight with `flight{done chan struct{}; tok token; err error}`,
  `fetch` with `context.WithTimeout(context.Background(), c.timeout)`, redirect check `300 <= status < 400`, body read
  up to 1 MiB, `json.Unmarshal` into `map[string]any` (failure reads as empty), `access_token` a non-empty string and
  status 2xx else `token refresh failed (<status>[ <error>])`, `expires_in` through `jsNumber` with 0 / NaN → 3600,
  capped at 1e9 s, refresh token kept from the previous token when the answer has none, every message through the
  mask), `js.go` (helpers above), `oauth2.go` (the form, in the JS field order `grant_type, client_id, client_secret?,
  scope?, refresh_token?`; the stored secret parsed as JSON object `{client_secret, refresh_token}`, else, when it is
  not JSON at all, as the client secret itself).
- [ ] **Step 4: Run** `go -C gw test -race -count=1 ./provauth/`, vet (also `GOOS=windows`), gofmt.
- [ ] **Step 5: Commit and push**: `git commit -m "Add gw/provauth: a shared token cache and the OAuth2 client-credentials and refresh-token source (P4g)"`.

### Task 5: `provauth`: the Google service account (RS256 JWT)

**Files:**
- Create: `gw/provauth/google.go`, `gw/provauth/google_test.go`

**Interfaces:**
- Consumes: `newCached`, `formFunc`, `formError`, `field`, `jsString` (Task 4).
- Produces: `GoogleServiceAccount(saJSON Secret, scope string, hc *http.Client, clock Clock, opts ...Option) *Cached`;
  internal `signJWT(raw, scope string, now time.Time) (aud, jwt string, err error)`.

- [ ] **Step 1: Write the failing tests** (`google_test.go`, an RSA key generated in the test): the POST to the
  `token_uri` (httptest) carries `grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=<jwt>`;
  the JWT header decodes to exactly `{"alg":"RS256","typ":"JWT"}`, the claims to exactly
  `{"iss":"sa@example.test","scope":"<scope>","aud":"<token_uri>","iat":<t>,"exp":<t+3600>}` with `t` the clock's
  Unix seconds (floor), and the signature verifies with `rsa.VerifyPKCS1v15`; a PKCS#1 key works too; no `token_uri`
  (absent, `null` or `""`) gives `aud` and URL `https://oauth2.googleapis.com/token` (checked on the JWT only, no
  request: `signJWT`); no `client_email` leaves `iss` out, `null` writes `"iss":null`; not JSON → `"<id>: the
  credential is not a service account JSON"`; an EC key → `"<id>: the service account's private_key is not an RSA
  private key in PEM"`; no credential → `"<id>: no credential stored for google-sa"`.
- [ ] **Step 2: Run, expect a compile failure.**
- [ ] **Step 3: Implement** `google.go`: parse into `map[string]json.RawMessage`; `client_email` absent / `null` /
  string (else `formError`); `token_uri` absent / `null` / `""` → default, string → it, else `formError`; `private_key`
  string → `pem.Decode` → `PRIVATE KEY` (`x509.ParsePKCS8PrivateKey`, must be `*rsa.PrivateKey`) or `RSA PRIVATE KEY`
  (`x509.ParsePKCS1PrivateKey`); header and claims written as JSON text in the JS key order with `jsString`;
  `base64.RawURLEncoding`; `rsa.SignPKCS1v15(nil, key, crypto.SHA256, sha256(unsigned))` (deterministic, so the
  fixtures compare the whole assertion).
- [ ] **Step 4: Run** the provauth tests with `-race`, vet (both GOOS), gofmt.
- [ ] **Step 5: Commit and push**: `git commit -m "Add the Google service-account source to gw/provauth: an RS256 JWT bearer grant (P4g)"`.

### Task 6: The fixture generator, the corpus, the Go replay and the drift check

**Files:**
- Create: `scripts/fixtures/gen-provauth.js`, `gw/testdata/provauth/*.json`, `gw/provauth/fixtures_test.go`,
  `test/provauth-fixtures.test.js`

**Interfaces:**
- Produces: `require('./scripts/fixtures/gen-provauth.js').generate(): Promise<Map<string, string>>` (file name →
  file text); `node scripts/fixtures/gen-provauth.js` writes them (and removes stale `*.json` in the folder).

- [ ] **Step 1: Write the generator.** A fixed 2048-bit RSA test key (PKCS#8 PEM, generated once for this corpus and
  labelled test-only; the PKCS#1 form is derived with `crypto.createPrivateKey(pem).export({ type: 'pkcs1', format:
  'pem' })`). Emails use `example.test`, never a real domain, and the JSON carries no `type`/`private_key_id` (no
  secret-scanner pattern). For each case: `getConfig` returns the one provider (`preset: 'custom'`, `baseUrl:
  'https://upstream.example.test'`, `apiKey: { env: 'S' }` or none, the case's `auth`); `secrets = { resolve: () =>
  secret, mask: (s) => mask.reduce((a, v) => a.split(v).join('***'), String(s ?? '')) }`; `fetchImpl` records
  `{ url, method, headers, body }` and answers `new Response(r.body, { status: r.status, headers: r.headers })` from
  the queue; `now` returns the step's `now`. `setInterval` is replaced by a no-op only while `createProviders` runs
  (its background refresh is not part of a step). Each step calls `accessToken(p, step.force)` and records
  `{ token, exp: quota()[0].token.expiresAt }` or `{ throws: err.message }`. Output:
  `JSON.stringify({ fn: 'accessToken', in, out }, null, 2) + '\n'`.
- [ ] **Step 2: The corpus** (one file per case): `oauth2-cc-basic` (cache hit, the exact lead boundary, the refresh
  after it with `expires_in` as a string), `oauth2-cc-json-secret` (special characters in client id, secret and
  scope), `oauth2-cc-no-secret-field`, `oauth2-cc-numeric-secret` (JSON `123456`: no `client_secret`, as the JS does),
  `oauth2-cc-force`, `oauth2-rt-rotation`, `oauth2-rt-missing`, `oauth2-no-credential`, `oauth2-empty-credential`,
  `oauth2-http-401`, `oauth2-error-masked`, `oauth2-no-access-token`, `oauth2-not-json`, `oauth2-redirect-302`,
  `oauth2-redirect-307`, `oauth2-expires-variants` (missing, 0, `" 90 "`, `"abc"`, `"0x10"`, -5, 1.5),
  `oauth2-fail-then-ok`, `google-basic`, `google-token-uri`, `google-token-uri-empty`, `google-pkcs1`,
  `google-no-email`, `google-null-email`, `google-escapes` (an email with `"`, `\`, U+2028, `<>&` and `é`),
  `google-not-json`, `google-no-credential`, `google-error`.
- [ ] **Step 3: Write the failing Go replay** (`fixtures_test.go`): for every `../testdata/provauth/*.json`, build the
  source from `in` (`OAuth2` for `auth.type == "oauth2"`, else `GoogleServiceAccount` with `auth.scope`; `WithID`,
  `WithMask(MaskValues(in.mask))`, a `Secret` returning `in.secret` or `""` for `null`), an `http.Client` whose
  `Transport` records `{url, method, headers (content-type, accept), body}` and answers from `in.responses`, a clock
  set to each step's `now`; collect `{token, exp: exp.UnixMilli()}` or `{throws}`; compare
  `json.Marshal(decoded out)` with `json.Marshal(got)`. Also fail when the folder has no file.
- [ ] **Step 4: Generate**: `node scripts/fixtures/gen-provauth.js`; run `go -C gw test -race -count=1 ./provauth/`
  until every case matches (a mismatch is a bug in Go, or a quirk listed under "Spec deviations" 5 that then leaves
  the corpus).
- [ ] **Step 5: The drift check** (`test/provauth-fixtures.test.js`): `generate()` equals the files on disk, name for
  name and byte for byte, and the folder has no other file. Run on Node and on Deno (`-P=test`): both runtimes must
  produce the same bytes.
- [ ] **Step 6:** prettier `--write` the two new JS files; vet, gofmt.
- [ ] **Step 7: Commit and push**: `git commit -m "Generate provauth fixtures from the JS token code and replay them in Go, with a drift check on both runtimes (P4g)"`.

### Task 7: Verification and the full matrix

- [ ] `df -h /`; `go -C gw vet ./...`; `GOOS=windows go -C gw vet ./...`; `go -C gw test -race -count=1 ./...`;
  `gofmt -l gw`.
- [ ] Full Node suite once: `node --test test/*.test.js`; full Deno suite once:
  `deno test -q --no-check --no-prompt -P=test test/*.test.js` (with `ZAAFPULSE_DATA` and `HOME` in temp folders).
- [ ] `gh workflow run ci.yml -R rifqifitriady/ZAAFPulse-deno-go --ref migrate/p4-lib-ipc-auth`, watch it, fix any red
  job (Windows outbox behaviour in particular) with a commit per fix, and record the run id.

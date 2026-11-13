# P4e guard readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go guardrail library `gw/guard` meet the P4e entry conditions (plan
`2026-11-12-p4-lib-translate-guard.md`, section "P4e entry conditions", notes 13, 18–21; review
`stream-b-review.md` I4, M7, M8, N1 and the 256 MB heap gap) before stage 2 wires it into the request path. Nothing is
wired here.

**Architecture:** One `jsre.Budget` per `CheckBody`/`CheckText` call carries both the step limit and a wall-clock
deadline; the matcher checks the clock every 65,536 steps, so the hot path stays one comparison. Running out of
either fails the call closed with `*Block{Overrun}` in every mode. The exported scanners take a budget and return the
limit as an error. The text copies that made a 20 MB body cost 174–399 MB of heap are cut (no copy when nothing is
replaced, a UTF-16 to string conversion without a rune slice, a bitset instead of a bool and an int per code unit in
`deobfuscate`). The matcher's start-position filter gets stronger (the first two code units, and word starts for
patterns that begin with `\b`), which is what brings real text under the latency targets.

**Tech Stack:** Go 1.27.3, standard library only. Node 24 and Deno 2.9.8 for the JS side. GitHub Actions.

## Global Constraints

- Scope: `gw/guard/`, `gw/guard/jsre/`, their tests and `testdata/`, `scripts/bench-guard.js`, a new `guard-bench` job
  in `.github/workflows/ci.yml` (its own job; stream A adds steps to other jobs), and the plan docs.
- Every existing fixture in `gw/testdata/guard` stays byte-identical to the JS output; the only behaviour change is a
  new overrun (a block where the JS answers slowly), already a recorded deviation (note 13).
- Limits: one step budget per call, `min(20,000,000 + 600 × units checked, 3,000,000,000)` as before but shared by
  every string and check of the call; a deadline of 2 s (`requestDeadline`). Either fails closed in every mode.
- A 20 MB body split into 1,000 strings finishes or blocks within 2.5 s of CPU (I4).
- Peak extra heap ≤ 256 MB for any body up to 20 MB, measured as in `TestAdversarialInputs`.
- Real-text corpus (`gw/guard/testdata/bench-corpus.txt`): no overrun at any size up to 20 MB.
- Benchmark targets on the Ubuntu runner, all three checks on: 32 KB p50 ≤ 5 ms and p99 ≤ 15 ms; 128 KB p50 ≤ 30 ms and
  p99 ≤ 60 ms; 1 MB p99 ≤ 300 ms; every size at most 3× the JS (`scripts/bench-guard.js`, same slices). Up to three
  attempts per size.
- Verification per task: `gofmt -l gw` empty, `go -C gw vet ./...` (also `GOOS=windows`), `go -C gw test -race ./...`,
  the guard tests outside `-race`; at the end the generators on Node and Deno with no diff, the Node and Deno suites
  once, and the full CI matrix dispatched on the branch.
- Commits: one plain English sentence, no AI attribution, never `.superpowers/`. Push `migrate/p4e-guard-ready` only.
  Never touch ports 4777/4787.

## Files

| File | Responsibility |
|---|---|
| `gw/guard/jsre/vm.go` | `Budget` gains `Deadline` and `Now`; `ErrDeadline`; the clock is read every 65,536 steps |
| `gw/guard/jsre/match.go` | skipped start positions are counted; one VM per `FindAll`/`ReplaceAll`; `ReplaceAll` returns its input when nothing matched; `Str` without a rune slice |
| `gw/guard/jsre/first.go` | the two-code-unit and word-start filters |
| `gw/guard/guard.go` | the call's budget (`run.b`), `requestDeadline`, `Failure` in `Result`/`TextResult` |
| `gw/guard/injection.go`, `secrets.go`, `pii.go` | exported scanners take a `*jsre.Budget` and return an error; `deobfuscate` without per-unit copies |
| `gw/guard/benchcorpus_test.go`, `bench_test.go` | the corpus, `Benchmark*`, `TestGuardBench`, `TestBenchCorpusNeverOverruns` |
| `gw/guard/testdata/bench-corpus.txt` | the corpus: repository paths (guard corpus, code, docs, agent transcripts) |
| `scripts/bench-guard.js` | the JS timing on the same slices, `--json` for the Go test |
| `.github/workflows/ci.yml` | job `guard-bench` (Ubuntu): JS timing, then `TestGuardBench` and `TestBenchCorpusNeverOverruns` |

## Interfaces

```go
package jsre
var ErrDeadline = errors.New("jsre: deadline exceeded")
type Budget struct {
	Steps, Limit int64
	Deadline     time.Time        // zero: none
	Now          func() time.Time // nil: time.Now
	// unexported: next clock check
}
func (b *Budget) Run(f func()) error // ErrBudget, ErrStack or ErrDeadline; other panics propagate

package guard
var requestDeadline = 2 * time.Second
type Failure struct { Check string; Count int; Err string } // a detector that failed in log mode (N1)
type Result struct { Body jsv.Value; Action string; Hits []Hit; Error bool; Failures []Failure }
type TextResult struct { Text string; Action string; Hits []Hit; Error bool; Failures []Failure }
func NewBudget(units int) *jsre.Budget // the step limit for that many code units and the 2 s deadline
func ScanInjection(text string, b *jsre.Budget) (*Injection, error)
func SecretSpans(text string, b *jsre.Budget) ([]Span, error)
func MaskPII(text string, b *jsre.Budget) (string, map[string]int, error)
// b nil: NewBudget(len) of the text. A limit comes back as jsre.ErrBudget, ErrStack or ErrDeadline, never a panic.
```

## Tasks

### Task 1: Benchmark, corpus and JS timing

**Files:** create `gw/guard/benchcorpus_test.go`, `gw/guard/bench_test.go`, `gw/guard/testdata/bench-corpus.txt`,
`scripts/bench-guard.js`.

- [x] Corpus loader (manifest of repository paths joined with `\n`), slices cut on UTF-8 boundaries, offsets spread
  evenly; the JS script builds the same bytes and slices.
- [x] `TestGuardBench` (runs only with `ZP_GUARD_BENCH=1`; JS numbers from `ZP_GUARD_BENCH_JS`): p50/p99 per size,
  three attempts, a Markdown table to the log and `$GITHUB_STEP_SUMMARY`.
- [x] `TestBenchCorpusNeverOverruns`: 2, 5 and 20 MB, three slices each, no `*Block{Overrun}` (skipped under `-race`).
- [x] Record the "before" numbers in the report. Commit.

### Task 2: One budget and a deadline per call (I4)

**Files:** `gw/guard/jsre/vm.go`, `match.go`, `jsre/jsre_test.go`, `gw/guard/guard.go`, `adversarial_test.go`.

- [x] Failing tests: `jsre` — a budget with a past deadline stops a long run with `ErrDeadline` (an injected `Now`);
  skipped start positions count as steps. `guard` — `TestOneBudgetPerCall`: a 20 MB body of 1,000 crafted strings
  (each adversarial unit) returns or blocks within 2.5 s wall and, on Unix, 2.5 s of process CPU;
  `TestDeadlineFailsClosedInEveryMode` (an injected clock).
- [x] `Budget` with `Deadline`/`Now`, `ErrDeadline`; `run` holds one budget; each check on a string adds its allowance
  (`600 × units`) to the limit up to the cap; the data-URL test uses the same budget.
- [x] Note in the plan that M8 now applies per call. Commit.

### Task 3: Budgeted exported scanners (M7)

- [x] Failing test: each exported scanner given the 10 MB inputs of `TestNoEntryPointPanicsPastALimit` returns an error
  wrapping a limit and never panics; with `nil` it still never panics.
- [x] New signatures (Interfaces); fixtures call them with `nil`. Commit.

### Task 4: Detector failures in log mode (N1)

- [x] Failing test: a detector that panics in `log` mode yields `Result.Failures = [{secrets, 1, "boom"}]` (counted per
  string) and `TextResult.Failures` likewise; fixtures unchanged (`error` stays the JS boolean).
- [x] Implement. Commit.

### Task 5: Peak heap ≤ 256 MB

- [x] Failing test: `maxHeapGrowth` lowered to 256 MB; also checked on the 1,000-string body and the 20 MB corpus.
- [x] `ReplaceAll` returns its input slice when nothing matched and sizes its output once; `Str` encodes UTF-8 directly;
  `deobfuscate` keeps a drop bitset and maps offsets with per-block counts, applies leetspeak in place on its own copy;
  `visitStr` compares without a second copy. Commit.

### Task 6: Faster start-position filter

- [x] Failing test first: an equivalence test running every guard pattern with and without the filter over the corpus
  and the fixture texts (identical matches), then the benchmark targets.
- [x] Two-code-unit filter (prefix pairs over ASCII plus one class for the rest); word-start filter for patterns whose
  every branch begins with `\b` before a word character; one VM per `FindAll`; a VM is made only when a position
  passes the filter. Commit.

### Task 7: CI job and documentation

- [x] Job `guard-bench` (ubuntu-latest, own job): `node scripts/bench-guard.js --json`, then
  `ZP_GUARD_BENCH=1 go test -run 'TestGuardBench|TestBenchCorpusNeverOverruns'`; fails on a missed target.
- [x] Plan notes 22+ in `2026-11-12-p4-lib-translate-guard.md` with before/after tables; verification; dispatch the full
  matrix. Commit.

## Outcome

Done; results and the benchmark table are in notes 22–26 of `2026-11-12-p4-lib-translate-guard.md`. Deviations from
the tasks above: the speed task also added a literal-prefix trie, a leading-lookbehind filter, a run-then-unit search
(email), the one-scan `jsre.Multi` for the injection, anchor and secret patterns, and `guard.Prepare` (pattern
compilation outside the deadline); injection now runs before secrets and pii inside a string (its hits are still added
last). `TestBenchCorpusNeverOverruns` checks the step budget on every run and the 2 s deadline only in the
`guard-bench` job, where the targets are set.

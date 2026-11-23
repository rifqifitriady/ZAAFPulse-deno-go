// The scenarios of the final benchmark runner (scripts/final-bench.js), each with its targets and the results document
// that owes it (docs/superpowers/specs/2026-11-23-final-bench-design.md). Pure data and pure functions.

// The targets (program plan §4). M3: ≤ 50% of the JS baseline p95 5.67 ms and ≤ 5 ms.
const TARGETS = { M3: 2.84, M4_STREAMS: 500, M4_OVERHEAD: 10, M4_RSS: 80, M5: 200, M7: 1000 };
// The tree P4f was judged against (bench/results/2026-11-17-p4f-notes.md): P4e merged, before P4f.
const P4F_BASE = 'e41e2ea';
// The 500-stream overhead p95 gap P4f measured over that base (31.48 → 48.55 ms, not quiet-confirmed).
const P4F_GAP_MS = 17.07;

const plan = 'plan §4 (docs/migration/2026-11-10-deno-go-migration-plan.md)';
const SRC = {
  baseline: 'plan §2 baseline; owner decision 2026-11-10 (stream cells noisy, re-record at the end)',
  p4c: 'P4c results (docs/migration/2026-11-13-p4c-results.md); owner note item 1',
  p4e: 'P4e results "Owed" (docs/migration/2026-11-16-p4e-results.md); P7 precondition 2',
  p4d: 'P4d results "Owed" (docs/migration/2026-11-17-p4d-results.md)',
  rtk: 'token saver results "Owed" (docs/migration/2026-11-21-rtk-go-results.md)',
  p4h: 'P4h results "Owed" (docs/migration/2026-11-21-p4h-results.md)',
  p4f: 'P4f results "Follow-ups", final review m7; spec D21 (docs/migration/2026-11-17-p4f-results.md)',
  p5: 'P5 results "Benchmarks" (docs/migration/2026-11-22-p5-results.md)',
  p6: 'P6 results "Deferred" (docs/migration/2026-11-23-p6-results.md)',
  p3: 'P3 results §3 (docs/migration/2026-11-11-p3-results.md; measured on battery)',
  p4a: 'P4a results (docs/migration/2026-11-12-p4a-results.md; the front baseline)',
};

/**
 * Every scenario, in run order. kind: run (bench/run.js), egress (bench/egress-overhead.js), binary
 * (scripts/smoke-binary.js on a packaged build), json (a bench script printing one JSON object). checks: the targets
 * judged on it (else its rows are information).
 * @param {{ runtime?: string }} [o]
 */
function scenarios({ runtime = 'deno' } = {}) {
  const run = (id, title, source, o) => ({ id, title, source, kind: 'run', runtime, engine: 'js', tier: 'external', env: {}, checks: [], ...o });
  const go = ['M3', 'M4'];
  return [
    run('baseline', 'JS gateway on Node, external tier, 50/200/500 streams: the streaming baseline re-recorded', SRC.baseline, {
      runtime: 'node',
      levels: [50, 200, 500],
      baseline: true,
    }),
    run('private-js', 'M4 trio, private tier: the JS gateway', SRC.p4c, { tier: 'private' }),
    run('private-front', 'M4 trio, private tier: the Go gateway in front', SRC.p4c, { tier: 'private', engine: 'front' }),
    run('private-go', 'M4 trio, private tier: the go engine serving', SRC.p4c, { tier: 'private', engine: 'go', checks: go }),
    run('external-js', 'M4 trio, external tier (guardrails in the path): the JS gateway', SRC.p4e, {}),
    run('external-front', 'M4 trio, external tier: the Go gateway in front', SRC.p4e, { engine: 'front' }),
    run('external-go', 'M4 trio, external tier: the go engine serving (the G3 numbers)', SRC.p4e, {
      engine: 'go',
      checks: ['M3', 'M4', 'M5', 'M7bench'],
    }),
    run('translated-go', 'Translated format: /v1/messages (Anthropic) to chat, go engine, external tier', SRC.p4d, {
      engine: 'go',
      env: { ZP_BENCH_FORMAT: 'anthropic' },
      checks: go,
    }),
    run('saver-go', 'Token saver: a 64 KiB tool result in every call, go engine, external tier', SRC.rtk, {
      engine: 'go',
      env: { ZP_BENCH_TOOL_RESULT: '65536' },
      checks: go,
    }),
    run('saver-js', 'Token saver: a 64 KiB tool result in every call, JS gateway, external tier', SRC.rtk, {
      env: { ZP_BENCH_TOOL_RESULT: '65536' },
    }),
    run('route-go', 'Route: chat on a two-target round-robin route, go engine, external tier', SRC.p4h, {
      engine: 'go',
      env: { ZP_BENCH_ROUTE: 'round-robin' },
      checks: go,
    }),
    run('route-js', 'Route: chat on a two-target round-robin route, JS gateway, external tier', SRC.p4h, {
      env: { ZP_BENCH_ROUTE: 'round-robin' },
    }),
    run('p4f-base', `P4f A/B: the base tree ${P4F_BASE}, Node, go engine, private tier`, SRC.p4f, {
      runtime: 'node',
      engine: 'go',
      tier: 'private',
      commit: P4F_BASE,
    }),
    run('p4f-head', 'P4f A/B: this tree, Node, go engine, private tier', SRC.p4f, { runtime: 'node', engine: 'go', tier: 'private', gapOf: 'p4f-base' }),
    run('p4f-notrace', 'P4f A/B: this tree, tracing off', SRC.p4f, {
      runtime: 'node',
      engine: 'go',
      tier: 'private',
      env: { ZP_BENCH_TRACING: 'off' },
      gapOf: 'p4f-base',
    }),
    run('p4f-otlp', 'P4f D21: this tree with OTLP export on (to a local sink), go engine, private tier', SRC.p4f, {
      runtime: 'node',
      engine: 'go',
      tier: 'private',
      env: { ZP_BENCH_OTLP: 'on' },
      checks: go,
    }),
    { id: 'egress', title: 'Egress: a connectors call through the egress endpoint against direct, local fake', source: SRC.p5, kind: 'egress', checks: ['egress'] },
    { id: 'binary', title: 'Packaged binary with the gateway in front: start-up (M7) and archive size (M6)', source: SRC.p6, kind: 'binary', checks: ['M7'] },
    { id: 'ipc', title: 'IPC v1 round trip and event throughput (information)', source: SRC.p3, kind: 'json', script: 'bench/ipc-latency.js', checks: [] },
    { id: 'front-proxy', title: "The Go front's proxy overhead on dashboard pages (information)", source: SRC.p4a, kind: 'json', script: 'bench/front-overhead.js', checks: [] },
  ];
}

const IDS = scenarios().map((s) => s.id);

/**
 * The environment of one bench/run.js run of a scenario (on top of the scrubbed base environment).
 * @param {any} def
 * @param {{ smoke?: boolean }} [o]
 */
function runEnv(def, { smoke = false } = {}) {
  const levels = smoke ? (def.levels ? [5, 10] : [5]) : def.levels || [TARGETS.M4_STREAMS];
  return {
    ZP_RUNTIME: def.runtime,
    ...(def.engine === 'js' ? {} : { ZP_ENGINE: def.engine }),
    ZP_BENCH_TIER: def.tier,
    ZP_BENCH_LEVELS: levels.join(','),
    ...(smoke ? { ZP_BENCH_REQUESTS: '20', ZP_BENCH_SECONDS: '1', ZP_BENCH_STARTS: '1' } : {}),
    ...def.env,
  };
}

/**
 * The arguments of a json or egress scenario's script.
 * @param {any} def
 * @param {{ smoke?: boolean, runtime?: string }} [o]
 */
function scriptArgs(def, { smoke = false, runtime = 'deno' } = {}) {
  if (def.kind === 'egress') return smoke ? ['--count', '20', '--warmup', '2'] : ['--count', '2000', '--warmup', '100'];
  if (def.id === 'ipc') return smoke ? ['--count', '50', '--events', '500'] : ['--count', '2000', '--events', '30000'];
  if (def.id === 'front-proxy') return ['--count', smoke ? '50' : '2000', '--runtime', runtime];
  return [];
}

/**
 * A rough length of one repeat of a scenario in seconds (for the estimate before the confirmation).
 * @param {any} def
 * @param {{ smoke?: boolean }} [o]
 */
function estimateSeconds(def, { smoke = false } = {}) {
  if (def.kind === 'run') {
    const env = runEnv(def, { smoke });
    const requests = Number(env.ZP_BENCH_REQUESTS || 2000);
    const seconds = Number(env.ZP_BENCH_SECONDS || 10);
    const starts = Number(env.ZP_BENCH_STARTS || 5);
    const levels = env.ZP_BENCH_LEVELS.split(',').length;
    // Start-ups, the mock, seeding 25 keys and the 5 s idle wait; warm-up and paired calls at about 26 ms each; each
    // stream level twice (gateway, then direct) plus its drain.
    return Math.round(starts * 2 + 15 + (100 + 2 * requests) * 0.026 + levels * (2 * seconds + 4));
  }
  if (def.kind === 'egress') return smoke ? 5 : 15;
  if (def.kind === 'binary') return smoke ? 15 : 20;
  if (def.id === 'ipc') return smoke ? 5 : 20;
  return smoke ? 10 : 25;
}

module.exports = { TARGETS, P4F_BASE, P4F_GAP_MS, SRC, IDS, scenarios, runEnv, scriptArgs, estimateSeconds, plan };

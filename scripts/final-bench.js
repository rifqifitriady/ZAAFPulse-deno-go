// The final benchmarks of the Deno + Go migration in one command (docs/migration/final-benchmarks.md; design
// docs/superpowers/specs/2026-11-23-final-bench-design.md): a preflight of the machine, a confirmation and a countdown,
// every owed scenario in rounds against local fake providers, and a report in bench/results/<date>-final/.
//   deno task bench:final [flags]        node scripts/final-bench.js [flags]        --help for the flags
// Parts: bench/final/{flags,scenarios,preflight,procs,keepawake,runner,report}.js.
const fs = require('fs');
const os = require('os');
const path = require('path');
const readline = require('readline');
const { spawnSync } = require('child_process');
const { parseFlags, USAGE } = require('../bench/final/flags');
const sc = require('../bench/final/scenarios');
const pf = require('../bench/final/preflight');
const runner = require('../bench/final/runner');
const report = require('../bench/final/report');
const { startKeepAwake } = require('../bench/final/keepawake');

const ROOT = runner.ROOT;
const day = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
const mins = (s) => (s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.round((s % 3600) / 60)} min` : `${Math.max(1, Math.round(s / 60))} min`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const say = (s = '') => process.stdout.write(`${s}\n`);

function ask(question) {
  return new Promise((resolve) => {
    const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
    rl.question(question, (a) => {
      rl.close();
      resolve(String(a || '').trim().toLowerCase());
    });
  });
}

function gitFacts() {
  const r = spawnSync('git', ['-C', ROOT, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
  const d = spawnSync('git', ['-C', ROOT, 'status', '--porcelain', '--untracked-files=no'], { encoding: 'utf8' });
  return { sha: r.status === 0 ? r.stdout.trim() : null, dirty: d.status === 0 ? !!d.stdout.trim() : null };
}

// The results folder: never one that already holds a report (a second session the same day gets -2, -3, …).
function outDir(opts) {
  if (opts.out) return path.resolve(opts.out);
  const base = path.join(ROOT, 'bench', 'results', `${day()}-final${opts.smoke ? '-smoke' : ''}`);
  let dir = base;
  for (let i = 2; fs.existsSync(path.join(dir, 'report.md')); i++) dir = `${base}-${i}`;
  return dir;
}

async function main(argv) {
  const parsed = parseFlags(argv, sc.IDS);
  if (parsed.error) {
    process.stderr.write(`${parsed.error}\n\n${USAGE}\n`);
    return 2;
  }
  const opts = parsed.opts;
  if (opts.help) {
    say(USAGE);
    return 0;
  }
  const all = sc.scenarios({ runtime: opts.runtime });
  const defs = opts.only ? all.filter((d) => opts.only.includes(d.id)) : all;
  if (opts.list) {
    for (const d of all) say(`${d.id.padEnd(15)} ${d.title}\n${''.padEnd(15)} ${d.checks.length ? `targets: ${d.checks.join(', ')}` : 'information'} · ${d.source} · ~${mins(sc.estimateSeconds(d))} a run`);
    return 0;
  }
  const STAMP = opts.smoke ? report.SMOKE_STAMP : null;
  const out = outDir(opts);
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-final-bench-'));
  say(STAMP ? `\n*** ${STAMP} ***\n` : '');
  say(`ZAAFPulse final benchmarks · ${defs.length} scenario(s) × ${opts.repeats} repeat(s) · server runtime ${opts.runtime}`);
  say(`Results: ${out}\n`);

  // Preflight (also with --skip-preflight: the checks are printed and recorded, they just do not stop the run).
  say('Preflight');
  const pre = await pf.runPreflight(pf.realDeps({ selfPid: process.pid, dirs: [path.dirname(out), os.tmpdir()], heavy: opts.heavy }));
  const mark = { pass: 'pass', warn: 'WARN', fail: 'FAIL', note: 'note' };
  for (const c of pre.checks) say(`  ${mark[c.status].padEnd(4)}  ${c.label}: ${c.detail}`);
  const gwReady = pre.checks.find((c) => c.id === 'gateway').status === 'pass';
  if (!gwReady) {
    say('\nThe Go gateway is not built and could not be built: nothing can run.');
    fs.rmSync(tmp, { recursive: true, force: true });
    return 1;
  }
  if (!pre.ok && !opts.skipPreflight) {
    say('\nThe preflight failed: fix the FAIL lines above and run again, or pass --skip-preflight (results then not quiet-confirmed).');
    fs.rmSync(tmp, { recursive: true, force: true });
    return 1;
  }
  if (opts.skipPreflight) say('\n--skip-preflight: every result will be stamped "not quiet-confirmed".');

  const perRound = defs.reduce((a, d) => a + sc.estimateSeconds(d, { smoke: opts.smoke }), 0);
  const prepS = (defs.some((d) => d.commit) ? 60 : 0) + (defs.some((d) => d.kind === 'binary') && !opts.binary && opts.build ? 180 : 0);
  say(`\nEstimated: about ${mins(perRound * opts.repeats + prepS + opts.countdown)} (${mins(perRound)} a round${prepS ? `, ${mins(prepS)} to prepare` : ''}).`);

  const power = pre.checks.find((c) => c.id === 'power');
  if (power && power.ask && !opts.yes) {
    if (!process.stdin.isTTY) {
      say('Could not tell whether the machine is on AC power; run in a terminal, or with --yes.');
      return 1;
    }
    if ((await ask('Is the machine plugged in to AC power? [y/N] ')) !== 'y') return 1;
  }
  if (!opts.yes) {
    if (!process.stdin.isTTY) {
      say('No terminal to confirm in: run with --yes.');
      return 1;
    }
    const a = await ask('Leave the machine alone from now until the report is written. Type "yes" to start: ');
    if (a !== 'yes') {
      say('Not started.');
      fs.rmSync(tmp, { recursive: true, force: true });
      return 1;
    }
  }

  fs.mkdirSync(out, { recursive: true });
  const startedAt = new Date().toISOString();
  const sampler = runner.createSampler({ selfPid: process.pid });
  const awake = startKeepAwake();
  const results = defs.map((def) => ({ def, runs: [] }));
  const v = pre.versions;
  const g = gitFacts();
  const machine = {
    os: `${os.type()} ${os.release()} (${process.platform}-${process.arch})`,
    cpu: (os.cpus()[0] || {}).model || null,
    cores: os.cpus().length,
    memGB: Math.round(os.totalmem() / 2 ** 30),
    deno: v.deno,
    node: v.node,
    go: v.go,
    ...g,
  };
  const prep = { gwBin: pf.GW_BIN };
  let finished = false;
  let interrupted = false;

  const ctx = () => ({
    smoke: opts.smoke,
    preflight: { ran: !opts.skipPreflight, ok: pre.ok, checks: pre.checks },
    results,
    platformName: runner.platformName(),
    machine,
    opts,
    startedAt,
    endedAt: new Date().toISOString(),
    load: sampler.loadStats(),
    runner: `${process.versions.deno ? `deno ${process.versions.deno}` : `node ${process.versions.node}`}, keep-awake: ${awake ? awake.name : 'none'}`,
  });
  const writeAll = (note) => {
    for (const res of results)
      fs.writeFileSync(
        path.join(out, `${res.def.id}.json`),
        `${JSON.stringify({ ...(STAMP ? { stamp: STAMP } : {}), smoke: opts.smoke, quietConfirmed: false, scenario: res.def, runs: res.runs }, null, 2)}\n`,
      );
    const c = ctx();
    fs.writeFileSync(
      path.join(out, 'session.json'),
      `${JSON.stringify({ ...(STAMP ? { stamp: STAMP } : {}), smoke: opts.smoke, machine: c.machine, preflight: c.preflight, opts, startedAt, endedAt: c.endedAt, load: c.load, interrupted }, null, 2)}\n`,
    );
    let md = report.buildReport(c);
    if (note) md = md.replace('\n## Metrics', `\n**${note}**\n\n## Metrics`);
    fs.writeFileSync(path.join(out, 'report.md'), md);
    // The per-scenario quiet flag, from the report's rows.
    const rows = report.evaluate(c);
    for (const res of results) {
      const f = path.join(out, `${res.def.id}.json`);
      const j = JSON.parse(fs.readFileSync(f, 'utf8'));
      j.quietConfirmed = rows.filter((r) => r.scenario === res.def.id).every((r) => r.quiet === 'yes' || r.quiet.startsWith('n/a'));
      fs.writeFileSync(f, `${JSON.stringify(j, null, 2)}\n`);
    }
  };
  const cleanup = async () => {
    sampler.stop();
    const left = await runner.stopEverything(sampler);
    if (awake) awake.stop();
    fs.rmSync(tmp, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
    if (left.length) say(`Could not stop: ${left.join(', ')}`);
  };
  const onSignal = async (sig) => {
    if (interrupted) return;
    interrupted = true;
    say(`\n${sig}: stopping every process this runner started…`);
    try {
      writeAll(`Interrupted (${sig}) before the end: partial results.`);
    } catch {
      /* the report is best effort here */
    }
    await cleanup();
    say(`Partial report: ${path.join(out, 'report.md')}`);
    process.exit(130);
  };
  process.on('SIGINT', () => onSignal('SIGINT'));
  process.on('SIGTERM', () => onSignal('SIGTERM'));

  try {
    sampler.start();
    // Preparation (outside the measured window).
    const base = defs.find((d) => d.commit);
    if (base) {
      say(`\nPreparing the base tree ${base.commit} (git archive and go build)…`);
      const b = runner.prepareBaseTree(base.commit, tmp);
      if (b.error) say(`  ${b.error}`);
      else Object.assign(prep, { baseTree: b.dir, baseGwBin: b.gwBin });
    }
    if (defs.some((d) => d.kind === 'binary')) {
      if (!opts.binary && opts.build) say('\nBuilding the package for this machine (deno task build --package)…');
      const b = runner.prepareBinary({ binary: opts.binary, gw: opts.gw, build: opts.build }, tmp);
      if (b.error) {
        prep.binaryError = b.error;
        say(`  ${b.error}`);
      } else prep.binary = b;
    }
    if (opts.countdown) {
      say('');
      for (let s = opts.countdown; s > 0; s--) {
        process.stdout.write(`\rStarting in ${s} s · hands off… `);
        await sleep(1000);
      }
      say('\rStarting now.                        ');
    }
    for (let round = 1; round <= opts.repeats && !interrupted; round++) {
      say(`\nRound ${round} of ${opts.repeats}${STAMP ? ` (${STAMP})` : ''}`);
      for (const res of results) {
        if (interrupted) break;
        const from = sampler.mark();
        process.stdout.write(`  ${res.def.id.padEnd(15)} `);
        const r = await runner.runScenario(res.def, { smoke: opts.smoke, runtime: opts.runtime, prep, tmp });
        sampler.take();
        const { noisy, noise } = sampler.window(from, { meanLimit: opts.noiseCpu });
        const run = { repeat: round, ...r, noisy, noise, file: `${res.def.id}.json#${round}` };
        res.runs.push(run);
        const x = report.extract(res.def, r.data);
        const key =
          res.def.kind === 'run'
            ? `ns p95 ${x.nsP95} ms · ${x.level} streams: ${x.errors} errors, overhead p95 ${x.streamP95} ms${x.gwPeak ? ` · gw RSS ${x.gwPeak} MB` : ''}`
            : res.def.kind === 'egress'
              ? `overhead p95 ${x.p95} ms`
              : res.def.kind === 'binary'
                ? `ready ${x.readyMs} ms · archive ${x.archiveMB ?? '—'} MB`
                : JSON.stringify(x);
        say(r.ok ? `${r.durationS} s · ${key}${noisy ? ` · NOISY (other processes ${noise.foreignMean}% CPU)` : ''}` : `ERROR: ${r.error}`);
        if (!opts.smoke) await sleep(5000); // let the machine settle between scenarios
      }
      writeAll(round < opts.repeats ? `Partial: ${round} of ${opts.repeats} rounds done.` : null);
    }
    finished = true;
  } finally {
    if (!interrupted) {
      writeAll(finished ? null : 'The run stopped on an error: partial results.');
      await cleanup();
    }
  }
  const rows = report.evaluate(ctx());
  say(`\n${STAMP ? `*** ${STAMP} ***\n` : ''}Report: ${path.join(out, 'report.md')}`);
  say(`Targets: ${rows.filter((r) => /pass/.test(r.verdict)).length} pass, ${rows.filter((r) => /fail/.test(r.verdict)).length} fail, ${rows.filter((r) => r.verdict === 'error').length} error.`);
  return 0;
}

if (require.main === module)
  main(process.argv.slice(2)).then(
    (code) => process.exit(code),
    (err) => {
      process.stderr.write(`${(err && err.stack) || err}\n`);
      process.exit(1);
    },
  );

module.exports = { main };

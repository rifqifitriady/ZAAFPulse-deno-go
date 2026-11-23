// The command line of the final benchmark runner (scripts/final-bench.js). Pure: parseFlags(argv, ids) -> { opts } or
// { error }. `--name value` and `--name=value` both work.

const USAGE = `Usage: deno task bench:final [flags]      (or: node scripts/final-bench.js [flags])

Runs every owed benchmark of the Deno + Go migration in one session and writes bench/results/<date>-final/.
Close the heavy apps, plug in the power, then run it and leave the machine alone until the report is written.

  --yes               start without asking (the countdown still runs)
  --only <ids>        run only these scenarios (comma-separated; --list shows them)
  --smoke             tiny scale, to prove the runner works: SMOKE RUN, NOT A MEASUREMENT
  --out <dir>         where the report goes (default bench/results/<date>-final, or -final-smoke)
  --skip-preflight    run even when a check fails; every result is stamped "not quiet-confirmed"
  --repeats <n>       runs of each scenario, in rounds; the report shows the median and the spread (default 3,
                      smoke 1)
  --runtime <r>       deno (default, what v2 ships) or node: the server's runtime in the main scenarios
  --binary <path>     the packaged zaafpulse binary for the binary scenario (its zaafpulse-gw next to it, or --gw)
  --gw <path>         the packaged zaafpulse-gw next to --binary
  --no-build          do not build the package for the binary scenario (it is then skipped without --binary)
  --countdown <s>     seconds of countdown before the first scenario (default 10, smoke 0)
  --heavy <names>     more app names the preflight treats as heavy (comma-separated)
  --noise-cpu <pct>   a run is noisy when other processes averaged above this CPU (percent of one core; default 50)
  --list              list the scenarios and exit
  --help              this text`;

/**
 * @param {string[]} argv the arguments after the script
 * @param {string[]} ids the known scenario ids
 */
function parseFlags(argv, ids) {
  const o = {
    yes: false,
    only: null,
    smoke: false,
    out: null,
    skipPreflight: false,
    repeats: null,
    runtime: 'deno',
    binary: null,
    gw: null,
    build: true,
    countdown: null,
    heavy: [],
    noiseCpu: 50,
    list: false,
    help: false,
  };
  const bools = { yes: 'yes', smoke: 'smoke', 'skip-preflight': 'skipPreflight', list: 'list', help: 'help' };
  const values = ['only', 'out', 'repeats', 'runtime', 'binary', 'gw', 'countdown', 'heavy', 'noise-cpu'];
  const err = (error) => ({ error });
  for (let i = 0; i < argv.length; i++) {
    let a = argv[i];
    if (a === '-h') a = '--help';
    if (!a.startsWith('--')) return err(`unexpected argument "${a}"`);
    let name = a.slice(2);
    let value = null;
    const eq = name.indexOf('=');
    if (eq >= 0) {
      value = name.slice(eq + 1);
      name = name.slice(0, eq);
    }
    if (name in bools) {
      if (value !== null) return err(`--${name} takes no value`);
      o[bools[name]] = true;
      continue;
    }
    if (name === 'no-build') {
      o.build = false;
      continue;
    }
    if (!values.includes(name)) return err(`unknown flag --${name}`);
    if (value === null) {
      value = argv[++i];
      if (value === undefined || value.startsWith('--')) return err(`--${name} needs a value`);
    }
    switch (name) {
      case 'only': {
        const list = value
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean);
        const unknown = list.filter((id) => !ids.includes(id));
        if (!list.length) return err('--only needs at least one scenario id');
        if (unknown.length) return err(`--only: unknown scenario ${unknown.join(', ')} (known: ${ids.join(', ')})`);
        o.only = [...new Set(list)];
        break;
      }
      case 'repeats':
      case 'countdown': {
        const n = Number(value);
        const max = name === 'repeats' ? 20 : 600;
        const min = name === 'repeats' ? 1 : 0;
        if (!Number.isInteger(n) || n < min || n > max) return err(`--${name} must be a whole number from ${min} to ${max}`);
        o[name] = n;
        break;
      }
      case 'noise-cpu': {
        const n = Number(value);
        if (!Number.isFinite(n) || n <= 0 || n > 1000) return err('--noise-cpu must be a number above 0 (percent of one core)');
        o.noiseCpu = n;
        break;
      }
      case 'runtime':
        if (!['deno', 'node'].includes(value)) return err('--runtime must be deno or node');
        o.runtime = value;
        break;
      case 'heavy':
        o.heavy.push(
          ...value
            .split(',')
            .map((s) => s.trim())
            .filter(Boolean),
        );
        break;
      default:
        o[name] = value;
    }
  }
  if (o.gw && !o.binary) return err('--gw needs --binary');
  if (o.repeats === null) o.repeats = o.smoke ? 1 : 3;
  if (o.countdown === null) o.countdown = o.smoke ? 0 : 10;
  return { opts: o };
}

module.exports = { parseFlags, USAGE };

// A node:test reporter that names the test files still running. node --test reports a file's tests as the file's
// process sends them, and a file whose process never exits (a server or child left running that holds its stdout, a
// hang outside any test) is never finished: the run just waits, with nothing in the log that names it. This reporter
// notes when each file starts and ends and, from a 30 s timer, prints each file that has run for longer than
// ZP_TEST_WATCH_S seconds (default 180), again every as many seconds, so a run that is stopped from outside (a CI
// step timeout) has already named the file it was waiting for. On GitHub Actions the line is a warning annotation.
// It prints nothing else: run it next to the spec reporter (.github/workflows/ci.yml).
//   node --test --test-reporter=spec --test-reporter-destination=stdout \
//     --test-reporter=./scripts/test-watch.js --test-reporter-destination=stderr test/*.test.js
const path = require('path');

const LIMIT_MS = (Number(process.env.ZP_TEST_WATCH_S) || 180) * 1000;

module.exports = async function* testWatch(source) {
  const running = new Map(); // test file -> { since, told }
  // A reporter writes when an event arrives; a hung run sends none, so the timer writes to stderr itself.
  const timer = setInterval(() => {
    const now = Date.now();
    for (const [file, r] of running) {
      if (now - r.since < LIMIT_MS || now - r.told < LIMIT_MS) continue;
      r.told = now;
      const text = `test-watch: ${file} is still running after ${Math.round((now - r.since) / 1000)} s`;
      process.stderr.write(process.env.GITHUB_ACTIONS === 'true' ? `::warning::${text}\n` : `${text}\n`);
    }
  }, 30000);
  timer.unref();
  try {
    for await (const { type, data } of source) {
      // A file is a test named by its path at nesting 0; the tests inside it have their own names.
      if (!data || data.nesting !== 0 || !data.file || path.resolve(String(data.name)) !== data.file) continue;
      const file = path.relative(process.cwd(), data.file);
      if (type === 'test:dequeue') running.set(file, { since: Date.now(), told: 0 });
      else if (type === 'test:complete') running.delete(file);
    }
  } finally {
    clearInterval(timer);
  }
};

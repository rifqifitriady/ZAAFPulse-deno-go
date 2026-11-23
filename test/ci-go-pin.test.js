// P7 review L4: every job that sets up Go pins the same patched release. go1.27.3 carries GO-2026-6859 (net/http,
// HTTP/1 server crash on a MaxBytesReader header race), fixed in go1.27.4; govulncheck reports it for the front's and
// the egress endpoint's http.Server.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');

const MIN = [1, 27, 4];
const WF = path.join(__dirname, '..', '.github', 'workflows');
const ci = fs.readFileSync(path.join(WF, 'ci.yml'), 'utf8');
// P6: build-deno moved into build.yml (called by ci.yml and release.yml), so the pins are counted over every workflow.
const all = fs
  .readdirSync(WF)
  .filter((f) => f.endsWith('.yml'))
  .sort()
  .map((f) => fs.readFileSync(path.join(WF, f), 'utf8'))
  .join('\n');

const atLeast = (v, min) => {
  const p = v.split('.').map(Number);
  for (let i = 0; i < min.length; i++) if ((p[i] || 0) !== min[i]) return (p[i] || 0) > min[i];
  return true;
};

test('every setup-go in the workflows pins one Go release, go1.27.4 or later', () => {
  const pins = [...all.matchAll(/go-version:\s*'([^']+)'/g)].map((m) => m[1]);
  assert.ok(pins.length >= 7, `${pins.length} go-version pins`);
  assert.equal(new Set(pins).size, 1, `one release everywhere: ${[...new Set(pins)].join(', ')}`);
  assert.ok(atLeast(pins[0], MIN), `${pins[0]} is older than ${MIN.join('.')} (GO-2026-6859)`);
  assert.match(
    ci,
    new RegExp(`Go is pinned to ${pins[0].replace(/\./g, '\\.')}\\b`),
    'the header comment names the pin',
  );
});

// Fixtures for the gateway's route classification (P4b entry condition 2): for each raw request path, the pathname the
// control plane routes on (new URL(raw, base).pathname, as server.js builds `url`). gw/front/route_test.go checks that
// every path the gateway calls canonical has exactly this pathname, so a route class is only ever decided on a path
// the control plane would read the same way. Writes gw/testdata/route/<n>-<name>.json as { fn, in, out }.
// Run: node scripts/fixtures/gen-route.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'route');
const RAW = [
  '/v1/chat/completions',
  '/v1/chat/completions/',
  '/v1/chat%2Fcompletions',
  '/v1/%63hat/completions',
  '/v1/./chat/completions',
  '/v1/x/../chat/completions',
  '/v1/%2e/chat/completions',
  '/v1/%2E%2E/v1/chat/completions',
  '/v1\\chat/completions',
  '//v1/chat/completions',
  '/V1/chat/completions',
  '/v1//chat/completions',
  '/v1/chat/completions.',
  '/v1/chat/completions/.',
  '/v1/chat/completions/..',
  '/v1/messages',
  '/v1/messages/count_tokens',
  '/v1/models/oa%2Fm1',
  '/v1/models/oa/m1',
  '/v1beta/models/oa%2Fm1:generateContent',
  '/v1beta/models/oa/m1:generateContent',
  '/mcp',
  '/a2a',
  '/.well-known/agent-card.json',
  '/skills/zaafpulse/SKILL.md',
  '/v1/traces',
  '/v1/a;b/c',
  "/v1/a!$&'()*+,=:@~_-.x",
  // '/v1/a^b' is not here: Node escapes the ^ (/v1/a%5Eb), Deno keeps it, and the corpus holds only what both runtimes
  // agree on (gw/front/route_test.go TestCaretPathIsNotCanonical pins it).
  '/v1/a{b}',
  '/v1/a|b',
  '/v1/a[b]',
  '/v1/café',
  '/api/gateway/status',
  '/',
];

const slug = (s) =>
  s
    .replace(/[^A-Za-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 40) || 'root';
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
RAW.forEach((raw, i) => {
  const out = new URL(raw, 'http://localhost').pathname;
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${slug(raw)}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: 'pathname', in: raw, out })}\n`);
});
console.log(`gen-route: ${RAW.length} cases in ${path.relative(process.cwd(), OUT)}`);

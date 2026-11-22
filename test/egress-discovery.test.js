// The egress discovery file (lib/egress-discovery.js, P5 spec Decision 15): server.js writes .data/egress.json while
// its gateway is ready and removes it when the gateway is gone; mcp.js configures the egress client from it before each
// tool call (re-read when the file changed, used only while its pid is alive). No test leaves the machine: the outside
// name in the mcp.js case only ever reaches the fake endpoint.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');
const { writeDiscovery, removeDiscovery, createDiscoveryLoader } = require('../lib/egress-discovery');
const egress = require('../lib/egress');

const isWin = process.platform === 'win32';

function tmp(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-egress-disc-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}
const ST = { port: 4321, token: 'derived', engines: { connectors: 'go' }, lockdown: false };

test('written 0600 with a rename, not rewritten when unchanged, removed only by its own pid', (t) => {
  const dir = tmp(t);
  const file = path.join(dir, '.data', 'egress.json');
  assert.equal(writeDiscovery(file, ST, 111), true);
  assert.deepEqual(JSON.parse(fs.readFileSync(file, 'utf8')), { pid: 111, ...ST });
  if (!isWin) assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  assert.deepEqual(
    fs.readdirSync(path.dirname(file)).filter((f) => f.endsWith('.tmp')),
    [],
  );
  const ino = fs.statSync(file).ino;
  assert.equal(writeDiscovery(file, ST, 111), false); // unchanged: the mtime stays
  assert.equal(fs.statSync(file).ino, ino);
  assert.equal(writeDiscovery(file, { ...ST, port: 4322 }, 111), true);
  assert.equal(JSON.parse(fs.readFileSync(file, 'utf8')).port, 4322);
  assert.equal(removeDiscovery(file, 222), false); // another server's file stays
  assert.ok(fs.existsSync(file));
  assert.equal(removeDiscovery(file, 111), true);
  assert.ok(!fs.existsSync(file));
  assert.equal(removeDiscovery(file, 111), false); // nothing to remove
  fs.writeFileSync(file, '{broken');
  assert.equal(removeDiscovery(file, 111), true); // an unreadable file is removed
});

test('loader: configures from a file whose pid is alive, re-reads a changed file, goes direct otherwise', async (t) => {
  const dir = tmp(t);
  const file = path.join(dir, 'egress.json');
  const calls = [];
  const dead = new Set();
  const kill = (pid) => {
    if (dead.has(pid)) throw Object.assign(new Error('no such process'), { code: 'ESRCH' });
    if (pid === 999) throw Object.assign(new Error('not permitted'), { code: 'EPERM' });
    return true;
  };
  const lines = [];
  const loader = createDiscoveryLoader({ file, configure: (o) => calls.push(o), kill, log: (l) => lines.push(l) });
  // No file: direct (said once).
  assert.equal(await loader.refresh(), false);
  assert.equal(await loader.refresh(), false);
  assert.deepEqual(calls, [null]);
  // A file with a live pid.
  writeDiscovery(file, ST, 100);
  assert.equal(await loader.refresh(), true);
  assert.deepEqual(calls.at(-1), ST);
  assert.equal(await loader.refresh(), true);
  assert.equal(calls.length, 2); // nothing changed: not configured again
  // Changed (a new port after a gateway restart).
  writeDiscovery(file, { ...ST, port: 5555, engines: { connectors: 'check' } }, 100);
  assert.equal(await loader.refresh(), true);
  assert.equal(calls.at(-1).port, 5555);
  assert.equal(calls.at(-1).engines.connectors, 'check');
  // The pid died without removing the file (same mtime): direct.
  dead.add(100);
  assert.equal(await loader.refresh(), false);
  assert.equal(calls.at(-1), null);
  // Another account's process (EPERM) counts as alive.
  writeDiscovery(file, ST, 999);
  assert.equal(await loader.refresh(), true);
  assert.deepEqual(calls.at(-1), ST);
  // Removed: direct.
  fs.rmSync(file);
  assert.equal(await loader.refresh(), false);
  assert.equal(calls.at(-1), null);
  // Not a discovery file: direct, and said.
  fs.writeFileSync(file, JSON.stringify({ pid: 'x', port: 1, token: 't' }));
  assert.equal(await loader.refresh(), false);
  fs.writeFileSync(file, '{nope');
  assert.equal(await loader.refresh(), false);
  assert.equal(lines.length, 2);
  assert.match(lines[0], /is not a discovery file; going direct/);
  assert.match(lines[1], /could not be read/);
});

test('loader: a pid that cannot be checked (an old registration) goes direct, said once per file', async (t) => {
  const dir = tmp(t);
  const file = path.join(dir, 'egress.json');
  const calls = [];
  const lines = [];
  // The run grant of an AI client registered before P5 holds neither kill nor tasklist.
  const kill = () => {
    throw new Error("kill: not allowed by this build's run permissions");
  };
  const loader = createDiscoveryLoader({ file, configure: (o) => calls.push(o), kill, log: (l) => lines.push(l) });
  writeDiscovery(file, ST, 100);
  // Whatever listens on the file's port is never trusted with the token or the calls: direct.
  assert.equal(await loader.refresh(), false);
  assert.equal(await loader.refresh(), false);
  assert.deepEqual(calls, [null]);
  assert.equal(lines.length, 1);
  assert.match(lines[0], /cannot be checked under this registration; going direct/);
});

test('server.js writes the file on each egress change and removes it when the gateway is gone or it exits', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'server.js'), 'utf8');
  const body = src.slice(
    src.indexOf('function applyEgress()'),
    src.indexOf('\n}\n', src.indexOf('function applyEgress()')),
  );
  assert.match(
    body,
    /if \(opts\) writeDiscovery\(EGRESS_DISCOVERY, opts\);\s+else removeDiscovery\(EGRESS_DISCOVERY\);/,
  );
  assert.match(src, /const EGRESS_DISCOVERY = path\.join\(DIR, '\.data', 'egress\.json'\);/);
  assert.match(src, /process\.on\('exit', \(\) => removeDiscovery\(EGRESS_DISCOVERY\)\);/);
});

test('mcp.js: a tool call goes through the endpoint named by .data/egress.json while its pid is alive', async (t) => {
  const dir = tmp(t);
  const seen = [];
  const server = http.createServer((req, res) => {
    seen.push(req.headers);
    req.resume();
    res.writeHead(200, { 'content-type': 'application/json', 'x-zp-egress-url': req.headers['x-zp-egress-url'] });
    res.end('{"pong":true}');
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((r) => server.close(r));
  });
  const token = egress.deriveToken('front');
  fs.writeFileSync(
    path.join(dir, 'config.json'),
    JSON.stringify({
      integrations: {
        extra: {
          type: 'http',
          baseUrl: 'https://api.example.test',
          endpoints: [{ name: 'ping', method: 'GET', path: '/ping', description: 'Ping' }],
        },
      },
    }),
  );
  // This test process stands for the dashboard: its pid is alive.
  writeDiscovery(
    path.join(dir, '.data', 'egress.json'),
    { port: server.address().port, token, engines: { connectors: 'go' }, lockdown: false },
    process.pid,
  );
  const messages = [
    {
      jsonrpc: '2.0',
      id: 1,
      method: 'initialize',
      params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 't', version: '0' } },
    },
    { jsonrpc: '2.0', method: 'notifications/initialized' },
    { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'extra_ping', arguments: {} } },
  ];
  const out = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [path.join(__dirname, '..', 'mcp.js')], {
      env: {
        ...process.env,
        ZAAFPULSE_DATA: dir,
        ACC_CONFIG: path.join(dir, 'config.json'),
        ACC_CACHE_DIR: path.join(dir, 'cache'),
      },
    });
    let text = '';
    child.stdout.on('data', (c) => (text += c));
    child.on('error', reject);
    child.on('close', () => resolve(text));
    for (const m of messages) child.stdin.write(JSON.stringify(m) + '\n');
    // Close stdin once the tool call answered.
    const until = Date.now() + 15000;
    const timer = setInterval(() => {
      if (text.includes('"id":2') || Date.now() > until) {
        clearInterval(timer);
        child.stdin.end();
      }
    }, 20);
  });
  const reply = out
    .trim()
    .split('\n')
    .map((l) => JSON.parse(l))
    .find((r) => r.id === 2);
  assert.ok(reply && reply.result, out);
  assert.match(JSON.stringify(reply.result), /pong/);
  assert.equal(seen.length, 1);
  assert.equal(seen[0]['x-zp-egress-token'], token);
  assert.equal(seen[0]['x-zp-egress-url'], 'https://api.example.test/ping');
  assert.match(seen[0]['x-zp-egress-purpose'], /^connectors/);
});

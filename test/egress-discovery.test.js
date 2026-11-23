// The egress discovery file (lib/egress-discovery.js, P5 spec Decision 15): server.js writes .data/egress.json while
// its gateway is ready and removes it when the gateway is gone; mcp.js configures the egress client from it before each
// tool call (re-read when the file changed, used only while the endpoint proves it holds the token: GET /hello, P7
// review L1). No test leaves the machine: the outside
// name in the mcp.js case only ever reaches the fake endpoint.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const http = require('http');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');
const {
  writeDiscovery,
  removeDiscovery,
  createDiscoveryLoader,
  proveEndpoint,
  helloProof,
  oldRegistrations,
} = require('../lib/egress-discovery');
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

// The endpoint's proof (P7 review L1): a fake that answers GET /hello as the Go endpoint does (gw/egress/hello.go),
// with the token it holds and the port it listens on.
function helloServer(token, { answer } = {}) {
  const seen = [];
  const server = http.createServer((req, res) => {
    seen.push({ url: req.url, headers: req.headers });
    req.resume();
    const u = new URL(req.url, 'http://x');
    if (req.method === 'GET' && u.pathname === '/hello') {
      const n = u.searchParams.get('n') || '';
      const port = server.address().port;
      res.writeHead(200, { 'content-type': 'text/plain' });
      return res.end(answer ? answer(n, port) : helloProof(token, port, n));
    }
    res.writeHead(200, { 'content-type': 'application/json', 'x-zp-egress-url': req.headers['x-zp-egress-url'] || '' });
    res.end('{"pong":true}');
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve({ server, seen })));
}
const closeAll = (t, ...servers) =>
  t.after(async () => {
    for (const s of servers) {
      s.closeAllConnections();
      await new Promise((r) => s.close(r));
    }
  });

test("helloProof: the Go endpoint's vector (gw/egress/hello_test.go)", () => {
  const token = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
  assert.equal(
    helloProof(token, 4242, 'ab'.repeat(32)),
    '6a48143237facb291e083f006d1c8938dea62308bc35739292c7089b5bfb0a68',
  );
});

test('proveEndpoint: true only for an endpoint that holds the token and answers for its own port', async (t) => {
  const token = egress.deriveToken('front');
  const real = await helloServer(token);
  const squatter = await helloServer('another token');
  const silent = http.createServer(() => {}); // takes the connection, never answers
  await new Promise((r) => silent.listen(0, '127.0.0.1', r));
  // A relay: it forwards the nonce to the real endpoint and passes its answer on (a proof for the real port).
  const relay = await helloServer(token, {
    answer: (n) => helloProof(token, real.server.address().port, n),
  });
  const echo = await helloServer(token, { answer: (n) => n });
  closeAll(t, real.server, squatter.server, silent, relay.server, echo.server);
  assert.equal(await proveEndpoint(real.server.address().port, token), true);
  assert.equal(await proveEndpoint(squatter.server.address().port, token), false);
  assert.equal(await proveEndpoint(relay.server.address().port, token), false);
  assert.equal(await proveEndpoint(echo.server.address().port, token), false);
  assert.equal(await proveEndpoint(silent.address().port, token, { timeoutMs: 200 }), false);
  const closed = http.createServer();
  await new Promise((r) => closed.listen(0, '127.0.0.1', r));
  const gone = closed.address().port;
  await new Promise((r) => closed.close(r));
  assert.equal(await proveEndpoint(gone, token), false);
  // The proof never carries the token: only a fresh 64-hex nonce, and no egress header.
  for (const { url, headers } of [...real.seen, ...squatter.seen, ...relay.seen]) {
    assert.match(url, /^\/hello\?n=[0-9a-f]{64}$/);
    assert.ok(!JSON.stringify(headers).includes(token), 'the token was sent');
    assert.equal(Object.keys(headers).filter((h) => h.startsWith('x-zp-')).length, 0);
  }
  assert.notEqual(real.seen[0].url, relay.seen[0].url, 'a nonce is never reused');
});

test('loader: configures from a file whose endpoint proves itself, re-reads a changed file, goes direct otherwise', async (t) => {
  const dir = tmp(t);
  const file = path.join(dir, 'egress.json');
  const calls = [];
  const proven = new Set([4321, 5555]);
  const asked = [];
  const prove = async (port, token) => {
    asked.push([port, token]);
    return proven.has(port);
  };
  const lines = [];
  const loader = createDiscoveryLoader({ file, configure: (o) => calls.push(o), prove, log: (l) => lines.push(l) });
  // No file: direct (said once), nothing asked.
  assert.equal(await loader.refresh(), false);
  assert.equal(await loader.refresh(), false);
  assert.deepEqual(calls, [null]);
  assert.equal(asked.length, 0);
  // A file whose endpoint proves itself.
  writeDiscovery(file, ST, 100);
  assert.equal(await loader.refresh(), true);
  assert.deepEqual(calls.at(-1), ST);
  assert.deepEqual(asked.at(-1), [4321, 'derived']);
  assert.equal(await loader.refresh(), true);
  assert.equal(calls.length, 2); // nothing changed: not configured again
  assert.equal(asked.length, 2, 'proved again before every call');
  // Changed (a new port after a gateway restart).
  writeDiscovery(file, { ...ST, port: 5555, engines: { connectors: 'check' } }, 100);
  assert.equal(await loader.refresh(), true);
  assert.equal(calls.at(-1).port, 5555);
  assert.equal(calls.at(-1).engines.connectors, 'check');
  // The gateway died without removing the file and something else took the port: the proof fails, direct.
  proven.delete(5555);
  assert.equal(await loader.refresh(), false);
  assert.equal(calls.at(-1), null);
  assert.equal(await loader.refresh(), false);
  assert.equal(lines.filter((l) => /did not prove/.test(l)).length, 1, 'said once per file');
  // A proof that throws is a failed proof.
  writeDiscovery(file, { ...ST, port: 7777 }, 100);
  const throwing = createDiscoveryLoader({
    file,
    configure: (o) => calls.push(o),
    prove: async () => {
      throw new Error('boom');
    },
    log: () => {},
  });
  assert.equal(await throwing.refresh(), false);
  // Removed: direct.
  fs.rmSync(file);
  assert.equal(await loader.refresh(), false);
  assert.equal(calls.at(-1), null);
  // Not a discovery file: direct, and said.
  fs.writeFileSync(file, JSON.stringify({ pid: 'x', port: 1, token: 't' }));
  assert.equal(await loader.refresh(), false);
  fs.writeFileSync(file, '{nope');
  assert.equal(await loader.refresh(), false);
  assert.match(lines.at(-2), /is not a discovery file; going direct/);
  assert.match(lines.at(-1), /could not be read/);
});

test('loader: the default proof against a real listener; no pid check and no kill', async (t) => {
  const dir = tmp(t);
  const file = path.join(dir, 'egress.json');
  const token = egress.deriveToken('front');
  const real = await helloServer(token);
  const squatter = await helloServer('not the token');
  closeAll(t, real.server, squatter.server);
  const calls = [];
  const loader = createDiscoveryLoader({ file, configure: (o) => calls.push(o) });
  // A pid that is long gone: only the proof counts.
  writeDiscovery(file, { ...ST, port: real.server.address().port, token }, 2 ** 31 - 2);
  assert.equal(await loader.refresh(), true);
  writeDiscovery(file, { ...ST, port: squatter.server.address().port, token }, process.pid);
  assert.equal(await loader.refresh(), false);
  assert.equal(calls.at(-1), null);
  assert.ok(!squatter.seen.some((r) => JSON.stringify(r.headers).includes(token)));
  const src = fs.readFileSync(path.join(__dirname, '..', 'lib', 'egress-discovery.js'), 'utf8');
  assert.doesNotMatch(src, /permissions'\)\.kill|kill\(pid/);
});

test('oldRegistrations: an mcp-stdio entry whose run grant still holds kill or tasklist is named', () => {
  const home = '/h';
  const deno = '/opt/deno';
  const entry = (run) => ({
    command: deno,
    args: ['run', '-q', '--no-prompt', '--config=/app/deno.json', '-P=mcp-stdio', `--allow-run=${run}`, '/app/mcp.js'],
  });
  const files = {
    '/h/.claude.json': JSON.stringify({ mcpServers: { zaafpulse: entry('ps,lsof,kill,tasklist') } }),
    '/h/.kiro/settings/mcp.json': JSON.stringify({ mcpServers: { zaafpulse: entry('ps,lsof,which') } }),
    '/h/.cursor/mcp.json': JSON.stringify({ mcpServers: { zaafpulse: entry('ps,/usr/bin/kill') } }),
    '/h/.gemini/config/mcp_config.json': JSON.stringify({
      mcpServers: { other: { ...entry('kill'), args: ['run', '-P=mcp-stdio', '--allow-run=kill', '/x/server.js'] } },
    }), // not ours
    '/h/.codex/config.toml': `[mcp_servers.zaafpulse]\ncommand = "${deno}"\nargs = ["run", "-P=mcp-stdio", "--allow-run=ps,tasklist", "/app/mcp.js"]\n`,
    '/h/.gemini/settings.json': '{ "mcpServers": { "zaafpulse": { "command": "node", "args": ["/app/mcp.js"] } } }',
  };
  const read = (f) => (Object.hasOwn(files, f) ? files[f] : null);
  const names = oldRegistrations({ home, env: {}, platform: 'darwin', read });
  assert.deepEqual(names.sort(), ['Claude Code', 'Codex CLI', 'Cursor']);
  assert.deepEqual(oldRegistrations({ home, env: {}, platform: 'darwin', read: () => null }), []);
  assert.deepEqual(
    oldRegistrations({
      home,
      env: {},
      platform: 'darwin',
      read: () => {
        throw new Error('EACCES');
      },
    }),
    [],
  );
});

test('doctor names the MCP clients to register again', () => {
  const src = fs.readFileSync(path.join(__dirname, '..', 'bin', 'zaafpulse.js'), 'utf8');
  const body = src.slice(src.indexOf('async doctor()'), src.indexOf("'register-mcp'()"));
  assert.match(body, /oldRegistrations\(/);
  assert.match(body, /register-mcp/);
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

// Runs mcp.js once with an HTTP integration and one tool call; the discovery file names `port`.
async function mcpCall(dir, port, token, launch = [process.execPath, [path.join(__dirname, '..', 'mcp.js')]]) {
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
  writeDiscovery(
    path.join(dir, '.data', 'egress.json'),
    { port, token, engines: { connectors: 'go' }, lockdown: false },
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
    const child = spawn(launch[0], launch[1], {
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
  assert.ok(reply, out);
  return reply;
}

test('mcp.js: a tool call goes through the endpoint named by .data/egress.json once it proves itself', async (t) => {
  const dir = tmp(t);
  const token = egress.deriveToken('front');
  const ep = await helloServer(token);
  closeAll(t, ep.server);
  const reply = await mcpCall(dir, ep.server.address().port, token);
  assert.ok(reply.result, JSON.stringify(reply));
  assert.match(JSON.stringify(reply.result), /pong/);
  const calls = ep.seen.filter((r) => !r.url.startsWith('/hello'));
  assert.equal(calls.length, 1);
  assert.ok(ep.seen.findIndex((r) => r.url.startsWith('/hello')) < ep.seen.indexOf(calls[0]), 'proved first');
  assert.equal(calls[0].headers['x-zp-egress-token'], token);
  assert.equal(calls[0].headers['x-zp-egress-url'], 'https://api.example.test/ping');
  assert.match(calls[0].headers['x-zp-egress-purpose'], /^connectors/);
});

test('mcp.js: a process that took the port without the token never gets the token or the call', async (t) => {
  const dir = tmp(t);
  const token = egress.deriveToken('front');
  // The squatter answers /hello with a proof under a key it made up, and records everything it gets.
  const squatter = await helloServer('squatter key');
  closeAll(t, squatter.server);
  await mcpCall(dir, squatter.server.address().port, token);
  // The call went direct (api.example.test does not resolve), never to the squatter.
  assert.deepEqual(
    squatter.seen.filter((r) => !r.url.startsWith('/hello')),
    [],
  );
  assert.ok(!squatter.seen.some((r) => JSON.stringify(r).includes(token)), 'the token reached the squatter');
});

// The mcp-stdio permission set as an AI client launches it: the proof needs no run grant (no kill, no tasklist).
const hasDeno = !!process.versions.deno || require('child_process').spawnSync('deno', ['--version']).status === 0;
test(
  'mcp.js under the mcp-stdio set proves the endpoint and goes through it; the set holds no kill or tasklist',
  {
    skip: !hasDeno && 'deno is not installed',
  },
  async (t) => {
    const permissions = require('../lib/permissions');
    const run = permissions.readSets()['mcp-stdio'].run;
    assert.ok(!run.includes('kill') && !run.includes('tasklist'), `mcp-stdio run: ${run.join(', ')}`);
    const dir = tmp(t);
    const token = egress.deriveToken('front');
    const ep = await helloServer(token);
    closeAll(t, ep.server);
    const { args } = permissions.selfCommand({
      script: 'mcp.js',
      sub: 'mcp',
      set: 'mcp-stdio',
      dataDir: dir,
      env: { ...process.env, ZAAFPULSE_DATA: dir, ACC_CACHE_DIR: path.join(dir, 'cache') },
      config: {},
      approvals: {},
      gate: null,
      runtime: 'deno',
      singleFile: false,
    });
    const deno = process.versions.deno ? process.execPath : 'deno';
    const reply = await mcpCall(dir, ep.server.address().port, token, [deno, args]);
    assert.match(JSON.stringify(reply.result), /pong/, JSON.stringify(reply));
    const call = ep.seen.find((r) => !r.url.startsWith('/hello'));
    assert.ok(call && call.headers['x-zp-egress-token'] === token);
  },
);

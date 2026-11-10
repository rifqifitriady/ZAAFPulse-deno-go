// The Deno permission matrix (P2 spec D3, D4): deno.json's sets, the reason for every entry, the write and run scopes
// of a `deno run` launch, and per-permission checks that a feature works and its denied neighbour fails loudly.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const permissions = require('../lib/permissions');

const tmp = (p = 'zp-perm-') => fs.mkdtempSync(path.join(os.tmpdir(), p));
const sets = permissions.readSets();
const list = (v) => (Array.isArray(v) ? v : v && Array.isArray(v.allow) ? v.allow : []);
const PRODUCT = ['server', 'cli', 'mcp-stdio'];

test('deno.json has exactly the four sets, and nothing grants all', () => {
  assert.deepEqual(Object.keys(sets).sort(), ['cli', 'mcp-stdio', 'server', 'test']);
  assert.deepEqual([...permissions.SETS].sort(), Object.keys(sets).sort());
  assert.ok(!JSON.stringify(sets).includes('"all"'));
  const raw = require('../lib/governance/mcp-scan').parseJsonc(fs.readFileSync(permissions.CONFIG, 'utf8'));
  assert.deepEqual(Object.keys(raw), ['permissions'], 'deno.json holds only Deno config: the permission sets');
});

test('every allow-listed run and sys entry has a reason, and every reason is used', () => {
  for (const kind of ['run', 'sys']) {
    const used = new Set();
    for (const name of PRODUCT) {
      for (const e of list(sets[name][kind])) {
        assert.ok(permissions.WHY[kind][e], `${name}.${kind}: ${e} has no reason in lib/permissions.js WHY`);
        used.add(e);
      }
    }
    for (const e of Object.keys(permissions.WHY[kind])) assert.ok(used.has(e), `WHY.${kind}.${e} is in no set`);
  }
});

test('the broad entries are the same in every product set, each with a reason', () => {
  for (const name of PRODUCT) {
    const s = sets[name];
    assert.equal(s.read, true, `${name}.read`);
    assert.equal(s.write, true, `${name}.write (narrowed at launch by writeScope)`);
    assert.equal(s.env, true, `${name}.env: every name (owner decision O1)`);
    assert.deepEqual(s.net, { allow: true, deny: ['169.254.169.254', 'metadata.google.internal'] });
    assert.ok(Array.isArray(s.run) && Array.isArray(s.sys), `${name}: run and sys are allow-lists`);
  }
  for (const k of ['read', 'write', 'net', 'env']) assert.ok(permissions.WHY.broad[k], `WHY.broad.${k}`);
});

test('cli holds everything server holds; every set can call os.userInfo()', () => {
  for (const kind of ['run', 'sys']) {
    const cli = new Set(list(sets.cli[kind]));
    for (const e of list(sets.server[kind])) assert.ok(cli.has(e), `cli.${kind} lacks ${e}`);
  }
  // os.userInfo() needs uid, gid and userInfo together (lib/platform.js fullName, lib/service.js).
  for (const name of Object.keys(sets))
    for (const api of ['uid', 'gid', 'userInfo']) assert.ok(sets[name].sys.includes(api), `${name}.sys lacks ${api}`);
});

test('writeScope: the data folder and temp; server adds the agent skill and MCP client folders, never the home', () => {
  const home = tmp('zp-perm-home-');
  const data = path.join(home, 'data');
  const env = { HOME: home };
  const mcp = permissions.writeScope({ set: 'mcp-stdio', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.deepEqual(mcp, [data, '/t']);
  const server = permissions.writeScope({ set: 'server', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.ok(server.includes(path.join(home, '.claude', 'skills')));
  assert.ok(server.includes(path.join(home, '.agents', 'skills')));
  assert.ok(server.includes(path.join(home, '.kiro', 'settings')));
  assert.ok(!server.includes(home) && !server.includes('/'), 'never the home folder or the root');
  const cli = permissions.writeScope({ set: 'cli', dataDir: data, home, env, platform: 'darwin', tmp: '/t' });
  assert.ok(cli.includes(path.join(home, 'Library', 'LaunchAgents')));
  assert.ok(!server.includes(path.join(home, 'Library', 'LaunchAgents')), 'the service folders are for the CLI');
  const cache = path.join(home, 'cache');
  assert.deepEqual(
    permissions.writeScope({ set: 'mcp-stdio', dataDir: data, home, env: { ACC_CACHE_DIR: cache }, tmp: '/t' }),
    [data, '/t', cache],
  );
});

test('runScope: the set, plus the owner runtimes and ZAAFPULSE_CLAUDE_BIN for a deno run launch', () => {
  const r = permissions.runScope({
    set: 'server',
    config: { localRuntimes: [{ id: 'mine', command: ['/opt/x/bin/my-server', '--port', '9000'], port: 9000 }] },
    env: { ZAAFPULSE_CLAUDE_BIN: '/opt/claude/claude' },
  });
  assert.ok(r.includes('ps') && r.includes('/opt/x/bin/my-server') && r.includes('/opt/claude/claude'));
  assert.equal(new Set(r).size, r.length);
  assert.deepEqual(
    permissions.runScope({ set: 'mcp-stdio', env: { ZAAFPULSE_CLAUDE_BIN: '/x' } }),
    sets['mcp-stdio'].run,
  );
});

test('selfCommand: node from source, deno run with the set and computed scopes, or the single-file binary', () => {
  const data = tmp();
  const base = { script: 'mcp.js', sub: 'mcp', set: 'mcp-stdio', dataDir: data };
  const node = permissions.selfCommand({ ...base, runtime: 'node', singleFile: false });
  assert.deepEqual(node, { command: process.execPath, args: [path.join(permissions.APP_DIR, 'mcp.js')] });
  const bin = permissions.selfCommand({ ...base, runtime: 'deno', singleFile: true });
  assert.deepEqual(bin, { command: process.execPath, args: ['mcp'] });
  const run = permissions.selfCommand({ ...base, runtime: 'deno', singleFile: false });
  assert.equal(run.command, process.execPath);
  assert.deepEqual(run.args.slice(0, 5), [
    'run',
    '-q',
    '--no-prompt',
    `--config=${permissions.CONFIG}`,
    '-P=mcp-stdio',
  ]);
  assert.ok(run.args.some((a) => a.startsWith('--allow-write=') && a.includes(data)));
  assert.ok(run.args.includes(`--allow-run=${sets['mcp-stdio'].run.join(',')}`));
  assert.equal(run.args.at(-1), path.join(permissions.APP_DIR, 'mcp.js'));
  assert.ok(!run.args.includes('-A') && !run.args.includes('--allow-all'));
});

test('spawnRefused: only the empty result Deno gives for a refused program', () => {
  assert.equal(permissions.spawnRefused({}), true);
  assert.equal(permissions.spawnRefused({ status: 0 }), false);
  assert.equal(permissions.spawnRefused({ status: null, signal: null, error: new Error('ENOENT') }), false);
  assert.equal(
    permissions.spawnRefused({ error: new Error('ENOENT') }),
    false,
    'Deno: an allowed program not installed',
  );
});

test('kill: process.kill on Node, with all run access or on Windows; else the kill program, errors as process.kill', () => {
  assert.equal(permissions.kill(process.pid, 0, { deno: undefined }), true);
  const deno = (state, partial) => ({ permissions: { querySync: () => ({ state, partial }) } });
  const never = () => assert.fail('the kill program is not used here');
  assert.equal(permissions.kill(process.pid, 0, { deno: deno('granted', false), spawn: never }), true);
  assert.equal(permissions.kill(process.pid, 0, { deno: deno('prompt'), spawn: never, platform: 'win32' }), true);
  const calls = [];
  const answer = (r) => (cmd, args) => (calls.push([cmd, ...args]), r);
  const scoped = deno('granted', true);
  assert.equal(permissions.kill(4242, 'SIGSTOP', { deno: scoped, spawn: answer({ status: 0, stderr: '' }) }), true);
  permissions.kill(-4242, 'SIGTERM', { deno: scoped, spawn: answer({ status: 0 }) });
  permissions.kill(4242, 0, { deno: scoped, spawn: answer({ status: 0 }) });
  assert.deepEqual(calls, [
    ['kill', '-STOP', '--', '4242'],
    ['kill', '-TERM', '--', '-4242'],
    ['kill', '-0', '--', '4242'],
  ]);
  const thrown = (r) => {
    try {
      permissions.kill(4242, 0, { deno: scoped, spawn: answer(r) });
    } catch (err) {
      return err;
    }
    return assert.fail('no error');
  };
  assert.equal(thrown({ status: 1, stderr: 'kill: 4242: No such process\n' }).code, 'ESRCH');
  assert.equal(thrown({ status: 1, stderr: 'kill: (4242): Operation not permitted\n' }).code, 'EPERM');
  assert.match(thrown({}).message, /not allowed by this build's run permissions/);
  const enoent = Object.assign(new Error('spawnSync kill ENOENT'), { code: 'ENOENT' });
  assert.equal(thrown({ error: enoent }), enoent);
});

test('describe: the runtime and what the running process holds', () => {
  if (!process.versions.deno) return assert.deepEqual(permissions.describe(), { runtime: 'node' });
  const d = permissions.describe();
  assert.equal(d.runtime, 'deno');
  assert.equal(d.singleFile, false);
  assert.equal(d.grants.write, 'all', 'the test set has unscoped write (symlinks)');
  assert.equal(d.grants.net, 'scoped');
});

test('describe: a Linux refused /proc becomes a note', () => {
  const deno = {
    version: { deno: '2.9.8' },
    build: { standalone: true },
    permissions: { querySync: () => ({ state: 'granted' }) },
  };
  const d = permissions.describe({ deno, limits: () => ({ proc: 'refused', lsof: false, text: 'no lsof' }) });
  assert.deepEqual(d.notes, ['no lsof']);
  assert.equal(permissions.describe({ deno, limits: () => null }).notes, undefined);
});

// ---- per permission: the feature works, the denied neighbour fails loudly (spec D4) ----

const PROBE = path.join(__dirname, 'fixtures', 'permissions', 'probe.js');
const DENO = process.versions.deno ? process.execPath : 'deno';
const hasDeno = !!process.versions.deno || spawnSync('deno', ['--version'], { stdio: 'ignore' }).status === 0;
const skip = !hasDeno && 'deno is not installed';
const launch = (set, scope, op, arg) => [
  'run',
  '-q',
  '--no-prompt',
  `--config=${permissions.CONFIG}`,
  `-P=${set}`,
  `--allow-write=${scope.join(',')}`,
  PROBE,
  op,
  arg,
];

// Runs the probe as `deno run -P=<set>` with the write scope a launch of that set would get, in a throwaway home. That
// home is inside the OS temp folder, which a launch may write, so the scope's temp folder is one inside the home.
function probe(set, op, arg, { home, data, env = {} }) {
  const scope = permissions.writeScope({ set, dataDir: data, home, env: { HOME: home }, tmp: path.join(home, 'tmp') });
  const r = spawnSync(DENO, launch(set, scope, op, arg), {
    encoding: 'utf8',
    env: { PATH: process.env.PATH, HOME: home, USERPROFILE: home, ZAAFPULSE_DATA: data, ...env },
  });
  const line = String(r.stdout || '')
    .trim()
    .split('\n')
    .pop();
  assert.ok(line, `no answer from the probe: ${r.stderr}`);
  return JSON.parse(line);
}
const rig = () => {
  const home = tmp('zp-perm-home-');
  const data = path.join(home, 'data');
  fs.mkdirSync(data, { recursive: true });
  return { home, data };
};

test('write: server writes its data folder and the skill folders; the home folder is refused loudly', { skip }, () => {
  const t = rig();
  assert.deepEqual(probe('server', 'write', path.join(t.data, 'x.json'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.claude', 'skills'), { recursive: true });
  assert.deepEqual(probe('server', 'write', path.join(t.home, '.claude', 'skills', 'x'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.ssh'), { recursive: true });
  const r = probe('server', 'write', path.join(t.home, '.ssh', 'x'), t);
  assert.equal(r.ok, false);
  assert.match(r.error, /Requires write access to ".*\.ssh/);
  assert.equal(probe('server', 'write', path.join(t.home, 'x'), t).ok, false, 'the home folder itself');
});

test('write: mcp-stdio writes only its data folder, not the skill folders', { skip }, () => {
  const t = rig();
  assert.deepEqual(probe('mcp-stdio', 'write', path.join(t.data, 'x.json'), t), { ok: true });
  fs.mkdirSync(path.join(t.home, '.claude', 'skills'), { recursive: true });
  assert.match(
    probe('mcp-stdio', 'write', path.join(t.home, '.claude', 'skills', 'x'), t).error,
    /Requires write access/,
  );
});

test(
  'write: cli writes the service folder; server does not',
  { skip: skip || (process.platform !== 'darwin' && 'macOS folders') },
  () => {
    const t = rig();
    const agents = path.join(t.home, 'Library', 'LaunchAgents');
    fs.mkdirSync(agents, { recursive: true });
    assert.deepEqual(probe('cli', 'write', path.join(agents, 'x.plist'), t), { ok: true });
    assert.match(probe('server', 'write', path.join(agents, 'x.plist'), t).error, /Requires write access/);
  },
);

test('net: everything but cloud metadata, which is refused loudly', { skip }, async () => {
  const t = rig();
  const http = require('http');
  const srv = http.createServer((req, res) => res.end('ok'));
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  try {
    const url = `http://127.0.0.1:${srv.address().port}/`;
    // spawnSync would block this process and its server, so the probe runs in a child we wait for asynchronously.
    const r = await new Promise((resolve) => {
      const child = spawn(DENO, launch('server', [t.data], 'net', url), {
        env: { PATH: process.env.PATH, HOME: t.home },
      });
      let out = '';
      child.stdout.on('data', (b) => (out += b));
      child.on('close', () => resolve(JSON.parse(out.trim().split('\n').pop())));
    });
    assert.deepEqual(r, { ok: true });
  } finally {
    srv.close();
  }
  for (const set of PRODUCT)
    assert.match(
      probe(set, 'net', 'http://169.254.169.254/latest/meta-data/', t).error,
      /Requires net access to "169\.254\.169\.254/,
    );
  assert.match(
    probe('server', 'net', 'http://metadata.google.internal/', t).error,
    /Requires net access to "metadata\.google\.internal/,
  );
});

test('env: every name reads (owner decision O1), so a v1 secret reference resolves', { skip }, () => {
  const t = rig();
  const env = { ZAAFPULSE_PROBE: 'x', DT_TOKEN: 'secret-value' };
  for (const set of PRODUCT) {
    assert.deepEqual(probe(set, 'env', 'DT_TOKEN', { ...t, env }), { ok: true, value: 'set' });
    assert.deepEqual(probe(set, 'secret-env', 'DT_TOKEN', { ...t, env }), { ok: true });
  }
  const unset = probe('server', 'secret-env', 'NOT_SET_ANYWHERE', t);
  assert.match(unset.error, /environment variable NOT_SET_ANYWHERE is not set/);
});

test(
  'run: an allow-listed program runs; another is refused loudly',
  { skip: skip || (process.platform === 'win32' && 'POSIX programs') },
  () => {
    const t = rig();
    assert.deepEqual(probe('server', 'run', 'ps', t), { ok: true });
    const r = probe('server', 'run', 'curl', t);
    assert.equal(r.ok, false);
    assert.match(r.error, /Requires run access to "curl/);
    // mcp.js runs no notification, model server or CLI.
    assert.match(probe('mcp-stdio', 'run', 'osascript', t).error, /Requires run access to "osascript/);
  },
);

test(
  'signals: process.kill is refused under a run allow-list; the kill program carries them',
  { skip: skip || (process.platform === 'win32' && 'POSIX signals') },
  () => {
    const t = rig();
    const child = spawn('sleep', ['30'], { stdio: 'ignore' });
    try {
      assert.match(probe('server', 'rawkill', String(child.pid), t).error, /Requires run access/);
      assert.deepEqual(probe('server', 'kill', String(child.pid), t), { ok: true });
      assert.deepEqual(probe('cli', 'kill', String(child.pid), t), { ok: true });
    } finally {
      child.kill();
    }
  },
);

test(
  'sys: the login name in every set; network interfaces refused; hostname and the OS version per set',
  { skip },
  () => {
    const t = rig();
    for (const set of PRODUCT) assert.deepEqual(probe(set, 'sys', 'userInfo', t), { ok: true }, set);
    assert.deepEqual(probe('server', 'sys', 'homedir', t), { ok: true });
    assert.match(probe('server', 'sys', 'networkInterfaces', t).error, /Requires sys access to "networkInterfaces"/);
    assert.match(probe('mcp-stdio', 'sys', 'hostname', t).error, /Requires sys access to "hostname"/);
    assert.deepEqual(probe('cli', 'sys', 'release', t), { ok: true });
    assert.match(probe('server', 'sys', 'release', t).error, /Requires sys access to "osRelease"/);
  },
);

test('mcp.js starts under the mcp-stdio launch and answers with no permission refused', { skip }, async () => {
  const t = rig();
  fs.writeFileSync(path.join(t.data, 'config.json'), '{}');
  const { args } = permissions.selfCommand({
    script: 'mcp.js',
    sub: 'mcp',
    set: 'mcp-stdio',
    dataDir: t.data,
    env: { HOME: t.home },
    runtime: 'deno',
    singleFile: false,
  });
  const { out, err } = await new Promise((resolve) => {
    const child = spawn(DENO, args, {
      env: { PATH: process.env.PATH, HOME: t.home, USERPROFILE: t.home, ZAAFPULSE_DATA: t.data },
    });
    let o = '';
    let e = '';
    child.stdout.on('data', (b) => (o += b));
    child.stderr.on('data', (b) => (e += b));
    child.on('close', () => resolve({ out: o, err: e }));
    const msgs = [
      {
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 't', version: '0' } },
      },
      { jsonrpc: '2.0', method: 'notifications/initialized' },
      { jsonrpc: '2.0', id: 2, method: 'tools/list' },
      { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'acc_live_apps', arguments: {} } },
    ];
    for (const m of msgs) child.stdin.write(JSON.stringify(m) + '\n');
    child.stdin.end();
  });
  const byId = Object.fromEntries(
    out
      .trim()
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l))
      .map((r) => [r.id, r]),
  );
  assert.equal(byId[1].result.serverInfo.name, 'zaafpulse', err);
  assert.ok(byId[2].result.tools.length > 0);
  assert.ok(byId[3].result && !byId[3].result.isError, JSON.stringify(byId[3]));
  // On Linux the one refusal is /proc (no scoped grant reaches it): said once by lib/processes.js, which falls back to ps.
  const refused = err.split('\n').filter((l) => /NotCapable|Requires \w+ access/.test(l));
  assert.deepEqual(
    refused.filter((l) => !/^processes: \/proc(\/meminfo)? is not readable by this runtime/.test(l)),
    [],
    err,
  );
});

// v1.x MCP client entries after the v2 upgrade (P7): an entry v1.1 registered starts <prefix>/node/bin/node
// <prefix>/app/mcp.js, which the v2 installers move into <prefix>/rollback/. `zaafpulse doctor` names every client whose
// entry starts a program or script that is gone, and `zaafpulse register-mcp registered` writes every client that holds
// an entry again (keeping its data folder), and no other. Every test runs against a temp HOME with an empty environment.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const { ourEntries, missingPaths, brokenRegistrations } = require('../lib/mcp-registrations');
const { run, clients, NAME } = require('../scripts/register-mcp');

const ROOTS = [];
test.after(() => {
  for (const r of ROOTS) fs.rmSync(r, { recursive: true, force: true });
});
const tmp = () => {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-mcpreg-'));
  ROOTS.push(d);
  return d;
};
const put = (f, text) => {
  fs.mkdirSync(path.dirname(f), { recursive: true });
  fs.writeFileSync(f, text);
};
const readJ = (f) => JSON.parse(fs.readFileSync(f, 'utf8'));
// A v1.1 install's paths, after the v2 upgrade moved them (they do not exist).
const v1 = (prefix) => ({
  command: path.join(prefix, 'node', 'bin', 'node'),
  args: [path.join(prefix, 'app', 'mcp.js')],
});

test('missingPaths: an absolute command and an absolute mcp.js that are gone; bare names are not judged', () => {
  const root = tmp();
  const here = path.join(root, 'zaafpulse');
  put(here, '');
  assert.deepEqual(missingPaths({ command: here, args: ['mcp'] }), []);
  const old = v1(root);
  assert.deepEqual(missingPaths(old), [old.command, old.args[0]]);
  assert.deepEqual(missingPaths({ command: 'node', args: ['mcp.js'] }), []);
  // A deno run entry: only its mcp.js argument is a script path; the scopes and flags are not.
  const script = path.join(root, 'app', 'mcp.js');
  assert.deepEqual(missingPaths({ command: here, args: ['run', `--allow-write=${root}`, script] }), [script]);
});

test('brokenRegistrations names each client whose entry is gone, from user and project scope and TOML', () => {
  const home = tmp();
  const prefix = path.join(home, '.local', 'share', 'zaafpulse');
  const all = clients(home, { env: {} });
  const old = v1(prefix);
  put(all.cursor.file, JSON.stringify({ mcpServers: { [NAME]: { type: 'stdio', ...old }, other: { command: '/x' } } }));
  // An entry that works: its program exists.
  const bin = path.join(prefix, 'bin', 'zaafpulse');
  put(bin, '');
  put(all.kiro.file, JSON.stringify({ mcpServers: { [NAME]: { command: bin, args: ['mcp'] } } }));
  // Claude Code: a project-scoped entry under the old name.
  put(all.claude.file, JSON.stringify({ projects: { '/p': { mcpServers: { 'ai-command-center': old } } } }));
  put(
    all.codex.file,
    `[mcp_servers.${NAME}]\ncommand = ${JSON.stringify(old.command)}\nargs = [${JSON.stringify(old.args[0])}]\n`,
  );
  const got = brokenRegistrations({ home, env: {} });
  assert.deepEqual(got.map((b) => b.client).sort(), ['Claude Code', 'Codex CLI', 'Cursor']);
  for (const b of got) assert.deepEqual(b.paths, [old.command, old.args[0]], b.client);
  // The Claude Code entry is a project's own: register-mcp registered does not write it, so doctor says so.
  assert.deepEqual(got.map((b) => [b.client, b.scope]).sort(), [
    ['Claude Code', 'project'],
    ['Codex CLI', 'user'],
    ['Cursor', 'user'],
  ]);
  // Not ours: never named, even when its program is gone.
  assert.ok(!JSON.stringify(got).includes('"/x"'));
  assert.deepEqual(
    ourEntries({ home, env: {} })
      .map((x) => [x.id, x.scope])
      .sort(),
    [
      ['claude', 'project'],
      ['codex', 'user'],
      ['cursor', 'user'],
      ['kiro', 'user'],
    ],
  );
});

test('register-mcp registered: rewrites only the clients that hold an entry, keeping their data folder', () => {
  const home = tmp();
  const all = clients(home, { env: {} });
  const data = path.join(home, 'custom-data');
  fs.mkdirSync(data);
  put(
    all.cursor.file,
    JSON.stringify({ mcpServers: { [NAME]: { type: 'stdio', ...v1(home), env: { ZAAFPULSE_DATA: data } } } }),
  );
  // VS Code is installed, with no entry of ours: "registered" leaves it alone ("all" would write it).
  fs.mkdirSync(path.dirname(all.vscode.file), { recursive: true });
  const logs = [];
  run('registered', { home, log: (m) => logs.push(m) });
  const e = readJ(all.cursor.file).mcpServers[NAME];
  assert.equal(e.command, process.execPath);
  assert.match(e.args.at(-1), /mcp\.js$/);
  assert.equal(e.env.ZAAFPULSE_DATA, data, 'the data folder the old entry named is kept');
  assert.equal(e.type, 'stdio');
  assert.ok(!fs.existsSync(all.vscode.file));
  assert.deepEqual(brokenRegistrations({ home, env: {} }), []);
  // Nothing held: says so and writes nothing.
  const empty = tmp();
  const said = [];
  run('registered', { home: empty, log: (m) => said.push(m) });
  assert.match(said.join('\n'), /nothing to register again/);
  assert.deepEqual(fs.readdirSync(empty), []);
});

test('doctor names a client whose entry is gone and says how to fix it', () => {
  const home = tmp();
  const all = clients(home, { env: {} });
  put(all.cursor.file, JSON.stringify({ mcpServers: { [NAME]: v1(path.join(home, 'prefix')) } }));
  const r = spawnSync(process.execPath, [path.join(__dirname, '..', 'bin', 'zaafpulse.js'), 'doctor'], {
    encoding: 'utf8',
    timeout: 30000,
    env: {
      PATH: process.env.PATH,
      HOME: home,
      USERPROFILE: home,
      APPDATA: path.join(home, 'AppData', 'Roaming'),
      ZAAFPULSE_DATA: path.join(home, 'data'),
      ZAAFPULSE_PORT: '9',
    },
  });
  assert.equal(r.status, 0, r.stderr);
  assert.match(
    r.stdout,
    /· MCP entries\s+Cursor starts .*node.* and .*mcp\.js, which no longer exists; run `zaafpulse register-mcp registered`/,
  );
  put(all.cursor.file, JSON.stringify({ mcpServers: {} }));
  const ok = spawnSync(process.execPath, [path.join(__dirname, '..', 'bin', 'zaafpulse.js'), 'doctor'], {
    encoding: 'utf8',
    timeout: 30000,
    env: {
      PATH: process.env.PATH,
      HOME: home,
      USERPROFILE: home,
      ZAAFPULSE_DATA: path.join(home, 'data'),
      ZAAFPULSE_PORT: '9',
    },
  });
  assert.match(ok.stdout, /✓ MCP entries\s+every entry starts a program that exists/);
});

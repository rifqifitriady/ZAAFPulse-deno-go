// A user or folder name such as O'Brien or AT&T must not block a `deno run` launch (lib/permissions.js UNSAFE refuses
// only a comma, a double quote and a control character). The launch is an argument list; each file that carries it
// escapes for its own syntax. This runs one awkward path through every output: the MCP client JSON, the Codex TOML,
// the macOS plist, the systemd unit and the Windows launcher.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const permissions = require('../lib/permissions');
const service = require('../lib/service');
const { run, toml, NAME } = require('../scripts/register-mcp');

// Characters that mean something to a shell, cmd.exe, XML, TOML, systemd or a regex; none is a comma or a double quote.
const NAME_ = "O'Brien AT&T $x ^y |z <a> `b` (c);d #e !f ~g";
const HOME = path.resolve(path.sep, 'Users', NAME_);
const DATA = path.join(HOME, 'data');
const noCli = () => null;
const quiet = () => {};

const launch = (set, dataDir = DATA, env = {}) =>
  permissions.selfCommand({
    script: set === 'mcp-stdio' ? 'mcp.js' : path.join('bin', 'zaafpulse.js'),
    sub: set === 'mcp-stdio' ? 'mcp' : 'start',
    set,
    dataDir,
    env,
    config: {},
    approvals: {},
    runtime: 'deno',
    singleFile: false,
  });
const writeArg = (args) => args.find((a) => a.startsWith('--allow-write=')).slice('--allow-write='.length);

test('the launch line passes the folders as they are, with no escaping and no refusal', () => {
  for (const set of ['server', 'cli', 'mcp-stdio']) {
    const { args } = launch(set);
    const dirs = writeArg(args).split(',');
    assert.ok(dirs.includes(DATA), `${set}: the data folder is one entry, untouched`);
    assert.equal(
      dirs.length,
      permissions.writeScope({ set, dataDir: DATA, env: {} }).length,
      `${set}: no entry was split or merged`,
    );
  }
  const scope = permissions.writeScope({
    set: 'server',
    dataDir: DATA,
    home: HOME,
    env: {},
    tmp: path.join(HOME, 't'),
  });
  assert.ok(
    scope.includes(path.join(HOME, '.claude', 'skills')),
    'a home folder with ' + NAME_ + ' is granted its subfolders',
  );
  assert.ok(!scope.includes(HOME), 'never the home folder itself');
});

test('the refused characters stay refused: a comma, a double quote, a line break and the VBScript payload', () => {
  const PAYLOAD = 'C:\\x" & CreateObject("WScript.Shell").Run("calc") & "';
  for (const bad of ['/d/a,/', '/d/a"b', '/d/a\nb', '/d/a\0b', PAYLOAD])
    assert.throws(() => launch('server', bad), /--allow-write/, JSON.stringify(bad));
  assert.throws(
    () => launch('server', DATA, { ZAAFPULSE_CLAUDE_BIN: PAYLOAD }),
    (err) => /--allow-run/.test(err.message) && err.message.includes(JSON.stringify(PAYLOAD)),
  );
});

test('MCP client JSON: the data folder and the launch arguments round-trip exactly', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-chars-'));
  run('kiro', { home, env: {}, log: quiet, which: noCli, data: DATA });
  const file = path.join(home, '.kiro', 'settings', 'mcp.json');
  const entry = JSON.parse(fs.readFileSync(file, 'utf8')).mcpServers[NAME];
  assert.deepEqual(entry.env, { ZAAFPULSE_DATA: DATA });
  const want = launch('mcp-stdio');
  assert.equal(JSON.stringify(JSON.parse(JSON.stringify(want.args))), JSON.stringify(want.args));
  if (process.versions.deno) assert.ok(entry.args.some((a) => a.startsWith('--allow-write=') && a.includes(DATA)));
});

test('Codex TOML: the same arguments and folder come back from the table we write', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-chars-'));
  run('codex', { home, env: {}, log: quiet, which: noCli, data: DATA });
  const text = fs.readFileSync(path.join(home, '.codex', 'config.toml'), 'utf8');
  assert.deepEqual(toml.find(text, NAME).value.env, { ZAAFPULSE_DATA: DATA });
  const want = launch('mcp-stdio');
  const block = toml.block(NAME, { command: want.command, args: want.args, env: { ZAAFPULSE_DATA: DATA } });
  const found = toml.find(block, NAME).value;
  assert.deepEqual(found.args, want.args);
  assert.deepEqual(found.env, { ZAAFPULSE_DATA: DATA });
});

test('plist: markup characters become entities, the rest stays, and the text reads back the same', () => {
  const cmd = launch('server').args;
  const text = service.plist(['/usr/bin/deno', ...cmd], { ZAAFPULSE_DATA: DATA }, path.join(HOME, 'server.log'));
  const back = (s) => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
  const strings = [...text.matchAll(/<string>([^<]*)<\/string>/g)].map((m) => back(m[1]));
  assert.ok(strings.includes(cmd.find((a) => a.startsWith('--allow-write='))), 'the --allow-write argument');
  assert.ok(strings.includes(DATA), 'the environment value');
  assert.ok(text.includes("O'Brien AT&amp;T $x ^y |z &lt;a&gt; `b`"), text);
  assert.ok(!/<a>|AT&T/.test(text), 'no raw & or <a> from the name');
});

test('systemd unit: the argument is double-quoted, $ and % are doubled, and the single quote is quoted too', () => {
  const args = ['/usr/bin/deno', '--allow-write=' + DATA, '/app/bin/zaafpulse.js', 'start'];
  const unit = service.unitText(
    args,
    { ZAAFPULSE_DATA: DATA, XDG_CONFIG_HOME: "/home/O'Brien/.config" },
    { system: true },
  );
  const quoted = `"--allow-write=${DATA.replace(/\\/g, '\\\\').replace(/\$/g, '$$$$')}"`;
  assert.ok(unit.includes(`ExecStart=/usr/bin/deno ${quoted} /app/bin/zaafpulse.js start\n`), unit);
  assert.ok(unit.includes(`Environment="ZAAFPULSE_DATA=${DATA.replace(/\\/g, '\\\\')}"\n`), unit);
  assert.ok(unit.includes(`ReadWritePaths=${DATA}\n`), unit);
  // A word with a single quote and nothing else that needs quoting still must not reach systemd bare.
  assert.match(
    service.unitText(['/usr/bin/deno', "/o'b/x"], {}, { system: false }),
    /^ExecStart=\/usr\/bin\/deno "\/o'b\/x"$/m,
  );
  assert.match(service.unitText(['/usr/bin/deno'], { A: "it's" }, { system: false }), /^Environment="A=it's"$/m);
  // $ and % in the name are literal, not expanded.
  assert.match(service.unitText(['/x/$HOME/100%'], {}, { system: false }), /^ExecStart=\/x\/\$\$HOME\/100%%$/m);
});

test('Windows launcher: & ^ | < > ( ) and a single quote stay inside the quoted cmd.exe argument', () => {
  const dir = "C:\\Users\\O'Brien AT&T ^y |z <a> (c);d #e !f ~g `b` $x";
  const vbs = service.vbsText(
    ['C:\\Program Files\\deno\\deno.exe', '--allow-write=' + dir, 'C:\\app\\bin\\zaafpulse.js', 'start'],
    { ZAAFPULSE_DATA: dir },
    dir + '\\logs\\server.log',
  );
  assert.ok(
    vbs.includes(
      `sh.Run "cmd.exe /c """"C:\\Program Files\\deno\\deno.exe"" ""--allow-write=${dir}"" ""C:\\app\\bin\\zaafpulse.js"" ""start"" >> ""${dir}\\logs\\server.log"" 2>&1""", 0, True`,
    ),
    vbs,
  );
  assert.ok(vbs.includes(`e("ZAAFPULSE_DATA") = "${dir}"`), vbs);
});

test('Windows launcher: a quote, a %, a line break and the VBScript payload are still refused', () => {
  const PAYLOAD = 'C:\\x" & CreateObject("WScript.Shell").Run("calc") & "';
  const node = 'C:\\Program Files\\nodejs\\node.exe';
  for (const arg of [PAYLOAD, 'C:\\a"b', 'C:\\100%\\x', 'C:\\%PATH%\\x', 'C:\\a\nb', 'C:\\a\rb', 'C:\\a\0b'])
    assert.throws(
      () => service.vbsText([node, arg], {}, 'C:\\l.log'),
      (err) => err.message.includes(JSON.stringify(arg)),
    );
  assert.throws(() => service.vbsText([node, 'start'], {}, 'C:\\100%\\l.log'), /l\.log/);
});

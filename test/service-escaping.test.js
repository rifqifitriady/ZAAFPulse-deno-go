// The service files quote what they are given (security review of Task 5): the Windows launcher is VBScript that runs
// a cmd.exe line, and the systemd unit expands % specifiers and $ variables. An argument that cannot be written
// safely is refused before any file is written.
const test = require('node:test');
const assert = require('node:assert/strict');
const service = require('../lib/service');

const PAYLOAD = 'C:\\x" & CreateObject("WScript.Shell").Run("calc") & "';
const NODE = 'C:\\Program Files\\nodejs\\node.exe';

test('vbsText: an argument that would close its quotes, or that cmd.exe would expand, is refused', () => {
  for (const arg of [PAYLOAD, 'C:\\a"b\\node.exe', 'C:\\100%\\node.exe', 'C:\\%PATH%\\x', 'C:\\a\nb', 'C:\\a\0b'])
    assert.throws(
      () => service.vbsText([NODE, arg, 'start'], {}, 'C:\\Z\\logs\\server.log'),
      (err) => err.message.includes(JSON.stringify(arg)),
      JSON.stringify(arg),
    );
  assert.throws(() => service.vbsText([NODE, 'start'], {}, 'C:\\Z\\100%\\server.log'), /server\.log/);
});

test('vbsText: a Windows path with spaces, & or parentheses stays one quoted argument', () => {
  const vbs = service.vbsText(
    [NODE, 'C:\\Users\\A & B (work)\\app\\bin\\zaafpulse.js', 'start'],
    { ZAAFPULSE_DATA: 'C:\\Data "x"' },
    'C:\\Z\\logs\\server.log',
  );
  assert.ok(
    vbs.includes(
      'sh.Run "cmd.exe /c """"C:\\Program Files\\nodejs\\node.exe"" ""C:\\Users\\A & B (work)\\app\\bin\\zaafpulse.js"" ""start"" >> ""C:\\Z\\logs\\server.log"" 2>&1""", 0, True',
    ),
    vbs,
  );
  // An environment value is a VBScript string only (no cmd.exe): its quotes are doubled.
  assert.ok(vbs.includes('e("ZAAFPULSE_DATA") = "C:\\Data ""x"""'), vbs);
});

test('unitText: % and $ cannot expand in ExecStart or the environment, and a line break is refused', () => {
  const unit = service.unitText(
    ['/opt/50%/node', '/app/$HOME/zaafpulse.js', 'start'],
    { ZAAFPULSE_DATA: '/data/100%', ZAAFPULSE_CLAUDE_DIRS: '/a,/b' },
    { system: true },
  );
  assert.match(unit, /^ExecStart=\/opt\/50%%\/node \/app\/\$\$HOME\/zaafpulse\.js start$/m);
  assert.match(unit, /^Environment=ZAAFPULSE_DATA=\/data\/100%%$/m);
  assert.match(unit, /^ReadWritePaths=\/data\/100%%$/m);
  assert.match(unit, /^Environment=ZAAFPULSE_CLAUDE_DIRS=\/a,\/b$/m);
  assert.match(service.unitText(['/a b/node', 'x"y'], {}, { system: false }), /^ExecStart="\/a b\/node" "x\\"y"$/m);
  for (const bad of ['/a\nExecStartPre=/bin/sh', '/a\rb', '/a\0b'])
    assert.throws(() => service.unitText(['/usr/bin/node', bad], {}, { system: false }), /line break|control/);
  assert.throws(
    () => service.unitText(['/usr/bin/node'], { ZAAFPULSE_DATA: '/d\n[Service]' }, { system: false }),
    /ZAAFPULSE_DATA/,
  );
});

test('vbsText: a line break in an environment value cannot end the string and start a statement', () => {
  assert.throws(
    () =>
      service.vbsText(
        [NODE, 'start'],
        { ZAAFPULSE_DATA: 'C:\\d"\nCreateObject("WScript.Shell").Run "calc"\n\'' },
        'C:\\l.log',
      ),
    /line break/,
  );
});

test('Windows stop/running: the server is found by its command line, bare or quoted as vbsText writes it', () => {
  // PowerShell -match is case-insensitive; .NET and JS read these patterns alike.
  const server = new RegExp(service.WIN_SERVER_RE, 'i');
  const start = new RegExp(service.WIN_START_RE, 'i');
  const isServer = (line) => server.test(line) && start.test(line);
  const exe = 'C:\\Users\\A\\AppData\\Local\\ZAAFPulse\\bin\\zaafpulse.exe';
  const v1 = [
    'C:\\Users\\A\\AppData\\Local\\ZAAFPulse\\node\\node.exe',
    'C:\\Users\\A\\AppData\\Local\\ZAAFPulse\\app\\bin\\zaafpulse.js',
  ];
  // The command lines the launcher starts: cmd.exe /c runs `"<exe>" "start"`.
  for (const cmd of [
    [exe, 'start'],
    [...v1, 'start'],
  ]) {
    const run = service.vbsText(cmd, {}, 'C:\\Z\\logs\\server.log').match(/cmd\.exe \/c """"(.*?)"" >> /)[1];
    const line = `"${run.replace(/""/g, '"')}"`;
    assert.ok(line.endsWith('"start"'), line);
    assert.ok(isServer(line), line);
  }
  assert.ok(isServer(`${exe} start`));
  assert.ok(isServer('zaafpulse start --port 4790'));
  for (const line of [
    `"${exe}" service uninstall`,
    `"${exe}" service restart`,
    `"${exe}" status`,
    'C:\\Z\\bin\\zaafpulse-gw.exe -addr 127.0.0.1:0',
    'notepad.exe start',
  ])
    assert.ok(!isServer(line), line);
});

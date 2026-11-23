// The egress and gateway fallbacks reach an installed service (P7): a service does not inherit the shell, so a
// variable the docs give as a fallback works for a service only when lib/service.js carries it into the service file.
// ZAAFPULSE_EGRESS_LOCKDOWN=0 turns the lock-down off for the running server (the single-file build's egress client
// reads it at run time, not only the launch line), and ZAAFPULSE_GW_BIN names the gateway to start in front mode.
const test = require('node:test');
const assert = require('node:assert/strict');
const service = require('../lib/service');

const SHELL = {
  PATH: '/usr/bin:/bin',
  ZAAFPULSE_EGRESS_LOCKDOWN: '0',
  ZAAFPULSE_GW_BIN: '/opt/zp/bin/zaafpulse-gw',
  UNRELATED_SECRET: 'never carried',
};

test('settings carries the egress lock-down fallback and the gateway binary, and nothing unlisted', () => {
  const s = service.settings(SHELL);
  assert.equal(s.ZAAFPULSE_EGRESS_LOCKDOWN, '0');
  assert.equal(s.ZAAFPULSE_GW_BIN, '/opt/zp/bin/zaafpulse-gw');
  assert.equal(s.UNRELATED_SECRET, undefined);
  // Unset is not written.
  assert.deepEqual(service.settings({ PATH: '/usr/bin' }), {});
});

test('launchd: the plist sets both variables', () => {
  const text = service.plist(['/x/zaafpulse', 'start'], service.serviceEnv(SHELL, { os: 'macos' }), '/l.log');
  assert.match(text, /<key>ZAAFPULSE_EGRESS_LOCKDOWN<\/key><string>0<\/string>/);
  assert.match(text, /<key>ZAAFPULSE_GW_BIN<\/key><string>\/opt\/zp\/bin\/zaafpulse-gw<\/string>/);
  assert.doesNotMatch(text, /UNRELATED_SECRET/);
});

test('systemd: the unit sets both variables, per user and system-wide', () => {
  for (const system of [false, true]) {
    const env = service.serviceEnv(SHELL, { os: 'linux', system });
    const text = service.unitText(['/x/zaafpulse', 'start'], env, { system });
    assert.match(text, /^Environment=ZAAFPULSE_EGRESS_LOCKDOWN=0$/m, `system ${system}`);
    assert.match(text, /^Environment=ZAAFPULSE_GW_BIN=\/opt\/zp\/bin\/zaafpulse-gw$/m, `system ${system}`);
  }
});

test('Windows: the Startup launcher sets both variables', () => {
  const env = service.serviceEnv(
    { ...SHELL, PATH: 'C:\\Windows', ZAAFPULSE_GW_BIN: 'C:\\zp\\bin\\zaafpulse-gw.exe' },
    { os: 'windows' },
  );
  const text = service.vbsText(['C:\\zp\\bin\\zaafpulse.exe', 'start'], env, 'C:\\l.log');
  assert.ok(text.includes('e("ZAAFPULSE_EGRESS_LOCKDOWN") = "0"'), text);
  assert.ok(text.includes('e("ZAAFPULSE_GW_BIN") = "C:\\zp\\bin\\zaafpulse-gw.exe"'), text);
});

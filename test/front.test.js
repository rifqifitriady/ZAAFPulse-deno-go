// lib/front (P4a T5) without processes: the engine config.json asks for, the config.push front section (a go engine
// refused), where the gateway binary is, and the run grant a deno run launch gets for it.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { engineOf, frontSection, configBody, CLASSES } = require('../lib/front');
const { gwBinary, existingGwBinary } = require('../lib/front/bin');
const permissions = require('../lib/permissions');
const { check } = require('../lib/ipc/schema');
const { tmpDir } = require('./ipc-stub');

test('engineOf: "front" only when config.json asks for it; js otherwise', () => {
  assert.equal(engineOf({ gateway: { engine: 'front' } }), 'front');
  for (const c of [{}, null, { gateway: {} }, { gateway: { engine: 'js' } }, { gateway: { engine: 'go' } }])
    assert.equal(engineOf(c), 'js', JSON.stringify(c));
});

test('the config.push body with its front section is a valid v1 message; a go engine is refused', () => {
  const front = frontSection({ host: '127.0.0.1', port: 4777, upstreamPort: 53120 });
  assert.deepEqual(Object.keys(front.engines), CLASSES);
  assert.ok(Object.values(front.engines).every((e) => e === 'js'));
  assert.equal(front.tls, undefined);
  const msg = { v: 1, type: 'config.push', seq: 2, ts: 0, body: { ...configBody(front), gen: 1 } };
  assert.deepEqual(check(msg), { errors: [], unknownType: false });
  const tls = frontSection({ host: '0.0.0.0', port: 443, upstreamPort: 1, tls: { certPath: '/c', keyPath: '/k' } });
  assert.deepEqual(tls.tls, { certPath: '/c', keyPath: '/k' });
  assert.throws(
    () => frontSection({ host: 'h', port: 1, upstreamPort: 1, engines: { chat: 'go' } }),
    /engine "go" for chat is not available/,
  );
});

test('gwBinary: ZAAFPULSE_GW_BIN, else beside a single-file binary, else gw/bin in the source folder', () => {
  assert.equal(gwBinary({ env: { ZAAFPULSE_GW_BIN: '/opt/gw' } }), path.resolve('/opt/gw'));
  assert.equal(
    gwBinary({ env: {}, singleFile: true, execPath: '/usr/local/bin/zaafpulse', os: 'linux' }),
    path.join('/usr/local/bin', 'zaafpulse-gw'),
  );
  assert.equal(
    gwBinary({ env: {}, singleFile: false, appDir: '/src/zp', os: 'win32' }),
    path.join('/src/zp', 'gw', 'bin', 'zaafpulse-gw.exe'),
  );
});

test('runScope: a deno run launch is granted the gateway binary when it is there', (t) => {
  const dir = tmpDir(t, 'gwbin');
  const bin = path.join(dir, 'zaafpulse-gw');
  const env = { ZAAFPULSE_GW_BIN: bin };
  assert.equal(existingGwBinary({ env }), null);
  assert.ok(!permissions.runScope({ set: 'server', env }).includes(bin));
  fs.writeFileSync(bin, '');
  assert.equal(existingGwBinary({ env }), bin);
  assert.ok(permissions.runScope({ set: 'server', env }).includes(bin));
  assert.ok(permissions.runScope({ set: 'cli', env }).includes(bin));
  assert.ok(!permissions.runScope({ set: 'mcp-stdio', env }).includes(bin));
});

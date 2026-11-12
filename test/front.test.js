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

test('bin.js keeps the rules of lib/platform.js: the source folder and the single-file check', () => {
  const platform = require('../lib/platform');
  const name = process.platform === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw';
  assert.equal(gwBinary({ env: {} }), path.join(platform.APP_DIR, 'gw', 'bin', name));
  assert.equal(
    gwBinary({ env: {}, execPath: '/x/zaafpulse', os: 'linux' }),
    platform.isSingleFile()
      ? path.join('/x', 'zaafpulse-gw')
      : path.join(platform.APP_DIR, 'gw', 'bin', 'zaafpulse-gw'),
  );
});

// createFront with fakes: a supervisor that does nothing until the test calls its onFailed or onReady, and an internal
// listener that only counts. The main server is real, on a free port.
const http = require('http');
const net = require('net');
const { createFront } = require('../lib/front');

const freePort = () =>
  new Promise((resolve) => {
    const s = net.createServer().listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });

async function frontWithFakes(t, over = {}) {
  const port = await freePort();
  const mainServer = http.createServer((req, res) => {
    if (req.url === '/save') {
      // A save of the gateway section: the retry is asked for while this request is still answering.
      const done = new Promise((r) => res.once('close', r));
      front.reload({ after: done }).catch(() => {});
      setTimeout(() => res.end('saved'), 50);
      return;
    }
    res.end('ok');
  });
  const sups = [];
  const inners = [];
  const errors = [];
  const servings = [];
  const front = createFront({
    engine: 'front',
    host: '127.0.0.1',
    port,
    mainServer,
    bin: '/no/such/gateway',
    env: {},
    cpVersion: 'test',
    ingest: { handle: () => true },
    log: () => {},
    waitPortMs: 2000,
    onServing: (m) => servings.push(m),
    onListenError: (err) => errors.push(err),
    createSupervisor: (opts) => {
      const sup = { opts, started: 0, stopped: 0, start: () => sup.started++, reload: () => true };
      sup.status = () => ({ state: 'ready', pid: 1, gen: 1, restarts: 0, lastExit: null, lastHealth: null });
      sup.stop = async () => sup.stopped++;
      sups.push(sup);
      return sup;
    },
    internal: () => {
      const inner = { closed: 0, listen: async () => 1, close: async () => inner.closed++ };
      inners.push(inner);
      return inner;
    },
    ...over,
  });
  t.after(async () => {
    await front.stop(0);
    await new Promise((r) => mainServer.close(() => r()));
    mainServer.closeAllConnections();
  });
  await front.start();
  return { front, port, mainServer, sups, inners, errors, servings };
}
const until = async (fn, ms = 5000) => {
  const end = Date.now() + ms;
  while (!fn()) {
    if (Date.now() > end) throw new Error('timed out');
    await new Promise((r) => setTimeout(r, 10));
  }
};

test('I1: two reloads in fallback start front once; a stale supervisor cannot fall back over the new one', async (t) => {
  const f = await frontWithFakes(t);
  f.sups[0].opts.onFailed('outbox in use', { kind: 'fatal' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  const [a, b] = await Promise.all([f.front.reload(), f.front.reload()]);
  assert.deepEqual([a, b], [true, true]);
  assert.equal(f.sups.length, 2, 'one new supervisor, not two');
  assert.equal(f.front.mode, 'front');
  // The old supervisor reports a failure again: ignored, the new front keeps its internal listener.
  f.sups[0].opts.onFailed('late', { kind: 'fatal' });
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(f.front.mode, 'front');
  assert.equal(f.inners[1].closed, 0);
  assert.deepEqual(f.errors, []);
});

test('I1: a reload during the fallback port wait waits for it, then starts front once', async (t) => {
  const port = await freePort();
  // Something else (the old gateway, exiting) holds the port for 300 ms after the failure.
  const holder = net.createServer().listen(port, '127.0.0.1');
  await new Promise((r) => holder.once('listening', r));
  const f = await frontWithFakes(t, { port });
  f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
  const r = f.front.reload();
  setTimeout(() => holder.close(), 300);
  assert.equal(await r, true);
  assert.equal(f.sups.length, 2);
  assert.equal(f.front.mode, 'front');
  assert.deepEqual(f.errors, []);
});

test('I2: the save that asks for the retry gets its answer; the retry waits for it', async (t) => {
  const f = await frontWithFakes(t);
  f.sups[0].opts.onFailed('outbox in use', { kind: 'fatal' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  const r = await fetch(`http://127.0.0.1:${f.port}/save`, { method: 'POST', body: '{}' });
  assert.equal(await r.text(), 'saved');
  await until(() => f.sups.length === 2 && f.front.mode === 'front');
});

test('I2: after a crash-loop fallback a reload does not retry front (a restart does)', async (t) => {
  const f = await frontWithFakes(t);
  f.sups[0].opts.onFailed('5 restarts within 60 s', { kind: 'restarts' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  assert.equal(await f.front.reload(), false);
  assert.equal(f.sups.length, 1);
  assert.equal(f.mainServer.listening, true);
  assert.match(f.front.status().reason, /5 restarts/);
});

test('the fallback waits up to waitPortMs for the main port, then reports it taken', async (t) => {
  for (const [holdMs, ok] of [
    [400, true],
    [5000, false],
  ]) {
    const port = await freePort();
    const holder = net.createServer().listen(port, '127.0.0.1');
    await new Promise((r) => holder.once('listening', r));
    const f = await frontWithFakes(t, { port, waitPortMs: 1500 });
    const released = new Promise((r) => setTimeout(() => holder.close(r), holdMs));
    f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
    if (ok) {
      await until(() => f.servings.includes('fallback'), 5000);
      assert.equal(f.mainServer.listening, true);
      assert.deepEqual(f.errors, []);
    } else {
      await until(() => f.errors.length === 1, 5000);
      assert.equal(f.errors[0].code, 'EADDRINUSE');
      assert.equal(f.mainServer.listening, false);
    }
    holder.close();
    await released.catch(() => {});
  }
});

test('status: restartNeeded after a crash-loop fallback only (P4b entry condition 8)', async (t) => {
  const f = await frontWithFakes(t);
  assert.equal(f.front.status().restartNeeded, false);
  f.sups[0].opts.onFailed('6 restarts within 60 s', { kind: 'restarts' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  assert.equal(f.front.status().restartNeeded, true);
  const g = await frontWithFakes(t);
  g.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
  await until(() => g.front.mode === 'fallback' && g.mainServer.listening);
  assert.equal(g.front.status().restartNeeded, false);
});

test('m4: a retry whose internal listener cannot start falls back again at once', async (t) => {
  let calls = 0;
  const f = await frontWithFakes(t, {
    internal: () => {
      calls++;
      return {
        listen: async () => {
          if (calls > 1) throw new Error('listen EMFILE');
          return 1;
        },
        close: async () => {},
      };
    },
  });
  f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  assert.equal(await f.front.reload(), false);
  assert.equal(f.front.mode, 'fallback');
  assert.equal(f.mainServer.listening, true, 'the main port is served again');
  assert.match(f.front.status().reason, /front could not start again: listen EMFILE/);
  assert.equal(f.sups.length, 1, 'no supervisor was started');
});

test('T5-N4: stop() cancels a retry still waiting in the queue', async (t) => {
  const f = await frontWithFakes(t);
  f.sups[0].opts.onFailed('exit 4', { kind: 'fatal' });
  await until(() => f.front.mode === 'fallback' && f.mainServer.listening);
  let release;
  const held = new Promise((r) => (release = r));
  const retry = f.front.reload({ after: held });
  await f.front.stop(0);
  release();
  assert.equal(await retry, false);
  assert.equal(f.sups.length, 1, 'no gateway started after stop()');
});

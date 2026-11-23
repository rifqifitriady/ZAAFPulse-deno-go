// Compatibility probe: the Node APIs ZAAFPulse uses, run under Deno.
const crypto = require('node:crypto');
const os = require('node:os');
const { Worker } = require('node:worker_threads');
const { DatabaseSync } = require('node:sqlite');
const { AsyncLocalStorage } = require('node:async_hooks');
const zlib = require('node:zlib');
const cp = require('node:child_process');
const out = {};
const db = new DatabaseSync(':memory:');
db.exec('create table t(x)');
db.prepare('insert into t values (?)').run(1);
out.sqlite = db.prepare('select count(*) c from t').get().c === 1;
out.hmac = crypto.createHmac('sha256', 'k').update('x').digest('hex').length === 64;
out.scrypt = crypto.scryptSync('pw', 'salt', 32).length === 32;
const als = new AsyncLocalStorage();
als.run({ who: 'owner' }, () =>
  setTimeout(() => {
    out.als = als.getStore()?.who === 'owner';
  }, 1),
);
out.gzip = zlib.gunzipSync(zlib.gzipSync('abc')).toString() === 'abc';
out.spawn = cp.spawnSync(process.platform === 'win32' ? 'where' : 'which', ['ls']).status === 0;
const w = new Worker(
  'const { parentPort, workerData } = require("node:worker_threads"); parentPort.postMessage(workerData * 2);',
  { eval: true, workerData: 21 },
);
w.on('message', (m) => {
  out.worker = m === 42;
  w.terminate();
  setTimeout(() => console.log(JSON.stringify({ ...out, platform: os.platform(), arch: process.arch })), 20);
});

// One operation under whatever permissions this process holds; prints {"ok":true,…} or {"ok":false,"error":"…"} as
// one JSON line (test/permissions.test.js).
//   probe.js write <file> | net <url> | env <name> | run <program> | sys <api> | secret-env <name>
//            | kill <pid> (lib/permissions.js kill, signal 0) | rawkill <pid> (process.kill, signal 0)
const fs = require('fs');
const os = require('os');
const cp = require('child_process');

const SYS = {
  homedir: () => os.homedir(),
  hostname: () => os.hostname(),
  userInfo: () => os.userInfo().username,
  release: () => os.release(),
  networkInterfaces: () => os.networkInterfaces(),
};
const [op, arg] = process.argv.slice(2);

async function main() {
  if (op === 'write') fs.writeFileSync(arg, 'x');
  else if (op === 'net') await fetch(arg, { signal: AbortSignal.timeout(3000) }).then((r) => r.body && r.body.cancel());
  else if (op === 'env') return { value: process.env[arg] === undefined ? null : 'set' };
  else if (op === 'run')
    await new Promise((resolve, reject) =>
      cp.execFile(arg, ['--version'], (e) => (e && /Requires run access/.test(e.message) ? reject(e) : resolve())),
    );
  else if (op === 'sys') SYS[arg]();
  else if (op === 'kill') require('../../../lib/permissions').kill(Number(arg), 0);
  else if (op === 'rawkill') process.kill(Number(arg), 0);
  else if (op === 'secret-env') require('../../../lib/secrets').createSecrets().resolve({ env: arg }, 'probe');
  else throw new Error(`unknown op ${op}`);
  return {};
}

main().then(
  (r) => console.log(JSON.stringify({ ok: true, ...r })),
  (e) => console.log(JSON.stringify({ ok: false, error: String((e && e.message) || e).split('\n')[0] })),
);

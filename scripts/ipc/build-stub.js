// Builds the P3 gateway stub (gw/cmd/zaafpulse-gw) with the Go on PATH, for the cross-language IPC tests and
// scripts/gw-dev.js. ZP_GW_BIN names a binary built already (the CI Deno job builds it first). Returns { bin } or, when
// there is no Go, { skip: <reason> }. A build that fails throws: that is a broken gateway, not a missing tool.
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const GW_DIR = path.join(__dirname, '..', '..', 'gw');

function buildStub({ env = process.env } = {}) {
  if (env.ZP_GW_BIN) {
    if (!fs.existsSync(env.ZP_GW_BIN)) throw new Error(`ZP_GW_BIN names a file that does not exist: ${env.ZP_GW_BIN}`);
    return { bin: path.resolve(env.ZP_GW_BIN) };
  }
  const goEnv = { ...env, GOTOOLCHAIN: 'local', CGO_ENABLED: '0' };
  const v = spawnSync('go', ['version'], { encoding: 'utf8', env: goEnv });
  if (v.status !== 0)
    return { skip: 'go is not on PATH: the cross-language IPC tests build gw/cmd/zaafpulse-gw with it (CI has it)' };
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'zp-gw-bin-'));
  process.once('exit', () => fs.rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, process.platform === 'win32' ? 'zaafpulse-gw.exe' : 'zaafpulse-gw');
  const r = spawnSync('go', ['build', '-o', bin, './cmd/zaafpulse-gw'], { cwd: GW_DIR, encoding: 'utf8', env: goEnv });
  if (r.status !== 0) throw new Error(`go build of gw/cmd/zaafpulse-gw failed:\n${r.stderr || r.error}`);
  return { bin };
}

module.exports = { buildStub, GW_DIR };

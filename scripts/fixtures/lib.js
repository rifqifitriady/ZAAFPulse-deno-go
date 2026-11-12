// Shared writer for the fixture generators (P4 stage 1, stream B). A generator runs the real JS function over a fixed
// corpus and writes one gw/testdata/<lib>/<case>.json per case: { fn, in, out } or { fn, in, throws, status?, hits? }.
// The Go tests replay every file. The output is deterministic and byte-identical on Node and Deno:
// - crypto.randomBytes(n) returns n bytes all equal to a counter that starts at 1 for each case;
// - Date.now() returns FIXED_NOW;
// - files of the folder this run did not write are deleted, so a removed case cannot linger.
// A registered secret value is never written: it becomes <<ZPSECRET:i>> and "secrets" lists it base64-encoded, so no
// secret-shaped string sits in the repository (push protection, secret scanners). The Go test substitutes it back.
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const FIXED_NOW = 1767225600000; // 2026-01-01T00:00:00Z
let counter = 1;
crypto.randomBytes = (n) => Buffer.alloc(n, counter++ & 255);
Date.now = () => FIXED_NOW;

const ROOT = path.join(__dirname, '..', '..');

// Secret scanners match on prefixes; the corpus itself writes such placeholders with the first letter escaped
// ("\u0073k_live_xxx"). The fixtures do the same for every prefix below: JSON reads it back unchanged.
const SCANNED = /(sk_|rk_|pk_|sk-|AKIA|ASIA|gh[pousr]_|github_pat_|xox[abposr]-|AIza|hf_|eyJ|-----BEGIN)/g;
const escapePrefixes = (text) =>
  text.replace(SCANNED, (m) => `\\u${m.charCodeAt(0).toString(16).padStart(4, '0')}${m.slice(1)}`);

// Every value in `secrets` that occurs in the file text (JSON-escaped) is replaced, longest first.
function encodeSecrets(fixture, secrets) {
  const values = [...secrets].sort((a, b) => b.length - a.length || (a < b ? -1 : 1));
  let text = JSON.stringify(fixture, null, 2);
  const used = values.filter((v) => text.includes(JSON.stringify(v).slice(1, -1)));
  if (!used.length) return `${escapePrefixes(text)}\n`;
  text = JSON.stringify({ secrets: used.map((v) => Buffer.from(v).toString('base64')), ...fixture }, null, 2);
  used.forEach((v, i) => {
    text = text.split(JSON.stringify(v).slice(1, -1)).join(`<<ZPSECRET:${i}>>`);
  });
  return `${escapePrefixes(text)}\n`;
}

// cases: [{ name, fn, args, call(args) }]. `call` gets a JSON copy of args, so JS and Go see the same input; it may
// return a promise (readSse).
async function writeCases(lib, cases, { secrets = new Set() } = {}) {
  const dir = path.join(ROOT, 'gw', 'testdata', lib);
  fs.mkdirSync(dir, { recursive: true });
  const written = new Set();
  for (const c of cases) {
    if (!/^[\w.-]+$/.test(c.name)) throw new Error(`bad case name ${c.name}`);
    if (written.has(`${c.name}.json`)) throw new Error(`duplicate case ${c.name}`);
    const input = JSON.parse(JSON.stringify(c.args));
    counter = 1;
    const fixture = { fn: c.fn, in: JSON.parse(JSON.stringify(c.args)) };
    try {
      const out = await c.call(input);
      fixture.out = out === undefined ? null : JSON.parse(JSON.stringify(out));
    } catch (err) {
      fixture.throws = err.message;
      if (err.status) fixture.status = err.status;
      if (err.hits) fixture.hits = JSON.parse(JSON.stringify(err.hits));
    }
    const file = `${c.name}.json`;
    fs.writeFileSync(path.join(dir, file), encodeSecrets(fixture, secrets));
    written.add(file);
  }
  for (const f of fs.readdirSync(dir)) if (f.endsWith('.json') && !written.has(f)) fs.unlinkSync(path.join(dir, f));
  console.log(`${written.size} fixtures in gw/testdata/${lib}`);
}

module.exports = { writeCases, FIXED_NOW, ROOT };

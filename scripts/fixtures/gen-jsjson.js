// Fixtures for the Go engine's JavaScript JSON (gw/engine/jsjson.go, P4b): for each text, what JSON.stringify(JSON.parse
// (text)) gives, or that JSON.parse throws. The Go engine sends and answers bodies the JS gateway has parsed and written
// again, so it must read and write them the same way. Writes gw/testdata/jsjson/<n>-<name>.json as
// { fn: "roundtrip", in: <text>, out: <text> | null } (null: JSON.parse throws).
// Run: node scripts/fixtures/gen-jsjson.js (CI runs every scripts/fixtures/gen-*.js and fails on a diff).
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, '..', '..', 'gw', 'testdata', 'jsjson');
const TEXTS = [
  ['object', '{"model":"oa/m1","messages":[{"role":"user","content":"hi"}]}'],
  ['whitespace', ' \t\n{ "a" : [ 1 , 2 ] }\r\n'],
  ['integer-keys-first', '{"b":1,"2":2,"a":3,"1":4,"01":5,"-1":6,"4294967294":7,"4294967295":8}'],
  ['duplicate-key-first-place-last-value', '{"a":1,"b":2,"a":3}'],
  ['proto-key', '{"__proto__":{"x":1},"y":2}'],
  ['numbers', '[1.0,1e2,1E+2,-0,0.1,1e21,1e20,123456789012345678901,1e-7,1.5e-6,0.000001,-1.25e-10,5e-324]'],
  ['big-integer', '{"seed":12345678901234567890}'],
  ['out-of-range', '[1e400,-1e400,1e-400]'],
  ['float-shortest', '[0.30000000000000004,100.5,2.5e+25,1.7976931348623157e308]'],
  ['escapes', '"a\\"b\\\\c\\/d\\b\\f\\n\\r\\t\\u0001\\u001f\\u0041\\u00e9"'],
  ['raw-unicode', '"caf\u00e9 \u2028 \u2029 <script> & \u{1F600}"'],
  ['surrogate-pair-escape', '"\\ud83d\\ude00"'],
  ['lone-high-surrogate', '"\\ud83d"'],
  ['lone-low-surrogate', '"\\ude00x"'],
  ['literals', '[true,false,null]'],
  ['empty', '{"a":{},"b":[]}'],
  ['nested', '{"a":{"b":{"c":[1,[2,[3]]]}}}'],
  ['trailing-comma', '{"a":1,}'],
  ['single-quote', "{'a':1}"],
  ['leading-zero', '[01]'],
  ['bare-dot', '[1.]'],
  ['plus-sign', '[+1]'],
  ['nan', '[NaN]'],
  ['control-in-string', '"a\u0001b"'],
  ['bom', '\ufeff{}'],
  ['empty-text', ''],
  ['two-values', '{} {}'],
  ['bad-escape', '"\\x41"'],
  ['short-unicode-escape', '"\\u12"'],
  ['string-top', '"just a string"'],
  ['number-top', '42'],
];

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });
TEXTS.forEach(([name, text], i) => {
  let out = null;
  try {
    out = JSON.stringify(JSON.parse(text));
  } catch {
    out = null;
  }
  const file = path.join(OUT, `${String(i + 1).padStart(2, '0')}-${name}.json`);
  fs.writeFileSync(file, `${JSON.stringify({ fn: 'roundtrip', in: text, out })}\n`);
});
console.log(`gen-jsjson: ${TEXTS.length} cases in ${path.relative(process.cwd(), OUT)}`);

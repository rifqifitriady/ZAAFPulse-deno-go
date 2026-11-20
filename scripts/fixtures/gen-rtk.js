// Fixtures for the Go port of the token saver (lib/gateway/rtk.js; spec
// the rtk-go design Decisions 15-16). Writes gw/testdata/rtk/<name>.json through
// scripts/fixtures/lib.js writeCases as { fn, in, out } or { fn, in, throws }:
//   compressText [raw, hint]        -> null | { text, filter, saved }
//   pickFilter   [text, hint]       -> null | filter name
//   saveTokens   [bodyText, format] -> { body, result } (the body is given as JSON text so -0, 1e400 and repeated keys
//                                       reach JSON.parse as a client sends them)
//   compressBig  [recipe, hint]     -> null | { filter, saved, length, sha256 } (recipe: [[text, times], ...])
// A case named goHandoff-* is one the Go engine hands to the control plane; its out is the JS answer, for the record.
// gw/engine/rtk_test.go replays every file.
// Run: node scripts/fixtures/gen-rtk.js (no network, env, subprocess or sys access; writes under gw/testdata).
'use strict';
const crypto = require('crypto');
const { writeCases } = require('./lib');
const { compressText, pickFilter, saveTokens } = require('../../lib/gateway/rtk');

const E = '\x1b';
const sha256 = (s) => crypto.createHash('sha256').update(s, 'utf8').digest('hex');
const build = (recipe) => recipe.map(([t, n]) => t.repeat(n)).join('');
const lines = (n, f) => Array.from({ length: n }, (_, i) => f(i)).join('\n');

// Texts, deterministic.
const diff = (files, hunk) =>
  lines(files, (f) =>
    [
      `diff --git a/src/f${f}.js b/src/f${f}.js`,
      'index 1234567..89abcde 100644',
      `--- a/src/f${f}.js`,
      `+++ b/src/f${f}.js`,
      `@@ -1,${hunk} +1,${hunk} @@`,
      lines(hunk, (i) => `${i % 3 ? '+' : '-'}  const v${i} = ${i};`),
    ].join('\n'),
  );
const gitLog = (n) =>
  lines(n, (i) =>
    [`commit ${(i * 7919).toString(16).padStart(40, 'a')}`, 'Author: Dev <d@x.io>', `Date:   Mon Nov ${(i % 28) + 1} 10:00:00 2026 +0700`, '', `    Fix thing ${i}`, `    more detail ${i}`, ''].join('\n'),
  );
const gitStatus = (n) =>
  ['On branch main', 'Changes not staged for commit:', '  (use "git add <file>..." to update)', lines(n, (i) => `\tmodified:   src/file${i}.ts`), '', 'Untracked files:', lines(n, (i) => `\tnew${i}.md`), ''].join('\n');
const npmRun = (errs, warns, prog) =>
  [
    '> app@1.0.0 test',
    lines(prog, (i) => `Downloading pkg${i}@1.0.${i}`),
    lines(warns, (i) => `npm WARN deprecated thing${i}@1.0.0: use other`),
    lines(errs, (i) => `npm ERR! code E${i}\nnpm ERR! path /x/${i}\nnpm ERR! detail ${i}\nnpm ERR! more ${i}`),
    lines(30, (i) => `summary line ${i}`),
  ].join('\n');
const grepOut = (files, per) => lines(files * per, (i) => `src/d${i % 7}/f${Math.floor(i / per)}.js:${i + 1}:  match ${i}`);
const listing = (dirs, per) =>
  [lines(dirs * per, (i) => `./pkg/d${Math.floor(i / per)}/file${i}.ts`), lines(20, (i) => `./node_modules/x/${i}.js`), lines(5, (i) => `-rw-r--r-- 1 a b 12 src/x y${i}.js`)].join('\n');
const readOut = (n) => lines(n, (i) => `${String(i + 1).padStart(6)}\tconst line${i} = ${i};`);
const logOut = (n) =>
  ['', lines(n, (i) => `2026-11-17T10:00:${String(i % 60).padStart(2, '0')}Z INFO req ${(i * 2654435761 >>> 0).toString(16)} ${i % 5 ? 'ok' : 'retry'}`), '', '', 'end'].join('\n');
// Three letters g-z (no digit or hex run, so dedupe never merges two of them).
const word = (i) => [400, 20, 1].map((d) => 'ghijklmnopqrstuvwxyz'[Math.floor(i / d) % 20]).join('');
const deep = (n) => '['.repeat(n) + ']'.repeat(n);
const jsonDoc = (items) => JSON.stringify({ items: Array.from({ length: items }, (_, i) => ({ id: i, name: `n${i}`, tags: ['a', 'b'] })), meta: { total: items } });

// [name, raw, hint]
const COMPRESS = [
  ['under-min', 'x'.repeat(599), ''],
  ['at-min-plain', 'x'.repeat(600), ''],
  ['not-a-string', 12345, 'Bash git diff'],
  ['error-prefix', `[error] ${logOut(200)}`, ''],
  ['ansi-only', lines(40, (i) => `${E}[3${i % 8}mline ${i}${E}[0m`), ''],
  ['ansi-osc-bel', lines(40, (i) => `${E}]8;;http://x/${i}\x07link ${i}${E}]8;;\x07`), ''],
  ['ansi-osc-st', lines(40, (i) => `${E}]0;title ${i}${E}\\text ${i}`), ''],
  ['ansi-osc-unterminated', `${E}]0;title ${'y'.repeat(800)}`, ''],
  ['diff-by-head', diff(4, 80), ''],
  ['diff-by-hint', diff(2, 10).replace(/^diff --git .*$/gm, 'DIFF'), 'Bash git -C repo diff --stat'],
  ['diff-show-hint', diff(3, 30), 'Bash git show HEAD'],
  ['diff-long', diff(20, 30), ''],
  ['log-by-head', gitLog(60), ''],
  ['log-by-hint', gitLog(10), 'Bash git log --stat'],
  ['log-pipe-not-diff', gitLog(30), 'Bash git log | head; diff'],
  ['status-by-head', gitStatus(40), ''],
  ['status-by-hint', gitStatus(30), 'Bash git status'],
  ['build-npm', npmRun(3, 8, 40), 'Bash npm test'],
  ['build-many-warnings', npmRun(0, 20, 0), 'Bash cargo build'],
  ['build-by-head', `PASS a.test.js\n${npmRun(1, 2, 5)}`, ''],
  ['build-kelvin-make', npmRun(2, 2, 2), 'Bash ma\u212ae'],
  ['build-unicode-fail', lines(60, (i) => (i % 9 ? `step ${i}` : `✖ failed step ${i}`)), 'Bash pytest -q'],
  ['grep-by-head', grepOut(45, 12), ''],
  ['grep-by-hint', grepOut(3, 30), 'Grep TODO'],
  ['grep-name', grepOut(5, 15), 'grep'],
  ['grep-timestamps-not', lines(50, (i) => `10:00:${String(i % 60).padStart(2, '0')} event ${i}`), ''],
  ['listing-find', listing(50, 15), 'Bash find . -name "*.ts"'],
  ['listing-glob', listing(5, 20), 'Glob'],
  ['json-array', JSON.stringify(Array.from({ length: 30 }, (_, i) => ({ i, s: 'v'.repeat(30) }))), ''],
  ['json-doc', jsonDoc(60), 'Bash curl api'],
  ['json-long-string', JSON.stringify({ s: 'q'.repeat(2500) }), ''],
  ['json-string-2000', JSON.stringify({ s: 'q'.repeat(2000), t: 'r'.repeat(1000) }), ''],
  ['json-surrogate-at-2000', JSON.stringify(['a'.repeat(1999) + '\u{1F600}' + 'b'.repeat(100)]), ''],
  ['json-pair-before-2000', JSON.stringify(['a'.repeat(1998) + '\u{1F600}' + 'b'.repeat(100)]), ''],
  ['json-numbers', `[${'-0,1e21,1e400,0.1,123456789012345678901,'.repeat(20)}5]`, ''],
  ['json-keys', `{"b":1,"2":2,"a":3,"1":4,"b":5,"__proto__":6,"x":"${'z'.repeat(700)}"}`, ''],
  ['json-depth-12-13', JSON.stringify({ a: { b: { c: { d: { e: { f: { g: { h: { i: { j: { k: { l: { m: Array(30).fill(1) } } } } } } } } } } } }, pad: 'p'.repeat(600) }), ''],
  ['json-depth-512', deep(512), ''],
  ['json-bom-not-json', `\ufeff${JSON.stringify(Array(300).fill(1))}`, ''],
  ['json-broken', `[${'1,'.repeat(400)}`, ''],
  ['read-by-hint', readOut(300), 'Read /src/a.ts'],
  ['read-by-head', readOut(250), ''],
  ['read-cat', readOut(240), 'cat'],
  ['log-long', logOut(400), ''],
  ['log-leading-blank', `\n\n${logOut(200)}`, ''],
  ['whitespace-unicode', lines(300, (i) => (i % 4 ? `\u00a0nbsp ${i}\u2028ls` : '\ufeff \u3000')), ''],
  ['crlf', lines(200, (i) => `line ${i}\r`), ''],
  ['hint-dotted-I', diff(2, 40).replace(/^diff --git .*$/gm, 'x'), '\u0130git diff'],
  ['hint-fullwidth', 'z'.repeat(700), 'Bash \uff27\uff29\uff34 diff'],
  ['goHandoff-json-depth-513', deep(513), ''],
  ['goHandoff-json-lone-surrogate', `["\\ud800${'x'.repeat(700)}"]`, ''],
];

// [name, text, hint]
const PICK = [
  ['head-diff', 'diff --git a b', ''],
  ['head-diff-second-line', 'x\ndiff --git a b', ''],
  ['hint-diff', '', 'Bash git -C x diff'],
  ['hint-log-not-diff', '', 'git log | head; diff'],
  ['head-commit', 'commit 1234567\n', ''],
  ['head-commit-bad', 'commit 12345678z', ''],
  ['head-status', 'On branch x', ''],
  ['head-changes', 'y\nChanges to be committed:', ''],
  ['hint-build', '', 'npm test'],
  ['hint-go-test', '', 'exec go test ./...'],
  ['head-compiling', '   Compiling x v1', ''],
  ['head-tests-passed', 'a\nTests passed: 3', ''],
  ['head-grep-3', 'a.js:1:x\nb/c.ts:2-y\nd.md:3:z', ''],
  ['head-grep-2', 'a.js:1:x\nb.ts:2-y', ''],
  ['head-timestamps', '10:00:05 a\n10:00:06 b\n10:00:07 c', ''],
  ['json-ok', '[1,2]', ''],
  ['json-open', '[1,2', ''],
  ['json-bom', '\ufeff[1]', ''],
  ['json-space', '  {"a":1}  \n', ''],
  ['hint-glob', '', 'Glob'],
  ['hint-glob-exact-case', '', 'GLOB'],
  ['hint-ls', '', 'Bash ls -la'],
  ['hint-read', '', 'Read x'],
  ['hint-read-upper', '', 'READ x'],
  ['head-read-10', lines(10, (i) => `  ${i + 1}\tx`), ''],
  ['head-read-9', lines(9, (i) => `  ${i + 1}\tx`), ''],
  ['head-read-arrow', lines(10, (i) => `${i + 1}→x`), ''],
  ['head-read-blank-lines', lines(10, (i) => `\n  ${i + 1}|x`), ''],
  ['log-4001', 'x'.repeat(4001), ''],
  ['log-4000', 'x'.repeat(4000), ''],
  ['hint-dotted-I', '', '\u0130git diff'],
  ['hint-kelvin', '', 'ma\u212ae'],
  ['hint-fullwidth', '', '\uff27\uff29\uff34 diff'],
  ['hint-grep-upper', '', 'GREP'],
  ['empty', '', ''],
];

// Bodies for saveTokens.
const DIFF = diff(3, 70);
const GREP = grepOut(20, 15);
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: typeof args === 'string' ? args : JSON.stringify(args) } });
const oa = (msgs) => JSON.stringify({ model: 'oa/m1', messages: msgs });
const an = (msgs) => JSON.stringify({ model: 'an/claude-x', messages: msgs });
const asst = (...tcs) => ({ role: 'assistant', content: null, tool_calls: tcs });
const tool = (id, content) => ({ role: 'tool', tool_call_id: id, content });
const use = (id, name, input) => ({ type: 'tool_use', id, name, input });
const res = (id, content, extra = {}) => ({ type: 'tool_result', tool_use_id: id, content, ...extra });
// [name, bodyText, format]
const SAVE = [
  ['oa-tool-string', oa([{ role: 'user', content: 'go' }, asst(call('c1', 'Bash', { command: 'git diff' })), tool('c1', DIFF)]), 'openai'],
  ['oa-tool-array-content', oa([asst(call('c1', 'Bash', { command: 'git diff' })), tool('c1', [{ type: 'text', text: DIFF }, { type: 'image_url', image_url: { url: 'data:x' } }, { type: 'text', text: 'short' }, 7])]), 'openai'],
  ['oa-no-hint', oa([tool('zz', GREP)]), 'openai'],
  ['oa-hint-cmd', oa([asst(call('c1', 'shell', { cmd: 'git log' })), tool('c1', gitLog(20))]), 'openai'],
  ['oa-hint-input', oa([asst(call('c1', 'exec', { input: 'npm test' })), tool('c1', npmRun(2, 3, 10))]), 'openai'],
  ['oa-args-bad-json', oa([asst(call('c1', 'Bash', '{nope')), tool('c1', GREP)]), 'openai'],
  ['oa-args-number', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":5}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-args-object', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":{"command":"git log"}}}]},${JSON.stringify(tool('c1', gitLog(20)))}]}`, 'openai'],
  ['oa-args-array-of-json', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":["{\\"command\\":\\"git status\\"}"]}}]},${JSON.stringify(tool('c1', gitStatus(30)))}]}`, 'openai'],
  ['oa-args-toString-key-caught', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":{"toString":1}}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-name-number', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":7,"arguments":"{}"}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-name-array', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":["Ba",null,["sh"]],"arguments":"{}"}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-cmd-array', oa([asst(call('c1', 'Bash', { command: ['git', 'diff'] })), tool('c1', DIFF.replace(/^diff --git .*$/gm, 'x'))]), 'openai'],
  ['oa-cmd-infinity', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":"{\\"command\\":1e400}"}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-id-number-vs-string', `{"messages":[{"role":"assistant","tool_calls":[{"id":1,"function":{"name":"Bash","arguments":"{\\"command\\":\\"git log\\"}"}}]},{"role":"tool","tool_call_id":"1","content":${JSON.stringify(gitLog(20))}}]}`, 'openai'],
  ['oa-id-number-both', `{"messages":[{"role":"assistant","tool_calls":[{"id":1,"function":{"name":"Bash","arguments":"{\\"command\\":\\"git log\\"}"}}]},{"role":"tool","tool_call_id":1.0,"content":${JSON.stringify(gitLog(20))}}]}`, 'openai'],
  ['oa-id-negzero', `{"messages":[{"role":"assistant","tool_calls":[{"id":-0,"function":{"name":"Bash","arguments":"{\\"command\\":\\"git log\\"}"}}]},{"role":"tool","tool_call_id":0,"content":${JSON.stringify(gitLog(20))}}]}`, 'openai'],
  ['oa-id-missing-both', `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"Bash","arguments":"{\\"command\\":\\"git log\\"}"}}]},{"role":"tool","content":${JSON.stringify(gitLog(20))}}]}`, 'openai'],
  ['oa-id-object', `{"messages":[{"role":"assistant","tool_calls":[{"id":{},"function":{"name":"Bash","arguments":"{\\"command\\":\\"git log\\"}"}}]},{"role":"tool","tool_call_id":{},"content":${JSON.stringify(gitLog(20))}}]}`, 'openai'],
  ['oa-tool-before-call', oa([tool('c1', gitLog(20)), asst(call('c1', 'Bash', { command: 'git log' }))]), 'openai'],
  ['oa-two-results', oa([asst(call('a', 'Bash', { command: 'git diff' }), call('b', 'Grep', { pattern: 'x' })), tool('a', DIFF), tool('b', GREP), tool('b', 'short')]), 'openai'],
  ['oa-error-prefix', oa([tool('c1', `[error] ${GREP}`)]), 'openai'],
  ['oa-tool-calls-empty-string', `{"messages":[{"role":"assistant","tool_calls":""},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-function-string', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":"Bash"}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-message-primitives', `{"messages":["x",5,true,[],${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['oa-messages-not-array', '{"messages":{"0":{"role":"tool","content":"x"}}}', 'openai'],
  ['oa-nothing-to-save', oa([{ role: 'user', content: 'hi' }]), 'openai'],
  // One blank line dropped by the log filter: 1 char saved, savedTokens 0 (Math.round(0.25)).
  ['oa-one-char-saved', oa([tool('c1', lines(270, (i) => `item ${word(i)} zzzzzzzzzz`).replace('\n', '\n\n\n'))]), 'openai'],
  ['oa-throws-null-message', '{"messages":[null]}', 'openai'],
  ['oa-throws-tool-calls-string', '{"messages":[{"role":"assistant","tool_calls":"x"}]}', 'openai'],
  ['oa-throws-tool-calls-object', '{"messages":[{"role":"assistant","tool_calls":{}}]}', 'openai'],
  ['oa-throws-tool-calls-number', '{"messages":[{"role":"assistant","tool_calls":1}]}', 'openai'],
  ['oa-throws-tc-null', '{"messages":[{"role":"assistant","tool_calls":[null]}]}', 'openai'],
  ['oa-throws-tc-array', '{"messages":[{"role":"assistant","tool_calls":[[]]}]}', 'openai'],
  ['oa-throws-no-function', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a"}]}]}', 'openai'],
  ['oa-throws-function-null', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":null}]}]}', 'openai'],
  ['oa-throws-args-null', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"x","arguments":"null"}}]}]}', 'openai'],
  ['oa-throws-content-null-item', '{"messages":[{"role":"tool","content":[null]}]}', 'openai'],
  ['oa-throws-name-toString', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":{"toString":1},"arguments":"{}"}}]}]}', 'openai'],
  ['oa-throws-cmd-toString', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"x","arguments":"{\\"command\\":{\\"toString\\":\\"x\\"}}"}}]}]}', 'openai'],
  ['oa-throws-cmd-array-toString', '{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"x","arguments":"{\\"command\\":[{\\"toString\\":1}]}"}}]}]}', 'openai'],
  ['an-tool-result-string', an([{ role: 'assistant', content: [use('t1', 'Bash', { command: 'git diff' })] }, { role: 'user', content: [res('t1', DIFF)] }]), 'anthropic'],
  ['an-tool-result-blocks', an([{ role: 'assistant', content: [use('t1', 'Grep', { pattern: 'x' })] }, { role: 'user', content: [res('t1', [{ type: 'text', text: GREP }, { type: 'image', source: {} }, 'p'])] }]), 'anthropic'],
  ['an-is-error', an([{ role: 'user', content: [res('t1', GREP, { is_error: true })] }]), 'anthropic'],
  ['an-result-before-use', an([{ role: 'user', content: [res('t1', gitLog(20))] }, { role: 'assistant', content: [use('t1', 'Bash', { command: 'git log' })] }]), 'anthropic'],
  ['an-input-pattern', an([{ role: 'assistant', content: [use('t1', 'Grep', { pattern: 'TODO' })] }, { role: 'user', content: [res('t1', GREP)] }]), 'anthropic'],
  ['an-input-falsy', an([{ role: 'assistant', content: [use('t1', 'Bash', 0)] }, { role: 'user', content: [res('t1', GREP)] }]), 'anthropic'],
  ['an-input-string', an([{ role: 'assistant', content: [use('t1', 'Bash', 'git diff')] }, { role: 'user', content: [res('t1', GREP)] }]), 'anthropic'],
  ['an-name-missing', `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","input":{"path":"/a"}}]},{"role":"user","content":[${JSON.stringify(res('t1', readOut(300)))}]}]}`, 'anthropic'],
  ['an-content-string', an([{ role: 'user', content: GREP }]), 'anthropic'],
  ['an-primitive-blocks', `{"messages":["x",{"role":"user","content":["s",3,${JSON.stringify(res('t1', GREP))}]}]}`, 'anthropic'],
  ['an-throws-null-message', '{"messages":[null]}', 'anthropic'],
  ['an-throws-null-block', '{"messages":[{"role":"user","content":[null]}]}', 'anthropic'],
  ['an-throws-null-result-item', '{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[null]}]}]}', 'anthropic'],
  ['an-throws-name-toString', '{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":{"toString":0},"input":{}}]}]}', 'anthropic'],
  ['goHandoff-oa-args-lone-surrogate', `{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"Bash","arguments":"{\\"command\\":\\"\\\\ud800 git diff\\"}"}}]},${JSON.stringify(tool('c1', GREP))}]}`, 'openai'],
  ['goHandoff-body-lone-surrogate', `{"messages":[{"role":"tool","content":"\\ud800${'x'.repeat(700)}"}]}`, 'openai'],
];

// [name, recipe, hint]
const BIG = [
  ['big-1mib-log', [['2026-11-17 INFO line same\n', 40000]], ''],
  ['big-1mib-ansi', [[`${E}[31mred${E}[0m line\n`, 60000]], ''],
  ['big-1mib-json', [['[', 1], ['{"a":1,"b":"xyz"},', 59999], ['{"a":1,"b":"xyz"}]', 1]], ''],
  ['big-1mib-grep', [['src/a.js:1: x\n', 80000]], ''],
  ['big-at-max', [['abcdefgh\n', 1165084], ['x', 4]], ''],
  ['big-over-max', [['abcdefgh\n', 1165084], ['x', 5]], ''],
];

// The seeded corpus (mulberry32: integer math, the same on every V8).
function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const hex = (r, n) => Array.from({ length: n }, () => '0123456789abcdef'[Math.floor(r() * 16)]).join('');
const KINDS = [
  (r, i) => `diff --git a/x${i} b/x${i}`,
  (r, i) => `@@ -${i},3 +${i},4 @@`,
  (r, i) => `+ added line ${i} é`,
  (r, i) => `commit ${hex(r, 40)}`,
  (r, i) => `    message ${i}`,
  (r, i) => `\tmodified:   src/m${i % 7}.go`,
  (r, i) => ` M src/s${i}.ts`,
  (r, i) => `npm ERR! failed ${i}`,
  (r, i) => `warning: unused ${i}`,
  (r, i) => `Compiling crate${i} v0.${i}.0`,
  (r, i) => `[${i}/40] building`,
  (r, i) => `src/dir${i % 5}/file${i % 9}.js:${i}:${i % 80}: match ${i}`,
  (r, i) => `node_modules/p${i}/index.js`,
  (r, i) => `./a/b${i % 4}/c${i}.txt`,
  (r, i) => `${String(i).padStart(6)}\tcode line ${i}`,
  (r, i) => `2026-11-17T10:00:${String(i % 60).padStart(2, '0')}Z INFO request ${hex(r, 12)} done`,
  () => '',
  () => '   ',
  (r, i) => `\u00a0nbsp ${i}\u2028ls`,
  (r, i) => `\ufeffbom ${i}`,
  (r, i) => `emoji \u{1F600} ${i} ✖ fail`,
  (r, i) => `${E}[3${i % 8}mcolored ${i}${E}[0m`,
  (r, i) => `${E}]8;;http://x/${i}\x07link${E}]8;;\x07`,
  (r, i) => `PASS test/${i}.test.js`,
  (r, i) => `error: boom ${i}`,
  () => 'same repeated line',
];
const HINTS = ['', 'Bash git diff HEAD~1', 'Bash git log -5', 'Bash git status', 'Bash npm test', 'Bash cargo build', 'Grep TODO', 'grep', 'Glob **/*.ts', 'Read /src/a.ts', 'cat', 'Bash ls -R', 'Bash find . -name x', 'Bash rg foo | head', 'exec_command go test ./...', '\u0130git diff', 'Bash ma\u212ae', 'Bash \uff27\uff29\uff34 diff'];
function corpus(n, seed) {
  const r = mulberry32(seed);
  const pick = (a) => a[Math.floor(r() * a.length)];
  const out = [];
  for (let c = 0; c < n; c++) {
    const count = 5 + Math.floor(r() * 250);
    const kinds = Array.from({ length: 1 + Math.floor(r() * 4) }, () => pick(KINDS));
    const ls = [];
    let prev = '';
    let run = 0;
    for (let i = 0; i < count; i++) {
      if (run > 0) {
        ls.push(prev);
        run--;
        continue;
      }
      prev = pick(kinds)(r, i);
      if (r() < 0.05) run = Math.floor(r() * 30);
      ls.push(prev);
    }
    out.push([ls.join(r() < 0.2 ? '\r\n' : '\n'), pick(HINTS)]);
  }
  return out;
}
function jsonCorpus(n, seed) {
  const r = mulberry32(seed);
  let left = 0; // values still allowed in this document (keeps each one under ~1500 values)
  const value = (d) => {
    left--;
    const k = Math.floor(r() * (d > 14 || left <= 0 ? 3 : 6));
    if (k === 0) return Math.floor(r() * 1e6) / (r() < 0.5 ? 1 : 7);
    if (k === 1) return 'w'.repeat(Math.floor(r() * (r() < 0.1 ? 2600 : 40))) + (r() < 0.3 ? '\u{1F600}' : '');
    if (k === 2) return r() < 0.5 ? null : r() < 0.5;
    if (k === 3) return Array.from({ length: Math.floor(r() * 40) }, () => value(d + 1));
    return Object.fromEntries(Array.from({ length: Math.floor(r() * 6) }, (_, i) => [r() < 0.2 ? String(i) : `k${i}`, value(d + 1)]));
  };
  return Array.from({ length: n }, () => {
    left = 1500;
    const v = [value(0), value(1), value(1)];
    return [JSON.stringify(v, null, r() < 0.3 ? 2 : 0), ''];
  });
}

const cases = [
  ...COMPRESS.map(([name, raw, hint]) => ({ name: `compress-${name}`.replace(/^compress-goHandoff-/, 'goHandoff-compress-'), fn: 'compressText', args: [raw, hint], call: ([a, h]) => compressText(a, h) })),
  ...PICK.map(([name, text, hint]) => ({ name: `pick-${name}`, fn: 'pickFilter', args: [text, hint], call: ([t, h]) => pickFilter(t, h) })),
  ...SAVE.map(([name, text, format]) => ({
    name: `save-${name}`.replace(/^save-goHandoff-/, 'goHandoff-save-'),
    fn: 'saveTokens',
    args: [text, format],
    call: ([t, f]) => {
      const body = JSON.parse(t);
      const result = saveTokens(body, f);
      return { body, result };
    },
  })),
  ...BIG.map(([name, recipe, hint]) => ({
    name,
    fn: 'compressBig',
    args: [recipe, hint],
    call: ([rc, h]) => {
      const r = compressText(build(rc), h);
      return r && { filter: r.filter, saved: r.saved, length: r.text.length, sha256: sha256(r.text) };
    },
  })),
  ...corpus(160, 20261117).map(([raw, hint], i) => ({ name: `corpus-${String(i).padStart(3, '0')}`, fn: 'compressText', args: [raw, hint], call: ([a, h]) => compressText(a, h) })),
  ...jsonCorpus(40, 17112026).map(([raw, hint], i) => ({ name: `corpus-json-${String(i).padStart(2, '0')}`, fn: 'compressText', args: [raw, hint], call: ([a, h]) => compressText(a, h) })),
];

writeCases('rtk', cases).catch((err) => {
  console.error(err);
  process.exit(1);
});

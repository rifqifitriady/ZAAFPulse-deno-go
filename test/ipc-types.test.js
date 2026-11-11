// lib/types.js and ipc/v1 stay in step (P3 D6): every property of a typedef that crosses the boundary exists in its
// schema with a compatible type, and is required there exactly when the typedef does not mark it optional. A new
// typedef must be mapped here or listed as not in v1, so the contract cannot drift silently.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const { schemas } = require('../lib/ipc/schema');

const TYPES_JS = fs.readFileSync(path.join(__dirname, '..', 'lib', 'types.js'), 'utf8');

// typedef -> [schema (message type), path inside its body: a property name, '[]' for items, 'oneOf:N' for a branch]
const MAP = {
  UsageRecord: ['usage.report', ['record']],
  GuardrailOutcome: ['usage.report', ['record', 'guard']],
  GuardrailHit: ['usage.report', ['record', 'guard', 'hits', '[]']],
  BudgetSnapshot: ['budget.snapshot', []],
  BudgetView: ['budget.snapshot', ['budgets', '[]']],
  AuditEvent: ['audit.event', ['oneOf:0', 'event']],
  AuditTarget: ['audit.event', ['oneOf:0', 'event', 'target']],
  GatewayConfigSection: ['config.push', ['gateway']],
  GatewayProviderConfig: ['config.push', ['gateway', 'providers', '[]']],
  GatewayRouteConfig: ['config.push', ['gateway', 'routes', '[]']],
  GuardrailsPolicy: ['config.push', ['gateway', 'guardrails']],
  GatewayKeyConfig: ['config.push', ['keys', '[]']],
};
const NOT_IN_V1 = {
  Money: 'a scalar: integer nano-USD, written "type": "integer" where it appears',
  BudgetCheckInput: 'asked inside the gateway; no v1 message carries it',
  BudgetRefusal: 'answered inside the gateway as a 402; no v1 message carries it',
};
// Properties a schema deliberately does not carry where the typedef has them.
const MOVED = {
  'GatewayConfigSection.keys': 'sent as config.push keys (GatewayKeyConfig plus hash), checked as GatewayKeyConfig',
  'GatewayProviderConfig.apiKey':
    'a secret reference: the control plane resolves it and sends the value in secrets.handoff',
};

function typedefs(text) {
  const out = {};
  for (const block of text.match(/\/\*\*[\s\S]*?\*\//g) || []) {
    const head = block.match(/@typedef\s+\{[^}]+\}\s+(\w+)/);
    if (!head) continue;
    const props = [];
    for (const m of block.matchAll(/@property\s+\{(.+?)\}\s+(\[?)([\w$]+)\]?/g))
      props.push({ type: m[1], optional: m[2] === '[', name: m[3] });
    out[head[1]] = props;
  }
  return out;
}

function splitTop(s) {
  const parts = [];
  let depth = 0;
  let cur = '';
  for (const c of s) {
    if ('{<('.includes(c)) depth++;
    else if ('}>)'.includes(c)) depth--;
    if (c === '|' && depth === 0) {
      parts.push(cur.trim());
      cur = '';
    } else cur += c;
  }
  parts.push(cur.trim());
  return parts;
}

function jsdocKinds(type) {
  const kinds = new Set();
  for (const part of splitTop(type)) {
    if (part === 'string' || /^'.*'$/.test(part)) kinds.add('string');
    else if (part === 'number') kinds.add('number');
    else if (part === 'Money') kinds.add('integer');
    else if (part === 'boolean' || part === 'true' || part === 'false') kinds.add('boolean');
    else if (part === 'null') kinds.add('null');
    else if (part === 'unknown') kinds.add('any');
    else if (part.endsWith('[]') || part.startsWith('Array<')) kinds.add('array');
    else if (part.startsWith('{') || /^(Record|Partial)</.test(part) || /^[A-Z]\w*$/.test(part)) kinds.add('object');
    else throw new Error(`lib/types.js: a type this test cannot read: ${part}`);
  }
  return kinds;
}

function schemaKinds(node) {
  if (node.type) return new Set(Array.isArray(node.type) ? node.type : [node.type]);
  if (node.oneOf) return new Set(node.oneOf.flatMap((b) => [...schemaKinds(b)]));
  if (Object.hasOwn(node, 'const')) return new Set([node.const === null ? 'null' : typeof node.const]);
  return new Set(['any']);
}

function compatible(jsdoc, schema) {
  if (schema.has('any')) return true;
  for (const k of jsdoc) {
    if (k === 'any') continue;
    if (k === 'number' || k === 'integer') {
      if (!schema.has('number') && !schema.has('integer')) return false;
    } else if (!schema.has(k)) return false;
  }
  return true;
}

function resolve(schema, steps) {
  let node = schema;
  for (const s of steps) {
    if (s === '[]') node = node.items;
    else if (s.startsWith('oneOf:')) node = node.oneOf[Number(s.slice(6))];
    else node = node.properties && node.properties[s];
    if (!node) throw new Error(`no schema node at ${steps.join(' > ')}`);
  }
  return node;
}

const defs = typedefs(TYPES_JS);

test('the parser reads lib/types.js', () => {
  assert.ok(defs.UsageRecord.some((p) => p.name === 'cost' && p.type === 'number' && !p.optional));
  assert.ok(defs.BudgetView.some((p) => p.name === 'scope' && p.type === "'global'|{ dim: string, value: string }"));
  assert.ok(defs.AuditEvent.some((p) => p.name === 'actor' && p.optional));
});

test('every typedef in lib/types.js is mapped to a schema or listed as not in v1', () => {
  const unaccounted = Object.keys(defs).filter((n) => !MAP[n] && !NOT_IN_V1[n]);
  assert.deepEqual(unaccounted, []);
  for (const n of [...Object.keys(MAP), ...Object.keys(NOT_IN_V1)]) assert.ok(defs[n], `${n} is a typedef`);
});

for (const [name, [type, steps]] of Object.entries(MAP)) {
  test(`${name} matches ${type} ${steps.join(' > ') || '(body)'}`, () => {
    const node = resolve(schemas()[type], steps);
    for (const p of defs[name]) {
      if (MOVED[`${name}.${p.name}`]) {
        assert.ok(!node.properties[p.name], `${name}.${p.name} is not in the schema (${MOVED[`${name}.${p.name}`]})`);
        continue;
      }
      const prop = node.properties[p.name];
      assert.ok(prop, `${name}.${p.name} is in the schema`);
      const j = jsdocKinds(p.type);
      const s = schemaKinds(prop);
      assert.ok(compatible(j, s), `${name}.${p.name}: typedef {${p.type}} vs schema ${[...s].join('|')}`);
      const required = (node.required || []).includes(p.name);
      assert.equal(required, !p.optional, `${name}.${p.name} is ${p.optional ? 'optional' : 'required'} in both`);
    }
  });
}

test('config.push keys carry every GatewayKeyConfig property plus the SHA-256 hash, and no secret reference', () => {
  const key = resolve(schemas()['config.push'], ['keys', '[]']);
  assert.deepEqual(Object.keys(key.properties).sort(), [...defs.GatewayKeyConfig.map((p) => p.name), 'hash'].sort());
  assert.equal(key.properties.hash.pattern, '^[0-9a-f]{64}$');
  const provider = resolve(schemas()['config.push'], ['gateway', 'providers', '[]']);
  assert.equal(provider.properties.apiKey, undefined);
});

test('Money crosses as an integer: usage cost and every price', () => {
  const s = schemas();
  assert.equal(resolve(s['usage.report'], ['record', 'cost']).type, 'integer');
  const price = s['config.push'].properties.pricing.additionalProperties;
  for (const k of ['input', 'output', 'cacheRead', 'cacheWrite5m', 'cacheWrite1h', 'webSearch'])
    assert.equal(price.properties[k].type, 'integer', k);
});

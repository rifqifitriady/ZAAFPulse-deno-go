// The IPC v1 contract on the control-plane side (P3 D6): the JSON Schemas in ipc/v1 and a validator for the subset of
// JSON Schema they use (type, const, enum, properties, required, additionalProperties, items, minimum, maximum,
// minLength, minItems, pattern, oneOf). No dependency. The schemas are strict (additionalProperties: false); the
// production reader is lenient ({ lenient: true }): an unknown field is ignored, as the version rule wants (D2).
const fs = require('fs');
const path = require('path');

const PROTOCOL = 1;
const DIR = path.join(__dirname, '..', '..', 'ipc', 'v1');
const TYPES = [
  'hello',
  'hello.ok',
  'hello.refused',
  'config.push',
  'secrets.handoff',
  'budget.snapshot',
  'shutdown',
  'ack',
  'ready',
  'health',
  'usage.report',
  'audit.event',
  'trace.span',
  'error',
];
// The gateway's events: written to its outbox first, carrying the stream's durable seq, acknowledged once stored.
const EVENTS = ['usage.report', 'audit.event', 'trace.span'];
const KEYWORDS = new Set([
  '$schema',
  'title',
  'description',
  'type',
  'const',
  'enum',
  'properties',
  'required',
  'additionalProperties',
  'items',
  'minimum',
  'maximum',
  'minLength',
  'minItems',
  'pattern',
  'oneOf',
]);

const kindOf = (v) =>
  v === null
    ? 'null'
    : Array.isArray(v)
      ? 'array'
      : typeof v === 'number'
        ? Number.isInteger(v)
          ? 'integer'
          : 'number'
        : typeof v;
const typeMatches = (t, v) => t === kindOf(v) || (t === 'number' && kindOf(v) === 'integer');

/**
 * The errors of `value` against `schema` ([] when it is valid). `lenient` ignores additionalProperties: false.
 * @param {Record<string, any>} schema
 * @param {unknown} value
 * @param {{ lenient?: boolean }} [opts]
 * @param {string} [at]
 * @returns {string[]}
 */
function validate(schema, value, opts = {}, at = '$') {
  if (schema.type !== undefined) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.some((t) => typeMatches(t, value))) return [`${at}: must be ${types.join(' or ')}`];
  }
  const errors = [];
  if (Object.hasOwn(schema, 'const') && value !== schema.const)
    errors.push(`${at}: must be ${JSON.stringify(schema.const)}`);
  if (schema.enum && !schema.enum.includes(value))
    errors.push(`${at}: must be one of ${schema.enum.map((x) => JSON.stringify(x)).join(', ')}`);
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum)
      errors.push(`${at}: must be at least ${schema.minimum}`);
    if (schema.maximum !== undefined && value > schema.maximum) errors.push(`${at}: must be at most ${schema.maximum}`);
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength)
      errors.push(`${at}: must be at least ${schema.minLength} characters`);
    if (schema.pattern !== undefined && !new RegExp(schema.pattern, 'u').test(value))
      errors.push(`${at}: must match ${schema.pattern}`);
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined && value.length < schema.minItems)
      errors.push(`${at}: must have at least ${schema.minItems} items`);
    if (schema.items) value.forEach((x, i) => errors.push(...validate(schema.items, x, opts, `${at}[${i}]`)));
  }
  if (kindOf(value) === 'object') {
    const obj = /** @type {Record<string, unknown>} */ (value);
    for (const r of schema.required || []) if (!Object.hasOwn(obj, r)) errors.push(`${at}.${r}: required`);
    const props = schema.properties || {};
    const extra = schema.additionalProperties;
    for (const [k, v] of Object.entries(obj)) {
      if (Object.hasOwn(props, k)) errors.push(...validate(props[k], v, opts, `${at}.${k}`));
      else if (extra === false) {
        if (!opts.lenient) errors.push(`${at}.${k}: unknown field`);
      } else if (extra && typeof extra === 'object') errors.push(...validate(extra, v, opts, `${at}.${k}`));
    }
  }
  if (schema.oneOf) {
    const matching = schema.oneOf.filter((s) => validate(s, value, opts, at).length === 0).length;
    if (matching !== 1)
      errors.push(`${at}: must match exactly one of ${schema.oneOf.length} shapes (matches ${matching})`);
  }
  return errors;
}

/**
 * The keywords of `schema` (and its subschemas) that validate() does not implement, as "<path>: <keyword>".
 * @param {Record<string, any>} schema
 * @param {string} [at]
 * @returns {string[]}
 */
function unsupported(schema, at = '$') {
  const out = Object.keys(schema)
    .filter((k) => !KEYWORDS.has(k))
    .map((k) => `${at}: ${k}`);
  for (const [k, s] of Object.entries(schema.properties || {})) out.push(...unsupported(s, `${at}.${k}`));
  if (schema.items) out.push(...unsupported(schema.items, `${at}[]`));
  if (schema.additionalProperties && typeof schema.additionalProperties === 'object')
    out.push(...unsupported(schema.additionalProperties, `${at}.*`));
  (schema.oneOf || []).forEach((s, i) => out.push(...unsupported(s, `${at}/oneOf/${i}`)));
  return out;
}

let cache = null;
/** @returns {Record<string, Record<string, any>>} the envelope schema and one schema per message type */
function schemas() {
  if (cache) return cache;
  const load = (name) => JSON.parse(fs.readFileSync(path.join(DIR, `${name}.schema.json`), 'utf8'));
  const map = { envelope: load('envelope') };
  for (const t of TYPES) map[t] = load(t);
  return (cache = map);
}

/**
 * A whole message: the envelope, then the body against its type's schema. An envelope that is valid but names a type
 * v1 does not know is `unknownType` (the caller logs it once and drops it, D2), not an error.
 * @param {unknown} msg
 * @param {{ lenient?: boolean }} [opts]
 * @returns {{ errors: string[], unknownType: boolean }}
 */
function check(msg, { lenient = false } = {}) {
  const s = schemas();
  const errors = validate(s.envelope, msg, { lenient });
  if (errors.length) return { errors, unknownType: false };
  const m = /** @type {{ type: string, body: unknown }} */ (msg);
  if (!TYPES.includes(m.type)) return { errors: [], unknownType: true };
  return { errors: validate(s[m.type], m.body, { lenient }, '$.body'), unknownType: false };
}

module.exports = { PROTOCOL, DIR, TYPES, EVENTS, validate, unsupported, schemas, check };

// The config.json-to-IPC converter (P4b): what the control plane pushes to the Go gateway in front, built fresh for
// every generation from the same sources the JS gateway reads, so both engines decide from the same state.
//   configPush:  config.json `gateway` (providers without secrets, routes, guardrails, defaults, tokenSaver, modelMap),
//                keys.json (each key with the SHA-256 hex of its secret, never the secret), and the effective price
//                table (config.pricing: defaults, live prices and the owner's own) in nano-USD per 1M tokens;
//   secretsPush: each provider's resolved stored credential and the mask set (every resolved value of 6+ characters,
//                as lib/secrets.js mask() hides them): the key of a key-auth provider, and for token auth (P4g) the
//                OAuth2 client secret or its {client_secret, refresh_token} JSON, or the service account JSON, from
//                which the gateway fetches its own tokens; a provider whose credential does not resolve is left out,
//                and the gateway hands its calls to the control plane, which gives the same error it gives today;
//   snapshotPush: the budgets the guard enforces (enforce "block"), evaluated without a forecast, and each key's cost
//                center (lib/cost/guard.js reads both).
// Pure apart from the resolver and the budget service it is handed. No token is handed over: the gateway refreshes
// its own (P4g spec Decisions 8, 9).
const { toNano } = require('../money');
const { providerSpanAttrs } = require('../otel');
const { switches } = require('../egress');

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v) => (typeof v === 'string' ? v : '');
const strings = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []);
const MODES = {
  secrets: ['off', 'log', 'redact', 'block'],
  pii: ['off', 'log', 'redact', 'block'],
  injection: ['off', 'log', 'block'],
};
const int = (v) => (Number.isSafeInteger(v) && v >= 0 ? v : null);

// lib/gateway/guardrails.js resolvePolicy(cfg, null), kept here so that this file does not pull the whole guardrails
// module into the type check of lib/front (test/front-push.test.js checks that both give the same policy).
const POLICY_DEFAULTS = { scope: 'external', secrets: 'redact', pii: 'off', injection: 'log' };
function globalPolicy(cfg) {
  const c = isObj(cfg) ? cfg : {};
  const out = { scope: c.scope === 'external' || c.scope === 'all' ? c.scope : POLICY_DEFAULTS.scope };
  for (const k of Object.keys(MODES)) out[k] = MODES[k].includes(c[k]) ? c[k] : POLICY_DEFAULTS[k];
  return out;
}

/** A provider as config.push carries it: no apiKey reference; auth { type: 'key' } when it has one. */
function provider(p) {
  const out = { id: str(p.id), preset: str(p.preset), baseUrl: str(p.baseUrl) };
  // A credential in the base URL (user:password@, a query) stays out of config.push (P4b review m5): the provider goes
  // disabled, with its origin and path only, so the gateway hands its calls to the server, which calls it as before.
  let unsafe = false;
  try {
    const u = new URL(out.baseUrl);
    if (u.username || u.password || u.search || u.hash) {
      out.baseUrl = `${u.origin}${u.pathname}`.replace(/\/+$/, '');
      unsafe = true;
    }
  } catch {
    out.baseUrl = '';
    unsafe = true;
  }
  if (Array.isArray(p.models)) out.models = strings(p.models);
  if (isObj(p.aliases))
    out.aliases = Object.fromEntries(Object.entries(p.aliases).filter(([, v]) => typeof v === 'string'));
  for (const k of ['enabled', 'approved', 'responses', 'reasoning']) if (typeof p[k] === 'boolean') out[k] = p[k];
  if (unsafe) out.enabled = false;
  // providers.js tierOf takes any truthy tier, and only "private" is private (final review I4).
  if (p.tier) out.tier = p.tier === 'private' ? 'private' : 'external';
  if (p.format === 'openai' || p.format === 'anthropic') out.format = p.format;
  if (p.tokenParam === 'max_tokens' || p.tokenParam === 'max_completion_tokens') out.tokenParam = p.tokenParam;
  if (isObj(p.auth) && typeof p.auth.type === 'string' && p.auth.type) {
    const a = { type: p.auth.type };
    for (const k of ['tokenUrl', 'clientId', 'scope']) if (typeof p.auth[k] === 'string') a[k] = p.auth[k];
    if (p.auth.grant === 'client_credentials' || p.auth.grant === 'refresh_token') a.grant = p.auth.grant;
    out.auth = a;
  } else if (p.apiKey != null) out.auth = { type: 'key' };
  // The model span's provider attributes (P4f D4), from the raw entry as lib/otel.js modelSpan reads it.
  out.span = providerSpanAttrs(p, out.id);
  return out;
}

function route(r) {
  const out = { name: str(r.name), targets: strings(r.targets) };
  if (r.strategy === 'fallback' || r.strategy === 'round-robin') out.strategy = r.strategy;
  if (typeof r.description === 'string') out.description = r.description;
  return out;
}

/**
 * A keys.json key as the gateway enforces it (ipc/v1 config.push keys[]), read the way lib/gateway/keys.js reads it, or
 * null when a field would not mean the same to the gateway: the gateway then does not know the key and hands its calls
 * to the server, which decides as it always has (P4b review I3: the gateway is never more permissive than JS).
 *   scopes, models, agents: arrays (allows() reads String(pattern); a missing list allows everything);
 *   rpm: falsy is 60 (`k.rpm || 60`), a fraction is rounded up (`w.length >= 2.5` is `>= 3`);
 *   expiresAt: falsy never expires, a fraction is rounded down (`now > x.5` is `now > x`), 0.x is -1;
 *   revokedAt: revoked whenever it reads as true; budgetUSD: a number or null; privateOnly: truthy.
 */
function keyAuth(k) {
  if (!isObj(k) || typeof k.id !== 'string' || !k.id || !/^[0-9a-f]{64}$/.test(k.hash || '')) return null;
  const list = (v) => (v == null ? [] : Array.isArray(v) ? v.map(String) : null);
  const models = list(k.models);
  const agents = list(k.agents);
  if (!Array.isArray(k.scopes) || !models || !agents) return null;
  let rpm = 0;
  if (k.rpm) {
    if (typeof k.rpm !== 'number' || !Number.isFinite(k.rpm) || k.rpm < 0) return null;
    rpm = Math.min(Math.ceil(k.rpm), Number.MAX_SAFE_INTEGER); // past it, unlimited in both
  }
  let expiresAt = null;
  if (k.expiresAt) {
    if (typeof k.expiresAt !== 'number' || !Number.isFinite(k.expiresAt)) return null;
    // Clamped to a safe integer, so config.push stays decodable: a later expiry than that never comes, an earlier one
    // has passed (re-review N2).
    expiresAt = Math.max(-Number.MAX_SAFE_INTEGER, Math.min(Number.MAX_SAFE_INTEGER, Math.floor(k.expiresAt))) || -1;
  }
  if (k.budgetUSD != null && !Number.isFinite(k.budgetUSD)) return null;
  const guardrails = {};
  if (isObj(k.guardrails)) {
    if (k.guardrails.scope === 'external' || k.guardrails.scope === 'all') guardrails.scope = k.guardrails.scope;
    for (const c of Object.keys(MODES)) if (MODES[c].includes(k.guardrails[c])) guardrails[c] = k.guardrails[c];
  }
  return {
    id: k.id,
    name: str(k.name),
    scopes: strings(k.scopes),
    models,
    agents,
    knowledge: strings(k.knowledge),
    budgetUSD: k.budgetUSD == null ? null : k.budgetUSD,
    rpm,
    privateOnly: !!k.privateOnly,
    expiresAt,
    // Any value JS reads as true is a revocation, Infinity included (re-review N2).
    revokedAt: k.revokedAt
      ? Number.isSafeInteger(Math.floor(Number(k.revokedAt)))
        ? Math.max(1, Math.floor(Number(k.revokedAt)))
        : 1
      : null,
    guardrails,
    prefix: str(k.prefix),
    approveOwnRuns: k.approveOwnRuns === true,
    createdAt: int(k.createdAt) ?? 0,
    lastUsedAt: int(k.lastUsedAt),
    hash: k.hash,
  };
}

const PRICE_FIELDS = ['input', 'output', 'cacheRead', 'cacheWrite5m', 'cacheWrite1h'];
const nano = (v) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? toNano(v) : 0);
/** One price entry in nano-USD per 1M tokens (web search: per search); fields that are not prices are dropped. */
function price(e) {
  const out = { input: nano(e.input), output: nano(e.output) };
  for (const k of PRICE_FIELDS.slice(2)) if (typeof e[k] === 'number') out[k] = nano(e[k]);
  if (typeof e.webSearch === 'number') out.webSearch = nano(e.webSearch);
  if (Array.isArray(e.tiers))
    out.tiers = e.tiers.filter(isObj).map((t) => {
      const r = { above: Math.max(0, Math.floor(Number(t.above) || 0)), input: nano(t.input), output: nano(t.output) };
      for (const k of PRICE_FIELDS.slice(2)) if (typeof t[k] === 'number') r[k] = nano(t[k]);
      return r;
    });
  if (typeof e.estimate === 'boolean') out.estimate = e.estimate;
  return out;
}

// The keys the gateway gets. A key the push drops takes every key with its hash along (re-review N3): JS matches the
// first key with a hash (Buffer.from(hash, 'hex') reads upper case too), so a later one must not answer in its place.
function pushedKeys(keys) {
  const list = (Array.isArray(keys) ? keys : []).map((k) => [k, keyAuth(k)]);
  const refused = new Set();
  // The bytes JS compares: Buffer.from(hash, 'hex') reads upper case and stops at the first non-hex character (final
  // review m3'), so "<64 hex>zz" is the same key as "<64 hex>".
  for (const [k, a] of list)
    if (!a && isObj(k) && k.hash != null) refused.add(Buffer.from(String(k.hash), 'hex').toString('hex'));
  return list.map(([, a]) => a).filter((a) => a && !refused.has(a.hash));
}

/**
 * The config.push egress section (P5 Decisions 11, 13, 19): the allow-list (empty until P5c fills it from config), its
 * mode (report until then) and each caller class's switch from config.json `egress.classes` (direct, check or go;
 * anything else is direct), which the gateway only shows.
 * @param {any} egress config.json's egress section
 */
function egressSection(egress) {
  const e = isObj(egress) ? egress : {};
  return { allow: [], allowList: 'report', classes: switches(e.classes) };
}

/**
 * The config.push body without gen and front (the supervisor adds gen, lib/front adds front). trace: whether a span is
 * recorded anywhere (P4f D8: the local store on, or OTLP export on); left out, the gateway emits no span. egress: the
 * egress section (egressSection of config.json's egress), always present.
 * @param {{ gateway: any, keys: any[], pricing: Record<string, any>, trace?: { spans?: unknown }, egress?: any }} src
 */
function configPush({ gateway, keys, pricing, trace, egress }) {
  const g = isObj(gateway) ? gateway : {};
  const d = isObj(g.defaults) ? g.defaults : {};
  return {
    gateway: {
      providers: (Array.isArray(g.providers) ? g.providers : []).filter(isObj).map(provider),
      routes: (Array.isArray(g.routes) ? g.routes : []).filter(isObj).map(route),
      guardrails: globalPolicy(g.guardrails),
      defaults: { model: str(d.model), plannerModel: str(d.plannerModel), codeModel: str(d.codeModel) },
      tokenSaver: g.tokenSaver !== false,
      modelMap: (Array.isArray(g.modelMap) ? g.modelMap : [])
        .filter(isObj)
        .map((m) => ({ match: str(m.match), to: str(m.to) })),
    },
    keys: pushedKeys(keys),
    pricing: Object.fromEntries(
      Object.entries(isObj(pricing) ? pricing : {})
        .filter(([, e]) => isObj(e))
        .map(([k, e]) => [k, price(e)]),
    ),
    ...(isObj(trace) ? { trace: { spans: trace.spans === true } } : {}),
    egress: egressSection(egress),
  };
}

/**
 * The ratelimit.window bodies of a new session (P4f D16): the takes per key, packed so that no body passes about 1 MiB
 * of JSON (a key's own list is never split; at most 10,000 takes, about 150 KB).
 * @param {{ keyId: string, at: number[] }[]} windows
 */
function windowPush(windows) {
  const bodies = [];
  let cur = [];
  let size = 0;
  for (const w of Array.isArray(windows) ? windows : []) {
    if (!isObj(w) || typeof w.keyId !== 'string' || !w.keyId || !Array.isArray(w.at)) continue;
    const item = { keyId: w.keyId, at: w.at.filter((x) => Number.isSafeInteger(x) && x >= 0) };
    const n = JSON.stringify(item).length + 1;
    if (cur.length && size + n > 1024 * 1024) {
      bodies.push({ windows: cur });
      cur = [];
      size = 0;
    }
    cur.push(item);
    size += n;
  }
  if (cur.length) bodies.push({ windows: cur });
  return bodies;
}

/**
 * The route.state body of a new session (P4h Decision 3): providers.js routeState(), with the entries the schema
 * refuses left out (a name or provider id that is not a string, a counter that is not a safe integer ≥ 0, an until that
 * is not a finite number ≥ 0).
 * @param {{ rr?: { name: string, n: number }[], cooldown?: { providerId: string, until: number, strikes: number,
 *   reason: string }[] }} state
 */
function routeStatePush(state) {
  const st = isObj(state) ? state : {};
  const nat = (x) => Number.isSafeInteger(x) && x >= 0;
  return {
    rr: (Array.isArray(st.rr) ? st.rr : [])
      .filter((x) => isObj(x) && typeof x.name === 'string' && x.name && nat(x.n))
      .map((x) => ({ name: x.name, n: x.n })),
    cooldown: (Array.isArray(st.cooldown) ? st.cooldown : [])
      .filter(
        (c) =>
          isObj(c) &&
          typeof c.providerId === 'string' &&
          c.providerId &&
          Number.isFinite(c.until) &&
          c.until >= 0 &&
          nat(c.strikes),
      )
      .map((c) => ({ providerId: c.providerId, until: c.until, strikes: c.strikes, reason: String(c.reason ?? '') })),
  };
}

/**
 * The secrets.handoff body without gen and frontToken.
 * @param {{ gateway: any, resolve: (ref: any, field: string) => string | null }} src
 */
function secretsPush({ gateway, resolve }) {
  const providers = [];
  const mask = new Set();
  for (const p of (isObj(gateway) && Array.isArray(gateway.providers) ? gateway.providers : []).filter(isObj)) {
    if (p.apiKey == null) continue;
    let value = null;
    try {
      value = resolve(p.apiKey, `${p.id} credential`);
    } catch {
      value = null; // the control plane answers this provider's calls with the error it gives today
    }
    if (typeof value !== 'string' || !value) continue;
    providers.push({ id: str(p.id), value });
    if (value.length >= 6) mask.add(value);
  }
  return { providers, mask: [...mask] };
}

const VIEW = ['id', 'name', 'scope', 'period', 'currency', 'measure', 'enforce', 'amount', 'from', 'to', 'state'];
/**
 * The budget.snapshot body without at: the blocking budgets with their state, and each key's cost center. A budget
 * service that cannot list (no ledger yet) gives no budgets: the guard lets requests through then as well.
 * @param {{ service: any, keyIds: string[], now?: number }} src
 */
function snapshotPush({ service, keyIds, now = Date.now() }) {
  const budgets = [];
  const keyCenter = {};
  if (!service) return { budgets, keyCenter };
  let list = [];
  try {
    list = service.list().budgets.filter((b) => b && b.enforce === 'block');
  } catch {
    list = [];
  }
  for (const b of list) {
    let v;
    try {
      v = service.evaluateOne(b, now, { forecast: false });
    } catch {
      v = { ...b, state: 'no_data', spentUSD: null, amountUSD: null, reason: 'the budget could not be checked' };
    }
    const view = Object.fromEntries(VIEW.map((k) => [k, v[k] ?? b[k]]));
    view.amount = Number.isFinite(view.amount) ? view.amount : 0;
    for (const k of ['name', 'currency', 'from', 'to']) view[k] = str(view[k]);
    view.state = str(view.state) || 'no_data';
    view.spentUSD = Number.isFinite(v.spentUSD) ? v.spentUSD : null;
    view.amountUSD = Number.isFinite(v.amountUSD) ? v.amountUSD : null;
    view.reason = typeof v.reason === 'string' ? v.reason : null;
    budgets.push(view);
  }
  for (const id of keyIds) {
    try {
      const c = service.keyCenter(id);
      if (typeof c === 'string' && c) keyCenter[id] = c;
    } catch {
      /* no center */
    }
  }
  return { budgets, keyCenter };
}

module.exports = {
  windowPush,
  routeStatePush,
  configPush,
  egressSection,
  secretsPush,
  snapshotPush,
  keyAuth,
  provider,
  price,
};

// The config.json-to-IPC converter (P4b): what the control plane pushes to the Go gateway in front, built fresh for
// every generation from the same sources the JS gateway reads, so both engines decide from the same state.
//   configPush:  config.json `gateway` (providers without secrets, routes, guardrails, defaults, tokenSaver, modelMap),
//                keys.json (each key with the SHA-256 hex of its secret, never the secret), and the effective price
//                table (config.pricing: defaults, live prices and the owner's own) in nano-USD per 1M tokens;
//   secretsPush: each key-auth provider's resolved credential and the mask set (every resolved value of 6+ characters,
//                as lib/secrets.js mask() hides them); a provider whose credential does not resolve is left out, and the
//                gateway hands its calls to the control plane, which gives the same error it gives today;
//   snapshotPush: the budgets the guard enforces (enforce "block"), evaluated without a forecast, and each key's cost
//                center (lib/cost/guard.js reads both).
// Pure apart from the resolver and the budget service it is handed. Providers with OAuth2 or a Google service account
// get no token here: provider auth in Go is P4g (stream C builds the library, stage 2 wires it).
const { toNano } = require('../money');
const { resolvePolicy } = require('../gateway/guardrails');

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v) => (typeof v === 'string' ? v : '');
const strings = (v) => (Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []);
const MODES = {
  secrets: ['off', 'log', 'redact', 'block'],
  pii: ['off', 'log', 'redact', 'block'],
  injection: ['off', 'log', 'block'],
};
const int = (v) => (Number.isSafeInteger(v) && v >= 0 ? v : null);

/** A provider as config.push carries it: no apiKey reference; auth { type: 'key' } when it has one. */
function provider(p) {
  const out = { id: str(p.id), preset: str(p.preset), baseUrl: str(p.baseUrl) };
  if (Array.isArray(p.models)) out.models = strings(p.models);
  if (isObj(p.aliases))
    out.aliases = Object.fromEntries(Object.entries(p.aliases).filter(([, v]) => typeof v === 'string'));
  for (const k of ['enabled', 'approved', 'responses', 'reasoning']) if (typeof p[k] === 'boolean') out[k] = p[k];
  if (p.tier === 'private' || p.tier === 'external') out.tier = p.tier;
  if (p.format === 'openai' || p.format === 'anthropic') out.format = p.format;
  if (p.tokenParam === 'max_tokens' || p.tokenParam === 'max_completion_tokens') out.tokenParam = p.tokenParam;
  if (isObj(p.auth) && typeof p.auth.type === 'string' && p.auth.type) {
    const a = { type: p.auth.type };
    for (const k of ['tokenUrl', 'clientId', 'scope']) if (typeof p.auth[k] === 'string') a[k] = p.auth[k];
    if (p.auth.grant === 'client_credentials' || p.auth.grant === 'refresh_token') a.grant = p.auth.grant;
    out.auth = a;
  } else if (p.apiKey != null) out.auth = { type: 'key' };
  return out;
}

function route(r) {
  const out = { name: str(r.name), targets: strings(r.targets) };
  if (r.strategy === 'fallback' || r.strategy === 'round-robin') out.strategy = r.strategy;
  if (typeof r.description === 'string') out.description = r.description;
  return out;
}

/** A keys.json key as the gateway enforces it (ipc/v1 config.push keys[]), or null when it has no valid hash. */
function keyAuth(k) {
  if (!isObj(k) || typeof k.id !== 'string' || !k.id || !/^[0-9a-f]{64}$/.test(k.hash || '')) return null;
  const guardrails = {};
  if (isObj(k.guardrails)) {
    if (k.guardrails.scope === 'external' || k.guardrails.scope === 'all') guardrails.scope = k.guardrails.scope;
    for (const c of Object.keys(MODES)) if (MODES[c].includes(k.guardrails[c])) guardrails[c] = k.guardrails[c];
  }
  return {
    id: k.id,
    name: str(k.name),
    scopes: strings(k.scopes),
    models: strings(k.models),
    agents: strings(k.agents),
    knowledge: strings(k.knowledge),
    budgetUSD: Number.isFinite(k.budgetUSD) ? k.budgetUSD : null,
    rpm: int(k.rpm) ?? 0,
    privateOnly: k.privateOnly === true,
    expiresAt: int(k.expiresAt),
    revokedAt: int(k.revokedAt),
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

/**
 * The config.push body without gen and front (the supervisor adds gen, lib/front adds front).
 * @param {{ gateway: any, keys: any[], pricing: Record<string, any> }} src
 */
function configPush({ gateway, keys, pricing }) {
  const g = isObj(gateway) ? gateway : {};
  const d = isObj(g.defaults) ? g.defaults : {};
  return {
    gateway: {
      providers: (Array.isArray(g.providers) ? g.providers : []).filter(isObj).map(provider),
      routes: (Array.isArray(g.routes) ? g.routes : []).filter(isObj).map(route),
      guardrails: resolvePolicy(g.guardrails, null),
      defaults: { model: str(d.model), plannerModel: str(d.plannerModel), codeModel: str(d.codeModel) },
      tokenSaver: g.tokenSaver !== false,
      modelMap: (Array.isArray(g.modelMap) ? g.modelMap : [])
        .filter(isObj)
        .map((m) => ({ match: str(m.match), to: str(m.to) })),
    },
    keys: (Array.isArray(keys) ? keys : []).map(keyAuth).filter(Boolean),
    pricing: Object.fromEntries(
      Object.entries(isObj(pricing) ? pricing : {})
        .filter(([, e]) => isObj(e))
        .map(([k, e]) => [k, price(e)]),
    ),
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
    if (p.apiKey == null || (isObj(p.auth) && p.auth.type && p.auth.type !== 'key')) continue;
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

module.exports = { configPush, secretsPush, snapshotPush, keyAuth, provider, price };

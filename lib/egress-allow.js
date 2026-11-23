// The egress allow-list (P5 spec Decision 13): every host the control plane and the Go engine may reach, built from
// config.json (and the few sources that live elsewhere, passed in), pushed in config.push.egress.allow. Each rule is
// { host, port }: host lower case, without IPv6 brackets or a trailing dot, a name, an address or "*.suffix"; port is the
// URL's effective port (443 for https and 80 for http when the URL names none, 5432 for Postgres), so a rule is exact
// (gw/egress Policy.Allows: a rule without a port takes any port; only Roam's peer wildcard is one). Loopback hosts are left out: they never
// go through Go (lib/egress.js isLoopbackHost). A URL that does not parse adds nothing. The `public` class (URL ingest)
// is never matched against the list (Decision 7), so it adds nothing.
// Pure apart from the secret resolver it may be handed (the monitor webhook URL and a pgvector connection URL are
// secret references; only their host and port go on the list).
const { isLoopbackHost } = require('./egress');
const { PRESETS } = require('./gateway/providers');

const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
const list = (v) => (Array.isArray(v) ? v.filter(isObj) : []);
const on = (x) => x.enabled !== false;
const DEFAULT_PORTS = { 'https:': 443, 'http:': 80, 'postgres:': 5432, 'postgresql:': 5432 };
const GOOGLE_TOKEN_URL = 'https://oauth2.googleapis.com/token';
const OPENROUTER = 'https://openrouter.ai';

/**
 * The rule for a URL, or null (not a string, no host, an unknown scheme, a loopback host).
 * @param {unknown} url
 * @returns {{ host: string, port: number } | null}
 */
function ruleOf(url) {
  if (typeof url !== 'string' || !url) return null;
  let u;
  try {
    u = new URL(url);
  } catch {
    return null;
  }
  const port = Number(u.port) || DEFAULT_PORTS[u.protocol];
  let host = u.hostname.toLowerCase();
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
  if (host.endsWith('.')) host = host.slice(0, -1);
  if (!port || !host || isLoopbackHost(host)) return null;
  return { host, port };
}
const https = (host) => ({ host, port: 443 });

// Provider base URLs (the preset's when the entry has none) and token URLs (OAuth2: the entry's or the preset's;
// Google service account: Google's default token endpoint, and the token_uri the stored JSON names when it names
// another, read through the resolver as both engines read it: only its host and port go on the list).
function providerUrls(config, resolveSecret) {
  const out = [];
  const g = isObj(config.gateway) ? config.gateway : {};
  for (const p of list(g.providers).filter(on)) {
    const pre = PRESETS[p.preset] || {};
    out.push(p.baseUrl || pre.baseUrl);
    const a = isObj(p.auth) ? p.auth : {};
    if (a.type === 'oauth2') out.push(a.tokenUrl || (pre.oauth || {}).tokenUrl);
    else if (a.type === 'google-sa') out.push(GOOGLE_TOKEN_URL, p.apiKey != null ? saTokenUri(p, resolveSecret) : null);
  }
  return out;
}

// The token_uri of a stored Google service account JSON, or null (no secret, not JSON, none named).
function saTokenUri(p, resolveSecret) {
  const raw = resolveSecret(p.apiKey, `${p.id} credential`);
  if (typeof raw !== 'string' || !raw) return null;
  try {
    const sa = JSON.parse(raw);
    return isObj(sa) && typeof sa.token_uri === 'string' ? sa.token_uri : null;
  } catch {
    return null;
  }
}

// Integrations (connectors, OmniRoute through the router connector, upstream MCP servers), each type's own defaults
// as lib/connectors applies them; n8n's allow-listed webhook URLs too.
const INTEGRATION_URLS = {
  dynatrace: (e) => [e.baseUrl, e.platformUrl],
  arize: (e) => [e.baseUrl || ((e.flavor || 'phoenix') === 'ax' ? 'https://api.arize.com' : 'http://localhost:6006')],
  n8n: (e) => [e.baseUrl, ...(isObj(e.webhooks) ? Object.values(e.webhooks) : [])],
  router: (e) => [e.baseUrl || 'http://localhost:20128'],
  http: (e) => [e.baseUrl],
  openrouter: () => [OPENROUTER],
  mcp: (e) => [e.url],
};
function integrationUrls(config) {
  const ints = isObj(config.integrations) ? config.integrations : {};
  return Object.values(ints)
    .filter((e) => isObj(e) && on(e) && INTEGRATION_URLS[e.type])
    .flatMap((e) => INTEGRATION_URLS[e.type](e));
}

// Second-brain sources: the apps' fixed API hosts, Pinecone's control and index hosts, and the URL of each source
// that has one (Heybrain's MCP URL, Qdrant, Weaviate, Chroma, a pgvector URL); a pgvector connection URL kept as a
// secret through the resolver.
const BRAIN_RULES = {
  notion: [https('api.notion.com')],
  // Roam's Backend API answers with a 308 to a peer host (peer-N.api.roamresearch.com) on a port of its choosing
  // (lib/brain/apps.js follows it): the wildcard takes any port.
  roam: [https('api.roamresearch.com'), { host: '*.roamresearch.com' }],
  mem: [https('api.mem.ai')],
  reflect: [https('reflect.app')],
  pinecone: [https('api.pinecone.io'), https('*.pinecone.io')],
};
function brainRules(config, resolveSecret) {
  const b = isObj(config.brain) ? config.brain : {};
  const out = [];
  for (const s of list(b.sources).filter(on)) {
    out.push(...(BRAIN_RULES[s.type] || []));
    const r = ruleOf(s.url);
    if (r) out.push(r);
    if (s.type === 'pgvector' && s.secret != null) {
      const p = ruleOf(resolveSecret(s.secret, `${s.name || s.id} secret`));
      if (p) out.push(p);
    }
  }
  return out;
}

// Remote A2A agents (agents.json, read by the caller).
const agentUrls = (agents) =>
  list(agents)
    .filter(on)
    .map((a) => a.remoteUrl);

// The monitor webhook (a secret reference in config.json monitors.channels.webhook.url).
function webhookUrls(config, resolveSecret) {
  const m = isObj(config.monitors) ? config.monitors : {};
  const ch = isObj(m.channels) ? m.channels : {};
  const w = isObj(ch.webhook) ? ch.webhook : null;
  return w && w.url != null ? [resolveSecret(w.url, 'webhook.url')] : [];
}

// The OTLP endpoint, chosen as lib/otel.js buildSettings chooses it, when export is on.
function otelUrls(config, env) {
  const c = isObj(config.otel) ? config.otel : {};
  const envEndpoint = env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT || env.OTEL_EXPORTER_OTLP_ENDPOINT;
  if (c.enabled === false || (c.enabled !== true && !envEndpoint)) return [];
  if (String(env.OTEL_SDK_DISABLED || '').toLowerCase() === 'true') return [];
  return [c.tracesEndpoint || c.endpoint || env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT || env.OTEL_EXPORTER_OTLP_ENDPOINT];
}

// Local runtimes and the evals' model servers: the owner's runtime health URLs (loopback by validation, so in practice
// none) and ZAAFPULSE_OLLAMA_URL / ZAAFPULSE_LMSTUDIO_URL when they point elsewhere.
const runtimeUrls = (config, env) => [
  ...list(config.localRuntimes).map((r) => r.health),
  env.ZAAFPULSE_OLLAMA_URL,
  env.ZAAFPULSE_LMSTUDIO_URL,
];

/**
 * The allow-list: sorted by host then port, unique.
 * @param {any} config config.json (raw)
 * @param {{ env?: Record<string, string | undefined>, agents?: any[],
 *   resolveSecret?: (ref: any, field: string) => string | null }} [opts]
 * @returns {{ host: string, port?: number }[]}
 */
function allowList(config, { env = {}, agents = [], resolveSecret = () => null } = {}) {
  const c = isObj(config) ? config : {};
  const safe = (ref, field) => {
    try {
      return resolveSecret(ref, field);
    } catch {
      return null; // an unresolvable secret adds nothing; its caller fails on its own
    }
  };
  const urls = [
    ...providerUrls(c, safe),
    ...integrationUrls(c),
    OPENROUTER, // the OpenRouter page and price sync (on by default, no switch for the page)
    ...agentUrls(agents),
    ...webhookUrls(c, safe),
    ...otelUrls(c, env),
    ...runtimeUrls(c, env),
  ];
  const rules = [...urls.map(ruleOf).filter(Boolean), ...brainRules(c, safe)];
  const seen = new Map(rules.map((r) => [`${r.host} ${r.port ?? '*'}`, r]));
  return [...seen.values()].sort((a, b) =>
    a.host < b.host ? -1 : a.host > b.host ? 1 : (a.port ?? 0) - (b.port ?? 0),
  );
}

module.exports = { allowList, ruleOf };

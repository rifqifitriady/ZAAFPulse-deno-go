// The egress allow-list (P5 spec Decision 13): every host the control plane and the Go engine may reach, built from
// config.json (and the few sources that live elsewhere, passed in), pushed in config.push.egress.allow. Each rule is
// { host, port }: host lower case, without IPv6 brackets or a trailing dot, a name, an address or "*.suffix"; port is the
// URL's effective port (443 for https and 80 for http when the URL names none, 5432 for Postgres), so a rule is exact
// (gw/egress Policy.Allows: a rule without a port takes any port; only Roam's peer wildcard is one). Loopback hosts are left out: they never
// go through Go (lib/egress.js isLoopbackHost). A URL that does not parse adds nothing. The `public` class (URL ingest)
// is never matched against the list (Decision 7), so it adds nothing.
// Pure apart from the secret resolver it may be handed (the monitor webhook URL and a pgvector connection URL are
// secret references; only their host and port go on the list).
const { CLASSES, isLoopbackHost } = require('./egress');
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
function integrationUrls(config, types) {
  const ints = isObj(config.integrations) ? config.integrations : {};
  return Object.values(ints)
    .filter((e) => isObj(e) && on(e) && INTEGRATION_URLS[e.type] && types.includes(e.type))
    .flatMap((e) => INTEGRATION_URLS[e.type](e));
}
const CONNECTOR_TYPES = ['dynatrace', 'arize', 'n8n', 'router', 'http', 'openrouter'];

// Second-brain sources: the apps' fixed API hosts, Pinecone's control and index hosts, and the URL of each source
// that has one (Qdrant, Weaviate, Chroma, a pgvector URL); a pgvector connection URL kept as a secret through the
// resolver. Heybrain's MCP URL is a brain source too, but its calls are of the `mcp` class (lib/brain/apps.js).
const BRAIN_RULES = {
  notion: [https('api.notion.com')],
  // Roam's Backend API answers with a 308 to a peer host (peer-N.api.roamresearch.com) on a port of its choosing
  // (lib/brain/apps.js follows it): the wildcard takes any port.
  roam: [https('api.roamresearch.com'), { host: '*.roamresearch.com' }],
  mem: [https('api.mem.ai')],
  reflect: [https('reflect.app')],
  pinecone: [https('api.pinecone.io'), https('*.pinecone.io')],
};
const brainSources = (config) => list((isObj(config.brain) ? config.brain : {}).sources).filter(on);
function brainDestinations(config, secret) {
  const out = [];
  for (const s of brainSources(config).filter((x) => x.type !== 'heybrain')) {
    out.push(...(BRAIN_RULES[s.type] || []), s.url);
    if (s.type === 'pgvector' && s.secret != null) out.push(secret(s.secret, `${s.name || s.id} secret`));
  }
  return out;
}
const heybrainUrls = (config) =>
  brainSources(config)
    .filter((x) => x.type === 'heybrain')
    .map((x) => x.url);

// Remote A2A agents (agents.json, read by the caller).
const agentUrls = (agents) =>
  list(agents)
    .filter(on)
    .map((a) => a.remoteUrl);

// The monitor webhook (a secret reference in config.json monitors.channels.webhook.url).
function webhookUrls(config, secret) {
  const m = isObj(config.monitors) ? config.monitors : {};
  const ch = isObj(m.channels) ? m.channels : {};
  const w = isObj(ch.webhook) ? ch.webhook : null;
  return w && w.url != null ? [secret(w.url, 'webhook.url')] : [];
}

// The OTLP endpoint, chosen as lib/otel.js buildSettings chooses it, when export is on.
function otelUrls(config, env) {
  const c = isObj(config.otel) ? config.otel : {};
  const envEndpoint = env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT || env.OTEL_EXPORTER_OTLP_ENDPOINT;
  if (c.enabled === false || (c.enabled !== true && !envEndpoint)) return [];
  if (String(env.OTEL_SDK_DISABLED || '').toLowerCase() === 'true') return [];
  return [c.tracesEndpoint || c.endpoint || env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT || env.OTEL_EXPORTER_OTLP_ENDPOINT];
}

// Price sync is on unless config.json says pricingSync.enabled: false (server.js).
const pricingOn = (config) => !(isObj(config.pricingSync) && config.pricingSync.enabled === false);

// A destination that is configured but whose value is not looked at (quietClasses: a secret reference, an agents.json
// that could not be read).
const PRESENT = Symbol('present');

/**
 * Every configured destination per caller class (lib/egress.js CLASSES): URLs, rules, or PRESENT. `secret` reads a
 * secret reference (the allow-list resolves it; quietClasses only notes that it is there).
 * @param {any} c config.json (raw, an object)
 * @param {{ env: Record<string, string | undefined>, agents: any[] | undefined,
 *   secret: (ref: any, field: string) => unknown }} opts
 */
function destinationsByClass(c, { env, agents, secret }) {
  return {
    gateway: providerUrls(c, secret),
    connectors: integrationUrls(c, CONNECTOR_TYPES),
    mcp: [...integrationUrls(c, ['mcp']), ...heybrainUrls(c)],
    // OmniRoute (lib/omniroute.js reads the router integrations) and the OpenRouter page, which has no switch.
    routers: [OPENROUTER, ...integrationUrls(c, ['router'])],
    pricing: pricingOn(c) ? [OPENROUTER] : [],
    // The evals' model servers (lib/evals/models.js).
    evals: [env.ZAAFPULSE_OLLAMA_URL, env.ZAAFPULSE_LMSTUDIO_URL],
    agents: agents === undefined ? [PRESENT] : agentUrls(agents),
    notify: webhookUrls(c, secret),
    brain: brainDestinations(c, secret),
    // URL ingest: whatever a user types; never matched against the list (Decision 7), never quiet.
    public: [],
    otel: otelUrls(c, env),
    // The owner's runtime health URLs (loopback by validation, so in practice none).
    runtimes: list(c.localRuntimes).map((r) => r.health),
  };
}

// A destination as a rule, or null (PRESENT, a URL that gives no rule: loopback, unparsable, another scheme).
const asRule = (d) => (isObj(d) && typeof d.host === 'string' ? d : typeof d === 'string' ? ruleOf(d) : null);

/**
 * The allow-list: sorted by host then port, unique.
 * @param {any} config config.json (raw)
 * @param {{ env?: Record<string, string | undefined>, agents?: any[],
 *   resolveSecret?: (ref: any, field: string) => string | null }} [opts]
 * @returns {{ host: string, port?: number }[]}
 */
function allowList(config, { env = {}, agents = [], resolveSecret = () => null } = {}) {
  const c = isObj(config) ? config : {};
  const secret = (ref, field) => {
    try {
      return resolveSecret(ref, field);
    } catch {
      return null; // an unresolvable secret adds nothing; its caller fails on its own
    }
  };
  const rules = Object.values(destinationsByClass(c, { env, agents: agents || [], secret }))
    .flat()
    .map(asRule)
    .filter(Boolean);
  const seen = new Map(rules.map((r) => [`${r.host} ${r.port ?? '*'}`, r]));
  return [...seen.values()].sort((a, b) =>
    a.host < b.host ? -1 : a.host > b.host ? 1 : (a.port ?? 0) - (b.port ?? 0),
  );
}

// Classes whose destinations never come from the configuration: never quiet.
const NEVER_QUIET = ['public'];

/**
 * The caller classes this install has nothing configured to call (P7 defaults design, Decision B): no destination of
 * the class gives a non-loopback rule. Presence decides: a secret reference counts as configured without being
 * resolved, and `agents` undefined (agents.json could not be read) counts as a configured agent. `public` and `routers`
 * (the OpenRouter page) are never quiet; `pricing` is quiet only with price sync off. In lib/egress.js CLASSES order.
 * @param {any} config config.json (raw)
 * @param {{ env?: Record<string, string | undefined>, agents?: any[] }} [opts]
 * @returns {string[]}
 */
function quietClasses(config, { env = {}, agents } = {}) {
  const c = isObj(config) ? config : {};
  const by = destinationsByClass(c, { env, agents, secret: (ref) => (ref == null ? null : PRESENT) });
  return CLASSES.filter(
    (cls) => !NEVER_QUIET.includes(cls) && !(by[cls] || []).some((d) => d === PRESENT || asRule(d)),
  );
}

module.exports = { allowList, quietClasses, ruleOf };

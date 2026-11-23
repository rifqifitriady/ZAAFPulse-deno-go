// @ts-check
// The integration tool endpoint of the go engine's /mcp tools/call (P4g, slice S9, Decision 2): the engine owns the
// HTTP edge (key, rate limit, both guardrail checks, the usage line, the spans, the reply) and calls the tool here, on
// the internal port, as the gateway itself (lib/front/internal.js `own`):
//   POST /_zp/mcp/call  { name, arguments, keyId?, trace? }
//     → 200 { r: <callTool's value> } (JSON.stringify: an undefined value is `{}`), or { thrown: true } when callTool
//       threw; `child: true` is added when a span was started inside the call (the gateway then sends its request
//       span, as lib/otel.js requestContext does for a request in which something happened);
//     → 400 when the body is not { name: string, arguments: object }.
// keyId runs the call as the key (lib/governance/context.js), so the integrations' audit names it as in js mode;
// trace ({ traceId, spanId, sampled, traceState }) is the gateway's request span, under which the tool's spans nest.
// Any other request is not this endpoint's (false).
const otel = require('../otel');
const govContext = require('../governance/context');

const MAX_BODY = 20 * 1024 * 1024;

const isObject = (v) => !!v && typeof v === 'object' && !Array.isArray(v);

/**
 * @param {{ getIntegrations: () => { callTool: (name: string, args: any) => any } }} deps
 * @returns {(req: any, res: any) => boolean}
 */
function toolEndpoint({ getIntegrations }) {
  return (req, res) => {
    if (req.method !== 'POST' || req.url !== '/_zp/mcp/call') return false;
    let size = 0;
    /** @type {Buffer[]} */
    const chunks = [];
    req.on('data', (/** @type {Buffer} */ c) => {
      size += c.length;
      if (size <= MAX_BODY) chunks.push(c);
    });
    req.on('end', () => {
      let m = null;
      try {
        m = size <= MAX_BODY ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : null;
      } catch {
        m = null;
      }
      if (
        !isObject(m) ||
        typeof m.name !== 'string' ||
        !isObject(m.arguments) ||
        (m.keyId !== undefined && typeof m.keyId !== 'string') ||
        (m.trace !== undefined && !isObject(m.trace))
      ) {
        res.writeHead(400).end();
        return;
      }
      let child = false;
      const trace = m.trace && typeof m.trace.traceId === 'string' ? m.trace : null;
      const call = () => getIntegrations().callTool(m.name, m.arguments);
      const run = () => (trace ? otel.within(trace, call, null, { onChild: () => (child = true) }) : call());
      const send = (/** @type {any} */ answer) => {
        let text;
        try {
          text = JSON.stringify(child ? { ...answer, child: true } : answer);
        } catch {
          text = JSON.stringify(child ? { thrown: true, child: true } : { thrown: true }); // a value JSON cannot hold
        }
        res.writeHead(200, { 'content-type': 'application/json' }).end(text);
      };
      govContext.runAs(m.keyId ? { via: 'apikey', keyId: m.keyId } : null, () =>
        Promise.resolve()
          .then(run)
          .then(
            (r) => send({ r }),
            () => send({ thrown: true }),
          ),
      );
    });
    return true;
  };
}

module.exports = { toolEndpoint };

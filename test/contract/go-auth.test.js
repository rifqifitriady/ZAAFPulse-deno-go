// Contract: calls to token-auth providers (P4g): OAuth2 (client_credentials and refresh_token grants, the token URL
// over HTTPS with a CA made for the run) and a Google service account (a test RSA key made here; its token_uri is the
// fake upstream's POST /token), all saved through the dashboard API. Chat, whole and streamed, reaches the provider
// with "Bearer <token from the token endpoint>"; a token the provider revoked is answered 401 once, and the call is
// replayed with a fresh token and answers 200. The cases hold in every mode; under ZP_ENGINE=go each line carries "ipc"
// (the go engine served the call with its own token), except the refresh_token grant provider's, which stays on the
// control plane (P4g Decision 19); under ZP_ENGINE=shadow the harness's stop() fails on any diff.
const test = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const { start } = require('./harness');
const { JWT_BEARER } = require('./upstream');

const GO = process.env.ZP_ENGINE === 'go';
// A running instance (ZP_CONTRACT_URL) does not trust this run's CA, so the OAuth2 token URL cannot be served to it.
const skip = !!process.env.ZP_CONTRACT_URL && 'needs a spawned server that trusts the run CA';
const CLIENT_SECRET = 'contract-client-secret';
const REFRESH = 'contract-refresh-token';
const SA_EMAIL = 'contract@zaafpulse.iam.gserviceaccount.com';
const SCOPE = 'https://contract.example/.default';

const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });

let h;
test.before(async () => {
  if (skip) return;
  h = await start({ tls: true });
  const store = (value) => ({ mode: 'store', value });
  const base = { preset: 'custom', baseUrl: h.upstream.url, tier: 'external', approved: true, models: ['m1'] };
  const oauth = (grant) => ({
    tokenUrl: `${h.upstream.tlsUrl}/token`,
    clientId: 'contract-client',
    scope: SCOPE,
    grant,
  });
  const providers = [
    { ...base, id: 'o2', authType: 'oauth2', oauth: oauth('client_credentials'), secret: store(CLIENT_SECRET) },
    {
      ...base,
      id: 'o2r',
      authType: 'oauth2',
      oauth: oauth('refresh_token'),
      secret: store(JSON.stringify({ client_secret: CLIENT_SECRET, refresh_token: REFRESH })),
    },
    {
      ...base,
      id: 'gsa',
      authType: 'google-sa',
      secret: store(
        JSON.stringify({
          type: 'service_account',
          client_email: SA_EMAIL,
          private_key: privateKey.export({ type: 'pkcs8', format: 'pem' }),
          token_uri: `${h.upstream.url}/token`,
        }),
      ),
    },
  ];
  for (const p of providers) {
    const r = await h.admin('providers', { action: 'save', ...p });
    assert.equal(r.provider.auth.type, p.authType);
  }
});
test.after(async () => {
  if (h) await h.stop();
});

const user = [{ role: 'user', content: 'hi' }];
const chatCalls = () => h.upstream.requests.filter((r) => r.path === '/chat/completions');
const bearerOf = (r) => String(r.headers.authorization || '');
// One chat call to provider id (stream or whole): 200, and the provider got a token the token endpoint issued.
async function chat(id, stream) {
  const before = (await h.logs()).length;
  const n = chatCalls().length;
  const r = await h.call('/v1/chat/completions', {
    key: h.key,
    body: { model: `${id}/m1`, messages: user, ...(stream ? { stream: true } : {}) },
  });
  assert.equal(r.status, 200, r.text);
  if (stream) assert.match(r.text, /data: \[DONE\]/);
  else assert.equal(r.json.choices[0].message.content, 'hi');
  const sent = chatCalls().slice(n);
  const auth = bearerOf(sent.at(-1));
  assert.match(auth, /^Bearer zp-tok-\d+$/);
  assert.ok(h.upstream.tokens.issued.includes(auth.slice(7)), auth);
  const line = await h.lineAfter(before);
  assert.equal(line.status, 200);
  // A refresh_token grant provider stays on the control plane (P4g Decision 19): js serves it in every mode.
  if (GO && id === 'o2r') assert.ok(!line.ipc, `js served it: ${JSON.stringify(line)}`);
  else if (GO) assert.ok(line.ipc, `the go engine served it: ${JSON.stringify(line)}`);
  return { sent, line };
}
const formsOf = (pred) => h.upstream.tokens.forms.map((f) => f.form).filter(pred);

for (const id of ['o2', 'o2r', 'gsa'])
  for (const stream of [false, true])
    test(`go auth: ${id} chat${stream ? ' stream' : ''} reaches the provider with a Bearer token`, { skip }, () =>
      chat(id, stream),
    );

test('go auth: the token endpoint got each grant as a form', { skip }, () => {
  for (const { contentType } of h.upstream.tokens.forms)
    assert.match(contentType, /^application\/x-www-form-urlencoded/);
  const cc = formsOf((f) => f.grant_type === 'client_credentials');
  assert.ok(cc.length > 0);
  for (const f of cc)
    assert.deepEqual(f, {
      grant_type: 'client_credentials',
      client_id: 'contract-client',
      client_secret: CLIENT_SECRET,
      scope: SCOPE,
    });
  const rt = formsOf((f) => f.grant_type === 'refresh_token');
  assert.ok(rt.length > 0);
  for (const f of rt)
    assert.deepEqual([f.client_id, f.client_secret, f.refresh_token], ['contract-client', CLIENT_SECRET, REFRESH]);
  const jwt = formsOf((f) => f.grant_type === JWT_BEARER);
  assert.ok(jwt.length > 0);
  for (const f of jwt) {
    const [head, claims, sig] = f.assertion.split('.');
    const json = (s) => JSON.parse(Buffer.from(s, 'base64url').toString());
    assert.deepEqual(json(head), { alg: 'RS256', typ: 'JWT' });
    const c = json(claims);
    assert.deepEqual(
      [c.iss, c.aud, c.scope],
      [SA_EMAIL, `${h.upstream.url}/token`, 'https://www.googleapis.com/auth/cloud-platform'],
    );
    assert.equal(c.exp - c.iat, 3600);
    assert.ok(crypto.verify('sha256', Buffer.from(`${head}.${claims}`), publicKey, Buffer.from(sig, 'base64url')));
  }
});

for (const id of ['o2', 'gsa'])
  test(`go auth: ${id} with a revoked token is answered 401 once, refreshed and replayed`, { skip }, async () => {
    h.upstream.revoke();
    const issued = h.upstream.tokens.issued.length;
    const { sent } = await chat(id, false);
    assert.equal(sent.length, 2, 'one 401, one replay');
    assert.ok(h.upstream.tokens.revoked.has(bearerOf(sent[0]).slice(7)), 'the first try carried the revoked token');
    assert.ok(!h.upstream.tokens.revoked.has(bearerOf(sent[1]).slice(7)), 'the replay carries a fresh one');
    assert.ok(h.upstream.tokens.issued.length > issued, 'a new token was fetched');
  });

// ZP_ENGINE=shadow: the four chat calls above to o2 and gsa (whole and streamed) were compared with the go engine under
// the provider-auth switch, with no diff (the two replays are skipped by design: each engine holds its own token; the
// refresh_token grant provider o2r is a hand-off, never a compare: Decision 19); a switch that stopped shadowing would
// leave the diff log empty and pass without this.
test(
  'go auth: the shadow compared the token-auth calls, with no diff',
  { skip: skip || (process.env.ZP_ENGINE !== 'shadow' && 'only under ZP_ENGINE=shadow') },
  async () => {
    const st = await h.shadowStats('provider-auth', 4);
    assert.ok(st && st.compared >= 4, JSON.stringify(st));
    assert.equal(st.diffs, 0, JSON.stringify(st));
    // The two o2r calls were handed off with the Decision 19 reason.
    const log = fs.readFileSync(path.join(h.server.data, '.data', 'gateway-shadow.jsonl'), 'utf8');
    const handed = log
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l))
      .filter((e) => e.outcome === 'handoff' && /refresh-token grant stays on the control plane/.test(e.reason || ''));
    assert.ok(handed.length >= 2, `${handed.length} refresh-token grant hand-offs`);
  },
);

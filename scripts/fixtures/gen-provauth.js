// Generates the provauth fixtures (P4 stage 1, stream C; spec "Equivalence by generated fixtures"): runs the real
// lib/gateway/providers.js accessToken over a fixed corpus, with a fake fetch that records each token request and
// answers from a queue, a fixed clock per step, and a fixed test key, and writes gw/testdata/provauth/<case>.json as
// { fn: 'accessToken', in, out: { requests, results } }. gw/provauth/fixtures_test.go replays every file in Go;
// test/provauth-fixtures.test.js regenerates them on Node and Deno and fails on any difference.
// Usage: node scripts/fixtures/gen-provauth.js (writes the files and removes stale ones).
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const { createProviders } = require('../../lib/gateway/providers');

const DIR = path.join(__dirname, '..', '..', 'gw', 'testdata', 'provauth');

// TEST-ONLY FIXTURE KEY, NOT A SECRET: an RSA key generated once for this corpus. It authenticates nothing and signs
// nothing outside the fixtures. It is fixed (not generated per run) because the RS256 signature is deterministic and
// the fixtures compare it byte for byte. Secret scanning skips this file and gw/testdata/provauth/
// (.github/secret_scanning.yml).
const TEST_KEY =
  [
    '-----BEGIN PRIVATE KEY-----',
    'MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQCil2E9oKovfk4C',
    '0vsWjkB3sbLGGztssD89umOIgy4I78rvzSqtEhMgNTa7MBW5u29VdLviOYkft05O',
    'UYTKcbvZl0gq0ubns7OqsOJ1xV6e6owgmm4hpVueyMg+a/UoOjDCmKf2RXSxRyJ0',
    'Bagzvu6CP+TnWDGgANaVJU8XRaq7cLp3H8B8BkwZVLhKWaVvDlFAnI5tXzDPkn2h',
    'MfnVgNwDtxMGGjls321RBKV5ho9toKNT0evCcwSisCt2Jd5upum71N3w2O8UKDYE',
    '0kVmI92pVEVE9BS71oSqetzNw+fwS6S6msycktQdKzIMXy0Fi7Coqar4hEkJpOxu',
    'mHkgqU3DAgMBAAECggEADpJwsFGg+ca2foGi5/+cft3ElFrGdamffAx4yo2vPN/O',
    'RAsS+GtL9DFbaWVth7WTzIiSJOaxMnrUStKlixtXEbEBp8zlIv5ea5LqM6dFIWBg',
    'v19NNY4YwF9ltap+pg9TusJsc0xVoWSAYv5KzUXJzI9QwvyK3g3qgs19jCwTRDSO',
    'pVhQA/uELIhxK4losngJJXeboml7KllmZVNJCUFnVB3ycdMGsPGs/fr4qSg6AAcW',
    '3IS0soTzn6p/ubF3MmS6Ry+bWyOW8nsWYCTDlMPYtUWscvtp7a/ERgbWkxmuL+UG',
    '2BJ+gaURgpanoEjj2SaO///KkhBy9dgDWkbJ0loW4QKBgQDS0uDoI32InL1fadZc',
    'qNMPdCJyZrt/06lnjovv/ZFOcwyD2Q45t8YL87pdbl+hTYbC15womejqFXlg56Yh',
    'uHiHdGKV3NjNCjTBPJsZNh5u+xhr+hVj8miAHrmYYfaraEKGQvGDcZPupVGm4Pp/',
    'er5YyPiFd+Doq373aRn+X4dkkQKBgQDFbp+/GP5+IJY6F8Rf5NbetaZFQYCYZTM/',
    'tTe17z+t3hMX+5KYDPL/9C5JbsMDcQxBHUIwYq6ItLmku0flVqIOfBKAyt5LEC6b',
    'PhN5d/mSQTopQXYt3pxKo3l43suRVgVAW3KMNYwPMX9X/VTHQVSJDmT60z6Fao5z',
    'BpeQtv7nEwKBgB4fa2s/xuwCGTzMzogmxyFqIpHeA+RbxdS6FVyRCnX/Eka0eMfe',
    'bjm+9V3sK1XM0fh8/h7+/ryTnOgvsRkYVoBkLMHk8EU7tBX4JuEPToHk9rRW4z+I',
    'n03PFg2j0a7kEr1ES2XQkKaEkIobky8rxo7BCGFpo4UbG+UV61ThKWNBAoGAHp6N',
    'f+GsdnsvUUsmVYj0Gg4p/0utWfU4AYVlYn38bkamx21oytQXS8Le+tyGuh3qPJ8B',
    'COgFEYaV1AqHl/XbpEtxwy+zUgjscX8rHgeRWTFsh8FzRqHjs1+vEyPahmvE3LpS',
    'BZS4aBLRISBU6aIkLi1hU9U3ORHRjd1T3EnFNqkCgYAl6IwatAXyReQtT8ty6PcI',
    'UtZ2DZIZ0yt8wsvZJFE8d+9qBg6Xw5R9S2PgqbvVq5+u0YvALUt7QYYRkPkgJv31',
    '9CdVAo3hpo7n+XGfuaEArk+qB7jx51koKNWYWBmym601cgaShUX0ADy/Flrqff9z',
    'UzaCj73RzfcK0e2VkFbXng==',
    '-----END PRIVATE KEY-----',
  ].join('\n') + '\n';
const TEST_KEY_PKCS1 = crypto.createPrivateKey(TEST_KEY).export({ type: 'pkcs1', format: 'pem' }).toString();

const T0 = 1767225600000; // 2026-01-01T00:00:00Z
const MIN = 60 * 1000;
const ok = (body) => ({ status: 200, body: JSON.stringify(body) });
const cc = (extra = {}) => ({
  type: 'oauth2',
  tokenUrl: 'https://login.example.test/tenant/oauth2/v2.0/token',
  clientId: 'app-1',
  scope: 'https://cognitiveservices.azure.com/.default',
  grant: 'client_credentials',
  ...extra,
});
const rt = (extra = {}) => cc({ grant: 'refresh_token', scope: '', ...extra });
const google = { type: 'google-sa', scope: 'https://www.googleapis.com/auth/cloud-platform' };
const sa = (fields) => JSON.stringify({ client_email: 'sa@proj.example.test', private_key: TEST_KEY, ...fields });

/** @type {Record<string, { auth: any, secret: string | null, mask?: string[], responses?: any[], steps: any[] }>} */
const CASES = {
  'oauth2-cc-basic': {
    auth: cc(),
    secret: 's3cret-value',
    responses: [ok({ access_token: 'cc-1', expires_in: 3600 }), ok({ access_token: 'cc-2', expires_in: '7200' })],
    steps: [
      { now: T0 },
      { now: T0 + 1000 }, // cached
      { now: T0 + 55 * MIN - 1 }, // just more than the lead left: cached
      { now: T0 + 55 * MIN }, // exactly the lead left: refreshed
      { now: T0 + 56 * MIN },
    ],
  },
  'oauth2-cc-json-secret': {
    auth: cc({ clientId: 'app/1 é~*', scope: 'a b+c&d=e' }),
    secret: JSON.stringify({ client_secret: "x y~*-._!'()é+&=%" }),
    responses: [ok({ access_token: 'tok', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'oauth2-cc-no-secret-field': {
    auth: cc({ scope: '' }),
    secret: JSON.stringify({ other: 1 }),
    responses: [ok({ access_token: 'tok' })],
    steps: [{ now: T0 }],
  },
  'oauth2-cc-numeric-secret': {
    auth: cc(),
    secret: '123456',
    responses: [ok({ access_token: 'tok', expires_in: 600 })],
    steps: [{ now: T0 }],
  },
  'oauth2-cc-force': {
    auth: cc(),
    secret: 'client-secret-1',
    responses: [ok({ access_token: 'a', expires_in: 3600 }), ok({ access_token: 'b', expires_in: 3600 })],
    steps: [{ now: T0 }, { now: T0 + 1000, force: true }, { now: T0 + 2000 }],
  },
  'oauth2-rt-rotation': {
    auth: rt({ clientId: 'c' }),
    secret: JSON.stringify({ client_secret: 'cs-123456', refresh_token: 'rt-0' }),
    responses: [
      ok({ access_token: 'a1', refresh_token: 'rt-1', expires_in: 60 }),
      ok({ access_token: 'a2', expires_in: 3600 }),
      ok({ access_token: 'a3', refresh_token: 'rt-3', expires_in: 3600 }),
    ],
    steps: [{ now: T0 }, { now: T0 + 1000 }, { now: T0 + 2000, force: true }, { now: T0 + 3000 }],
  },
  'oauth2-rt-missing': {
    auth: rt(),
    secret: 'plain-client-secret',
    steps: [{ now: T0 }],
  },
  'oauth2-no-credential': { auth: cc(), secret: null, steps: [{ now: T0 }] },
  'oauth2-empty-credential': { auth: cc(), secret: '', steps: [{ now: T0 }] },
  'oauth2-http-401': {
    auth: cc(),
    secret: 's3cret-value',
    responses: [{ status: 401, body: JSON.stringify({ error: 'invalid_client', error_description: 'AADSTS7000215' }) }],
    steps: [{ now: T0 }],
  },
  'oauth2-error-masked': {
    auth: cc(),
    secret: 's3cret-value',
    mask: ['s3cret-value'],
    responses: [{ status: 400, body: JSON.stringify({ error: 'bad secret s3cret-value' }) }],
    steps: [{ now: T0 }],
  },
  'oauth2-no-access-token': {
    auth: cc(),
    secret: 's',
    responses: [ok({ token_type: 'bearer', error: '' })],
    steps: [{ now: T0 }],
  },
  'oauth2-not-json': {
    auth: cc(),
    secret: 's',
    responses: [
      { status: 200, body: 'not json' },
      { status: 502, body: '<html>bad gateway</html>' },
    ],
    steps: [{ now: T0 }, { now: T0 + 1 }],
  },
  'oauth2-redirect-302': {
    auth: cc(),
    secret: 's',
    responses: [{ status: 302, body: '', headers: { location: 'https://elsewhere.example.test/' } }],
    steps: [{ now: T0 }],
  },
  'oauth2-redirect-307': {
    auth: cc(),
    secret: 's',
    responses: [{ status: 307, body: '{}', headers: { location: 'https://elsewhere.example.test/' } }],
    steps: [{ now: T0 }],
  },
  'oauth2-expires-variants': {
    auth: cc(),
    secret: 's',
    responses: [
      ok({ access_token: 't1' }),
      ok({ access_token: 't2', expires_in: 0 }),
      ok({ access_token: 't3', expires_in: ' 90 ' }),
      ok({ access_token: 't4', expires_in: 'abc' }),
      ok({ access_token: 't5', expires_in: '0x10' }),
      ok({ access_token: 't6', expires_in: -5 }),
      ok({ access_token: 't7', expires_in: 1.5 }),
      ok({ access_token: 't8', expires_in: null }),
    ],
    steps: [1, 2, 3, 4, 5, 6, 7, 8].map((i) => ({ now: T0 + i * 1000, force: true })),
  },
  'oauth2-fail-then-ok': {
    auth: cc(),
    secret: 's',
    responses: [{ status: 503, body: '{}' }, ok({ access_token: 'after', expires_in: 3600 })],
    steps: [{ now: T0 }, { now: T0 + 1000 }, { now: T0 + 2000 }],
  },
  'google-basic': {
    auth: google,
    secret: sa({}),
    responses: [ok({ access_token: 'sa-1', expires_in: 3599 })],
    steps: [{ now: T0 + 1500 }, { now: T0 + 2500 }],
  },
  'google-token-uri': {
    auth: google,
    secret: sa({ token_uri: 'https://token.example.test/oauth2/token' }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-token-uri-empty': {
    auth: google,
    secret: sa({ token_uri: '' }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-pkcs1': {
    auth: google,
    secret: sa({ private_key: TEST_KEY_PKCS1 }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-no-email': {
    auth: google,
    secret: JSON.stringify({ private_key: TEST_KEY }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-null-email': {
    auth: google,
    secret: sa({ client_email: null }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-escapes': {
    auth: { ...google, scope: 'scope "with" \\ escapes\t<>&' },
    secret: sa({ client_email: 'q"\\\u2028\u2029\u001f<>&é@example.test' }),
    responses: [ok({ access_token: 'sa-1', expires_in: 3600 })],
    steps: [{ now: T0 }],
  },
  'google-not-json': { auth: google, secret: '{not json', steps: [{ now: T0 }] },
  'google-no-credential': { auth: google, secret: null, steps: [{ now: T0 }] },
  'google-error': {
    auth: google,
    secret: sa({}),
    responses: [{ status: 400, body: JSON.stringify({ error: 'invalid_grant' }) }],
    steps: [{ now: T0 }],
  },
};

async function runCase(c) {
  const id = 'prov';
  const responses = [...(c.responses || [])];
  const requests = [];
  let now = 0;
  const provider = {
    id,
    preset: 'custom',
    baseUrl: 'https://upstream.example.test',
    ...(c.secret === null ? {} : { apiKey: { env: 'PROVIDER_SECRET' } }),
    models: [],
    enabled: true,
    tier: 'external',
    approved: true,
    auth: c.auth,
  };
  const mask = c.mask || [];
  const fetchImpl = async (url, init) => {
    requests.push({ url, method: init.method, headers: init.headers, body: init.body });
    const r = responses.shift();
    if (!r) throw new Error('no response queued');
    return new Response(r.body === '' ? null : r.body, { status: r.status, headers: r.headers || {} });
  };
  // The background refresh (a 60 s interval) is not part of a step.
  const realSetInterval = globalThis.setInterval;
  globalThis.setInterval = /** @type {any} */ (() => ({ unref() {} }));
  let providers;
  try {
    providers = createProviders({
      getConfig: () => ({ gateway: { providers: [provider] } }),
      secrets: {
        resolve: () => c.secret,
        mask: (s) => mask.reduce((a, v) => a.split(v).join('***'), String(s ?? '')),
      },
      fetchImpl,
      now: () => now,
    });
  } finally {
    globalThis.setInterval = realSetInterval;
  }
  const results = [];
  for (const step of c.steps) {
    now = step.now;
    try {
      const token = await providers.accessToken(provider, step.force === true);
      results.push({ token, exp: providers.quota()[0].token.expiresAt });
    } catch (err) {
      results.push({ throws: err.message });
    }
  }
  if (responses.length) throw new Error(`${responses.length} responses left unused`);
  return {
    fn: 'accessToken',
    in: { id, auth: c.auth, secret: c.secret, mask, responses: c.responses || [], steps: c.steps },
    out: { requests, results },
  };
}

/** @returns {Promise<Map<string, string>>} file name -> file text */
async function generate() {
  const files = new Map();
  for (const [name, c] of Object.entries(CASES)) {
    try {
      files.set(`${name}.json`, `${JSON.stringify(await runCase(c), null, 2)}\n`);
    } catch (err) {
      throw new Error(`${name}: ${err.message}`);
    }
  }
  return files;
}

if (require.main === module) {
  generate().then((files) => {
    fs.mkdirSync(DIR, { recursive: true });
    for (const f of fs.readdirSync(DIR)) if (f.endsWith('.json') && !files.has(f)) fs.rmSync(path.join(DIR, f));
    for (const [name, text] of files) fs.writeFileSync(path.join(DIR, name), text);
    console.log(`${files.size} fixtures in ${path.relative(process.cwd(), DIR)}`);
  });
}

module.exports = { generate, DIR };

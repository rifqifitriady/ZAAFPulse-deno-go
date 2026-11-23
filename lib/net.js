// Fetching addresses that someone else typed (a URL to ingest, a link in a document) without letting
// them reach this machine or its network: loopback, private, link-local and cloud-metadata addresses are
// refused, every redirect is checked again, the body is capped and the request times out. Addresses
// the owner configures on purpose (Ollama, a local Qdrant) use plain fetch, not this.
const dns = require('dns').promises;
const http = require('http');
const https = require('https');
const net = require('net');
const egress = require('./egress');

const MAX_REDIRECTS = 5;

// The eight 16-bit groups of an IPv6 address (text net.isIPv6 accepts; a zone is ignored), an embedded dotted IPv4
// tail included.
function hextets(a) {
  let head = a.replace(/%.*$/, '');
  const tail = [];
  const m = head.match(/^(.*:)(\d+\.\d+\.\d+\.\d+)$/);
  if (m) {
    const [p, q, r, s] = m[2].split('.').map(Number);
    tail.push((p << 8) | q, (r << 8) | s);
    head = m[1].endsWith('::') ? m[1] : m[1].slice(0, -1);
  }
  const groups = (t) => (t ? t.split(':').map((x) => parseInt(x, 16)) : []);
  const [left, right] = head.split('::');
  if (right === undefined) return [...groups(left), ...tail];
  const l = groups(left);
  const r = groups(right);
  return [...l, ...new Array(8 - tail.length - l.length - r.length).fill(0), ...r, ...tail];
}

const dotted = (hi, lo) => `${hi >> 8}.${hi & 255}.${lo >> 8}.${lo & 255}`;

// IPv6 ranges judged on the groups (gw/egress IsPrivate makes the same tests on the bytes): an IPv4-mapped address as
// its IPv4 address; ::/96 (unspecified, loopback, the deprecated IPv4-compatible ::a.b.c.d); NAT64 64:ff9b::/32
// (the well-known /96 and the local-use 64:ff9b:1::/48); unique-local fc00::/7; link-local fe80::/10; the deprecated
// site-local fec0::/10; multicast ff00::/8; Teredo 2001:0::/32 (its IPv4 server and client are hidden in it); and
// 6to4 2002::/16 when the IPv4 address it embeds is private.
function isPrivateV6(h) {
  if (h.slice(0, 5).every((x) => x === 0) && h[5] === 0xffff) return isPrivateAddress(dotted(h[6], h[7]));
  return (
    h.slice(0, 6).every((x) => x === 0) ||
    (h[0] === 0x64 && h[1] === 0xff9b) ||
    (h[0] & 0xfe00) === 0xfc00 ||
    (h[0] & 0xffc0) === 0xfe80 ||
    (h[0] & 0xffc0) === 0xfec0 ||
    (h[0] & 0xff00) === 0xff00 ||
    (h[0] === 0x2001 && h[1] === 0) ||
    (h[0] === 0x2002 && isPrivateAddress(dotted(h[1], h[2])))
  );
}

// True for addresses a public URL must never resolve to.
function isPrivateAddress(ip) {
  let a = String(ip).toLowerCase();
  if (a.startsWith('::ffff:') && net.isIPv4(a.slice(7))) a = a.slice(7); // IPv4-mapped IPv6
  if (net.isIPv4(a)) {
    const [p, q] = a.split('.').map(Number);
    return (
      p === 0 ||
      p === 10 ||
      p === 127 || // this network, private, loopback
      (p === 100 && q >= 64 && q <= 127) || // carrier-grade NAT
      (p === 169 && q === 254) || // link-local, cloud metadata
      (p === 172 && q >= 16 && q <= 31) ||
      (p === 192 && q === 168) ||
      (p === 192 && q === 0) || // IETF protocol assignments
      (p === 198 && (q === 18 || q === 19)) || // benchmarking
      p >= 224 // multicast and reserved
    );
  }
  if (net.isIPv6(a))
    return (
      a === '::' ||
      a === '::1' ||
      /^f[cd]/.test(a) ||
      /^fe[89ab]/.test(a) ||
      /^ff/.test(a) ||
      /^64:ff9b:/.test(a) ||
      isPrivateV6(hextets(a))
    );
  return true; // not an address at all
}

async function assertPublic(url, { lookup = dns.lookup } = {}) {
  let u;
  try {
    u = new URL(url);
  } catch {
    throw new Error('not a valid URL');
  }
  if (!['http:', 'https:'].includes(u.protocol)) throw new Error('only http:// and https:// URLs can be fetched');
  if (u.username || u.password) throw new Error('URLs with a user name or password are not fetched');
  const host = u.hostname.replace(/^\[|\]$/g, '');
  const addrs = net.isIP(host)
    ? [{ address: host, family: net.isIP(host) }]
    : await lookup(host, { all: true, verbatim: true });
  if (!addrs.length) throw new Error(`${host} does not resolve`);
  const bad = addrs.find((x) => isPrivateAddress(x.address));
  if (bad) throw new Error(`${host} points to a private or local address (${bad.address}); it is not fetched`);
  return { url: u, address: addrs[0].address, family: addrs[0].family || net.isIP(addrs[0].address) };
}

// One request to an address that was already checked; the connection is pinned to that address so a
// second DNS answer (DNS rebinding) cannot send it elsewhere.
function requestPinned({ url, address, family }, { headers, signal, maxBytes }) {
  const mod = url.protocol === 'https:' ? https : http;
  return new Promise((resolve, reject) => {
    const req = mod.request(
      url,
      {
        method: 'GET',
        headers,
        signal,
        lookup: (_h, o, cb) => (o && o.all ? cb(null, [{ address, family }]) : cb(null, address, family)),
        servername: url.hostname,
      },
      (res) => {
        const chunks = [];
        let size = 0;
        res.on('data', (c) => {
          size += c.length;
          if (size > maxBytes) {
            req.destroy(tooLarge(maxBytes));
            return;
          }
          chunks.push(c);
        });
        res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
        res.on('error', reject);
      },
    );
    req.on('error', reject);
    req.end();
  });
}

const tooLarge = (maxBytes) => new Error(`the page is larger than ${Math.round(maxBytes / 1048576)} MB`);

// The checks of assertPublic that need no DNS: the URL, its scheme, no credentials, and an address literal or a
// loopback name is not private. A name is checked by the gateway at connect time (public mode, P5 Decision 7).
function assertPublicName(url) {
  let u;
  try {
    u = new URL(url);
  } catch {
    throw new Error('not a valid URL');
  }
  if (!['http:', 'https:'].includes(u.protocol)) throw new Error('only http:// and https:// URLs can be fetched');
  if (u.username || u.password) throw new Error('URLs with a user name or password are not fetched');
  const host = u.hostname.replace(/^\[|\]$/g, '');
  if (net.isIP(host) && isPrivateAddress(host))
    throw new Error(`${host} points to a private or local address (${host}); it is not fetched`);
  if (egress.isLoopbackHost(host))
    throw new Error(`${host} points to a private or local address (${host}); it is not fetched`);
  return u;
}

// A gateway failure in safeFetch's own words, as close to the direct path's as the code allows.
function egressError(err, u) {
  const c = err && err.cause;
  if (!(err instanceof TypeError) || !c || typeof c !== 'object' || !c.code) return err;
  const host = u.hostname.replace(/^\[|\]$/g, '');
  if (c.code === 'EGRESS_PRIVATE') {
    // Go's message starts with the address it refused ("<addr>[:port] is a private or local address; ...").
    let addr = String(c.message || '').split(' ')[0];
    const m = /^\[?([^\]]*?)\]?:\d+$/.exec(addr);
    if (!net.isIP(addr) && m && net.isIP(m[1])) addr = m[1];
    return new Error(
      `${host} points to a private or local address (${net.isIP(addr) ? addr : host}); it is not fetched`,
    );
  }
  if (c.code === 'ENOTFOUND') return new Error(`${host} does not resolve`);
  return new Error(c.message || c.code);
}

// One hop through the gateway's egress endpoint in public mode (Go refuses a private address at connect, so no DNS
// here and no rebinding); the answer in requestPinned's shape.
async function requestEgress(u, { headers, signal, maxBytes }) {
  let res;
  try {
    res = await egress.fetchFor('public')(u.toString(), { headers, signal, redirect: 'manual', egressPublic: true });
  } catch (err) {
    throw egressError(err, u);
  }
  const chunks = [];
  let size = 0;
  if (res.body) {
    const reader = res.body.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > maxBytes) {
        await reader.cancel().catch(() => {});
        throw tooLarge(maxBytes);
      }
      chunks.push(Buffer.from(value));
    }
  }
  return { status: res.status, headers: egress.nodeHeaders(res.headers), body: Buffer.concat(chunks) };
}

// Whether untrusted URLs go through the gateway: the class 'public' on 'go', or the lock-down (where fetchFor fails
// with EGRESS_UNAVAILABLE when no gateway is ready).
function publicViaEgress() {
  const s = egress.state();
  return s.lockdown || (s.configured && s.engines.public === 'go');
}

// GET for untrusted URLs. Returns { status, ok, headers, url, text } with at most maxBytes of body. With the class
// 'public' on 'go' (P5 E12) each hop goes through the gateway in public mode; otherwise the address is resolved and
// checked here and the connection pinned to it (and a 'check' asks the gateway's verdict in the background).
async function safeFetch(url, { headers = {}, timeoutMs = 20000, maxBytes = 5 * 1024 * 1024, lookup } = {}) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  try {
    let current = url;
    for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
      let r;
      if (publicViaEgress()) {
        r = await requestEgress(assertPublicName(current), { headers, signal: ctl.signal, maxBytes });
      } else {
        const target = await assertPublic(current, { lookup });
        egress.check('public', target.url.toString(), { headers, redirect: 'manual', egressPublic: true });
        r = await requestPinned(target, { headers, signal: ctl.signal, maxBytes });
      }
      if (r.status >= 300 && r.status < 400 && r.headers.location) {
        current = new URL(r.headers.location, current).toString();
        continue;
      }
      return {
        status: r.status,
        ok: r.status >= 200 && r.status < 300,
        headers: r.headers,
        url: current,
        text: r.body.toString('utf8'),
      };
    }
    throw new Error('too many redirects');
  } catch (err) {
    throw new Error(err.name === 'AbortError' ? 'timed out' : err.message);
  } finally {
    clearTimeout(timer);
  }
}

// fetch that carries credentials (API keys in headers): a redirect is followed only within the same
// origin. fetch itself drops Authorization on a cross-origin redirect but keeps x-api-key, api-key and
// other custom headers, so another host is refused instead of being sent the key.
async function fetchSameOrigin(fetchImpl, url, init = {}) {
  let current = String(url);
  for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
    const r = await fetchImpl(current, { ...init, redirect: 'manual' });
    const location = r.status >= 300 && r.status < 400 && r.headers.get('location');
    if (!location) return r;
    const next = new URL(location, current);
    if (next.origin !== new URL(current).origin) {
      await r.body?.cancel?.().catch(() => {});
      throw new Error(
        `refused a redirect from ${new URL(current).host} to ${next.host}; credentials are not forwarded to another host`,
      );
    }
    await r.body?.cancel?.().catch(() => {});
    current = next.toString();
  }
  throw new Error('too many redirects');
}

module.exports = { safeFetch, fetchSameOrigin, assertPublic, isPrivateAddress };

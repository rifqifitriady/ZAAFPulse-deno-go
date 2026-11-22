// test/egress-lockdown.test.js: launched with a CLI --allow-net that names the cloud metadata address, it must still be
// refused by the set's --deny-net (deny wins), before any packet. Prints one JSON line.
fetch('http://169.254.169.254/latest/meta-data/', { signal: AbortSignal.timeout(3000) }).then(
  (r) => console.log(JSON.stringify({ ok: true, status: r.status })),
  (e) => console.log(JSON.stringify({ name: e.name, message: String(e.message) })),
);

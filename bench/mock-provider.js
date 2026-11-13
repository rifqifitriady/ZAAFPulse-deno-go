// A mock OpenAI-compatible provider for the gateway benchmark (P0 T4). Node standard library only, so it runs unchanged
// under Node and Deno. POST /v1/chat/completions answers after a fixed delay: whole, or as an SSE stream of `chunks`
// chunks `intervalMs` apart with usage in the final chunk. GET /v1/models lists the one model. Nothing else.
const http = require('http');

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// startMock({ delayMs, chunks, intervalMs }) -> { url, port, close() }. url is the base URL a provider is saved with.
async function startMock({ delayMs = 20, chunks = 20, intervalMs = 25 } = {}) {
  // Which engine called (P4c): Go's client says Go-http-client/…, the JS gateway's fetch something else. GET /_stats
  // answers the counts and resets them.
  const counts = { go: 0, js: 0 };
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (c) => {
      body += c;
    });
    req.on('end', async () => {
      if (req.method === 'GET' && req.url === '/_stats') {
        const out = { ...counts };
        counts.go = counts.js = 0;
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(JSON.stringify(out));
      }
      if (req.method === 'POST') counts[/^Go-http-client\//.test(req.headers['user-agent'] || '') ? 'go' : 'js']++;
      if (req.method === 'GET' && req.url === '/v1/models') {
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(JSON.stringify({ object: 'list', data: [{ id: 'bench-1', object: 'model', created: 0 }] }));
      }
      if (req.method !== 'POST' || req.url !== '/v1/chat/completions') {
        res.writeHead(404, { 'content-type': 'application/json' });
        return res.end('{"error":{"message":"no route"}}');
      }
      let b = {};
      try {
        b = JSON.parse(body);
      } catch {
        /* answered as whole */
      }
      await sleep(delayMs);
      const usage = { prompt_tokens: 12, completion_tokens: chunks };
      if (!b.stream) {
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(
          JSON.stringify({
            id: 'bench',
            object: 'chat.completion',
            created: 0,
            model: b.model || 'bench-1',
            choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }],
            usage,
          }),
        );
      }
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' });
      const chunk = (c) =>
        res.write(`data: ${JSON.stringify({ id: 'bench', object: 'chat.completion.chunk', ...c })}\n\n`);
      for (let i = 0; i < chunks; i++) {
        if (res.destroyed) return;
        chunk({ choices: [{ index: 0, delta: { content: 'x' }, finish_reason: i === chunks - 1 ? 'stop' : null }] });
        if (i < chunks - 1) await sleep(intervalMs);
      }
      chunk({ choices: [], usage });
      res.end('data: [DONE]\n\n');
    });
  });
  server.keepAliveTimeout = 60000;
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();
  return {
    port,
    url: `http://127.0.0.1:${port}/v1`,
    close: () =>
      new Promise((resolve) => {
        server.closeAllConnections?.();
        server.close(resolve);
      }),
  };
}

module.exports = { startMock };

// `node bench/mock-provider.js` serves on its own until stopped (bench/run.js runs it so, as a child process), with
// ZP_MOCK_DELAY_MS, ZP_MOCK_CHUNKS and ZP_MOCK_INTERVAL_MS.
if (require.main === module) {
  const env = (name, def) => (Number(process.env[name]) > 0 ? Number(process.env[name]) : def);
  startMock({
    delayMs: env('ZP_MOCK_DELAY_MS', 20),
    chunks: env('ZP_MOCK_CHUNKS', 20),
    intervalMs: env('ZP_MOCK_INTERVAL_MS', 25),
  }).then((m) => console.log(`mock provider at ${m.url} (POST /chat/completions, GET /models)`));
}

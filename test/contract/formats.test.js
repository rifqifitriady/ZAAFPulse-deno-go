// Contract: the wire formats of /v1/* against each provider type the gateway translates to (OpenAI chat, Anthropic
// Messages, native Responses), whole and streamed. Black-box: status, headers, body, SSE events, the request the
// upstream received, and the usage record read back through /api/gateway/logs.
const test = require('node:test');
const assert = require('node:assert/strict');
const { start, sseEvents, UPSTREAM_SECRET } = require('./harness');
const { TOOL_CALL } = require('./upstream');

let h;
test.before(async () => {
  h = await start();
});
test.after(async () => {
  if (h) await h.stop();
});

const user = (content = 'hi') => [{ role: 'user', content }];
const lastUp = (path) => h.upstream.requests.filter((r) => r.path === path).at(-1);
const xKey = () => ({ 'x-api-key': h.key });
const gKey = () => ({ 'x-goog-api-key': h.key });
const ups = (path) => h.upstream.requests.filter((r) => r.path === path);
const chat = (body, opts = {}) => h.call('/v1/chat/completions', { key: h.key, body, ...opts });
const messages = (body, headers = {}) => h.call('/v1/messages', { headers: { ...xKey(), ...headers }, body });
const responses = (body) => h.call('/v1/responses', { key: h.key, body });
const gemini = (model, method, body, query = '') =>
  h.call(`/v1beta/models/${encodeURIComponent(model)}:${method}${query}`, { headers: gKey(), body });
const gContents = (text = 'hi') => ({ contents: [{ role: 'user', parts: [{ text }] }] });
const dataChunks = (text) =>
  sseEvents(text)
    .map((e) => e.data)
    .filter((d) => d && typeof d === 'object');
const WEATHER_FN = {
  type: 'function',
  function: { name: 'get_weather', parameters: { type: 'object' } },
};
const WEATHER_TOOL_ANTHROPIC = { name: 'get_weather', input_schema: { type: 'object' } };

const CASES = [
  {
    name: 'chat → OpenAI provider, whole: the requested model name, the upstream model and credential',
    run: async () => {
      const r = await h.call('/v1/chat/completions', { key: h.key, body: { model: 'oa/m1', messages: user() } });
      assert.equal(r.status, 200);
      assert.equal(r.headers.get('content-type'), 'application/json');
      assert.equal(r.json.object, 'chat.completion');
      assert.equal(r.json.model, 'oa/m1');
      assert.equal(r.json.choices[0].message.content, 'hi');
      const up = lastUp('/chat/completions');
      assert.equal(up.body.model, 'm1');
      assert.equal(up.headers.authorization, `Bearer ${UPSTREAM_SECRET.oa}`);
    },
  },
  {
    name: 'chat → OpenAI provider, stream: chunks pass with the requested model, [DONE] last, usage asked upstream',
    run: async () => {
      const r = await h.call('/v1/chat/completions', {
        key: h.key,
        body: { model: 'oa/m1', stream: true, messages: user() },
      });
      assert.equal(r.status, 200);
      assert.equal(r.headers.get('content-type'), 'text/event-stream');
      assert.ok(r.text.startsWith(': keep-alive\n\n'));
      assert.ok(r.text.endsWith('data: [DONE]\n\n'));
      const chunks = sseEvents(r.text)
        .map((e) => e.data)
        .filter((d) => d && typeof d === 'object');
      assert.deepEqual(
        chunks.map((c) => c.choices[0].delta.content),
        ['Hel', 'lo'],
      );
      assert.ok(chunks.every((c) => c.model === 'oa/m1' && !('usage' in c)));
      assert.equal(lastUp('/chat/completions').body.stream_options.include_usage, true);
    },
  },
  {
    name: '/v1/messages → Anthropic provider, stream: message_start first with the requested model, message_stop last',
    run: async () => {
      const r = await h.call('/v1/messages', {
        headers: xKey(),
        body: { model: 'an/claude-x', max_tokens: 10, stream: true, messages: user() },
      });
      assert.equal(r.status, 200);
      const ev = sseEvents(r.text);
      assert.equal(ev[0].event, 'message_start');
      assert.equal(ev[0].data.message.model, 'an/claude-x');
      assert.equal(ev.at(-1).event, 'message_stop');
      const up = lastUp('/messages');
      assert.equal(up.body.model, 'claude-x');
      assert.equal(up.headers['x-api-key'], UPSTREAM_SECRET.an);
    },
  },
  {
    name: 'Gemini streamGenerateContent → OpenAI provider: candidates per chunk, STOP and usageMetadata last',
    run: async () => {
      const r = await h.call('/v1beta/models/oa%2Fm1:streamGenerateContent?alt=sse', {
        headers: gKey(),
        body: { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] },
      });
      assert.equal(r.status, 200);
      assert.equal(r.headers.get('content-type'), 'text/event-stream');
      const ev = sseEvents(r.text).map((e) => e.data);
      assert.deepEqual(
        ev.slice(0, 2).map((d) => d.candidates[0].content.parts[0].text),
        ['Hel', 'lo'],
      );
      assert.equal(ev.at(-1).candidates[0].finishReason, 'STOP');
      assert.equal(ev.at(-1).usageMetadata.promptTokenCount, 10);
      assert.ok(ev.every((d) => d.modelVersion === 'oa/m1'));
    },
  },
  {
    name: 'chat → OpenAI provider, stream with include_usage: the usage chunk is passed to the client',
    run: async () => {
      const r = await chat({ model: 'oa/m1', stream: true, stream_options: { include_usage: true }, messages: user() });
      assert.equal(r.status, 200);
      const chunks = dataChunks(r.text);
      assert.equal(chunks.length, 3);
      assert.equal(chunks[0].usage, null);
      assert.deepEqual(chunks.at(-1).choices, []);
      assert.equal(chunks.at(-1).usage.prompt_tokens, 10);
    },
  },
  {
    name: "chat → OpenAI provider, whole: the upstream's own model name is replaced; no stream usage asked",
    run: async () => {
      const r = await chat({ model: 'oa/m1', messages: user() });
      assert.equal(r.json.model, 'oa/m1');
      assert.equal('stream_options' in lastUp('/chat/completions').body, false);
    },
  },
  {
    name: 'chat → oa/gpt-5 with max_tokens: sent upstream as max_completion_tokens',
    run: async () => {
      const r = await chat({ model: 'oa/gpt-5', max_tokens: 100, messages: user() });
      assert.equal(r.status, 200);
      const b = lastUp('/chat/completions').body;
      assert.equal(b.max_completion_tokens, 100);
      assert.equal('max_tokens' in b, false);
    },
  },
  {
    name: 'chat → oa/m1 with max_tokens: sent upstream as max_tokens',
    run: async () => {
      await chat({ model: 'oa/m1', max_tokens: 100, messages: user() });
      const b = lastUp('/chat/completions').body;
      assert.equal(b.max_tokens, 100);
      assert.equal('max_completion_tokens' in b, false);
    },
  },
  {
    name: "chat → OpenAI provider keeps the caller's own fields",
    run: async () => {
      await chat({
        model: 'oa/m1',
        temperature: 0.2,
        top_p: 0.5,
        stop: ['\n'],
        reasoning_effort: 'high',
        messages: user(),
      });
      const b = lastUp('/chat/completions').body;
      assert.deepEqual([b.temperature, b.top_p, b.stop, b.reasoning_effort], [0.2, 0.5, ['\n'], 'high']);
    },
  },
  {
    name: 'chat → oa/tools: a tool call answer passes through',
    run: async () => {
      const r = await chat({ model: 'oa/tools', tools: [WEATHER_FN], messages: user() });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json.choices[0].message.tool_calls, [TOOL_CALL]);
      assert.equal(r.json.choices[0].finish_reason, 'tool_calls');
    },
  },
  {
    name: 'chat → Anthropic provider, whole: translated answer and usage; x-api-key and anthropic-version upstream',
    run: async () => {
      const r = await chat({ model: 'an/claude-x', messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.object, 'chat.completion');
      assert.equal(r.json.model, 'an/claude-x');
      assert.equal(r.json.choices[0].message.content, 'ok');
      assert.deepEqual(
        [r.json.usage.prompt_tokens, r.json.usage.completion_tokens, r.json.usage.total_tokens],
        [10, 2, 12],
      );
      const up = lastUp('/messages');
      assert.equal(up.headers['x-api-key'], UPSTREAM_SECRET.an);
      assert.equal(up.headers['anthropic-version'], '2023-06-01');
    },
  },
  {
    name: 'chat → Anthropic provider, stream: OpenAI chunks with the requested model, [DONE] last',
    run: async () => {
      const r = await chat({ model: 'an/claude-x', stream: true, messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.headers.get('content-type'), 'text/event-stream');
      assert.ok(r.text.endsWith('data: [DONE]\n\n'));
      const chunks = dataChunks(r.text);
      assert.ok(chunks.every((c) => c.model === 'an/claude-x'));
      const withChoice = chunks.filter((c) => c.choices && c.choices.length);
      assert.deepEqual(
        withChoice.map((c) => c.choices[0].delta),
        [{ role: 'assistant', content: '' }, { content: 'ok' }, {}],
      );
      assert.equal(withChoice.at(-1).choices[0].finish_reason, 'stop');
    },
  },
  {
    name: 'chat → an/tools: tools translated both ways',
    run: async () => {
      const r = await chat({ model: 'an/tools', tools: [WEATHER_FN], messages: user() });
      assert.equal(r.status, 200);
      assert.deepEqual(lastUp('/messages').body.tools, [
        { name: 'get_weather', description: '', input_schema: { type: 'object' } },
      ]);
      assert.deepEqual(r.json.choices[0].message.tool_calls[0], {
        id: 'toolu_1',
        type: 'function',
        function: { name: 'get_weather', arguments: '{"city":"Paris"}' },
      });
      assert.equal(r.json.choices[0].finish_reason, 'tool_calls');
    },
  },
  {
    name: 'chat → Anthropic provider: temperature clamped to 1, top_p dropped when temperature is set',
    run: async () => {
      await chat({ model: 'an/claude-x', temperature: 1.5, top_p: 0.9, messages: user() });
      const b = lastUp('/messages').body;
      assert.equal(b.temperature, 1);
      assert.equal('top_p' in b, false);
    },
  },
  {
    name: 'chat → Anthropic provider: parallel_tool_calls false is disable_parallel_tool_use',
    run: async () => {
      await chat({ model: 'an/claude-x', tools: [WEATHER_FN], parallel_tool_calls: false, messages: user() });
      assert.deepEqual(lastUp('/messages').body.tool_choice, { type: 'auto', disable_parallel_tool_use: true });
    },
  },
  {
    name: 'chat → Anthropic provider: reasoning_effort high is thinking with a budget, temperature dropped',
    run: async () => {
      await chat({ model: 'an/claude-x', reasoning_effort: 'high', temperature: 0.2, messages: user() });
      const b = lastUp('/messages').body;
      assert.deepEqual(b.thinking, { type: 'enabled', budget_tokens: 16384 });
      assert.equal(b.max_tokens, 24576);
      assert.equal('temperature' in b, false);
    },
  },
  {
    name: 'chat → Anthropic provider: a system message becomes system',
    run: async () => {
      await chat({
        model: 'an/claude-x',
        messages: [{ role: 'system', content: 'be brief' }, ...user()],
      });
      const b = lastUp('/messages').body;
      assert.equal(b.system, 'be brief');
      assert.deepEqual(
        b.messages.map((m) => m.role),
        ['user'],
      );
    },
  },
  {
    name: 'chat → private provider, whole: no credential upstream',
    run: async () => {
      const r = await chat({ model: 'pv/m1', messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'pv/m1');
      assert.equal('authorization' in lastUp('/chat/completions').headers, false);
    },
  },
  {
    name: 'chat → private provider, stream',
    run: async () => {
      const r = await chat({ model: 'pv/m1', stream: true, messages: user() });
      assert.equal(r.headers.get('content-type'), 'text/event-stream');
      assert.deepEqual(
        dataChunks(r.text).map((c) => c.choices[0].delta.content),
        ['Hel', 'lo'],
      );
      assert.ok(r.text.endsWith('data: [DONE]\n\n'));
    },
  },
  {
    name: 'chat → LiteLLM preset on automatic tier',
    run: async () => {
      const r = await chat({ model: 'lr/m1', messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'lr/m1');
    },
  },
  {
    name: 'chat → router/fallback: the failing target is tried first, the next one answers',
    run: async () => {
      const r = await chat({ model: 'router/fallback', messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'router/fallback');
      assert.deepEqual(
        ups('/chat/completions')
          .slice(-2)
          .map((x) => x.body.model),
        ['fail503', 'm1'],
      );
    },
  },
  {
    name: '/v1/messages → Anthropic provider, whole',
    run: async () => {
      const r = await messages({ model: 'an/claude-x', max_tokens: 10, messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.type, 'message');
      assert.equal(r.json.model, 'an/claude-x');
      assert.deepEqual(r.json.content, [{ type: 'text', text: 'ok' }]);
      assert.equal(lastUp('/messages').body.model, 'claude-x');
    },
  },
  {
    name: "/v1/messages → Anthropic provider forwards the client's Anthropic headers, never the gateway key",
    run: async () => {
      await messages(
        { model: 'an/claude-x', max_tokens: 10, messages: user() },
        { 'anthropic-version': '2023-06-01', 'anthropic-beta': 'context-1m-2025-08-07' },
      );
      const up = lastUp('/messages').headers;
      assert.equal(up['anthropic-version'], '2023-06-01');
      assert.equal(up['anthropic-beta'], 'context-1m-2025-08-07');
      assert.equal(up['x-api-key'], UPSTREAM_SECRET.an);
      assert.doesNotMatch(JSON.stringify(up), new RegExp(h.key));
    },
  },
  {
    name: '/v1/messages → OpenAI provider, whole: an Anthropic message',
    run: async () => {
      const r = await messages({ model: 'oa/m1', max_tokens: 10, messages: user() });
      assert.equal(r.status, 200);
      assert.equal(r.json.type, 'message');
      assert.equal(r.json.model, 'oa/m1');
      assert.deepEqual(r.json.content, [{ type: 'text', text: 'hi' }]);
      assert.equal(r.json.stop_reason, 'end_turn');
      assert.deepEqual([r.json.usage.input_tokens, r.json.usage.output_tokens], [1000, 500]);
      assert.equal(h.upstream.requests.at(-1).path, '/chat/completions');
    },
  },
  {
    name: '/v1/messages → OpenAI provider, stream: the Anthropic event sequence',
    run: async () => {
      const r = await messages({ model: 'oa/m1', max_tokens: 10, stream: true, messages: user() });
      assert.equal(r.status, 200);
      const ev = sseEvents(r.text);
      assert.deepEqual(
        ev.map((e) => e.event),
        [
          'message_start',
          'ping',
          'content_block_start',
          'content_block_delta',
          'content_block_delta',
          'content_block_stop',
          'message_delta',
          'message_stop',
        ],
      );
      assert.deepEqual(
        ev.filter((e) => e.event === 'content_block_delta').map((e) => e.data.delta.text),
        ['Hel', 'lo'],
      );
      assert.equal(ev[0].data.message.model, 'oa/m1');
      assert.equal(ev.find((e) => e.event === 'message_delta').data.usage.output_tokens, 2);
    },
  },
  {
    name: '/v1/messages → oa/tools: system and tools translated, the tool call answered as tool_use',
    run: async () => {
      const r = await messages({
        model: 'oa/tools',
        max_tokens: 10,
        system: 'be brief',
        tools: [WEATHER_TOOL_ANTHROPIC],
        messages: user(),
      });
      assert.equal(r.status, 200);
      const b = lastUp('/chat/completions').body;
      assert.deepEqual(b.messages[0], { role: 'system', content: 'be brief' });
      assert.deepEqual(b.tools[0], {
        type: 'function',
        function: { name: 'get_weather', description: '', parameters: { type: 'object' } },
      });
      assert.deepEqual(r.json.content, [
        { type: 'tool_use', id: 'call_1', name: 'get_weather', input: { city: 'Paris' } },
      ]);
      assert.equal(r.json.stop_reason, 'tool_use');
    },
  },
  {
    name: '/v1/messages → oa/gpt-5 with thinking and a server tool: max_completion_tokens, reasoning, x-zp-dropped',
    run: async () => {
      const r = await messages({
        model: 'oa/gpt-5',
        max_tokens: 3000,
        thinking: { type: 'enabled', budget_tokens: 2000 },
        tools: [{ type: 'web_search_20250305', name: 'web_search' }],
        messages: user(),
      });
      assert.equal(r.status, 200);
      const b = lastUp('/chat/completions').body;
      assert.equal(b.max_completion_tokens, 3000);
      assert.equal('max_tokens' in b, false);
      assert.equal(b.reasoning_effort, 'low');
      assert.equal(r.headers.get('x-zp-dropped'), 'tool web_search');
    },
  },
  {
    name: '/v1/messages → oa/m1 with the same body: max_tokens, no reasoning, both dropped features named',
    run: async () => {
      const r = await messages({
        model: 'oa/m1',
        max_tokens: 3000,
        thinking: { type: 'enabled', budget_tokens: 2000 },
        tools: [{ type: 'web_search_20250305', name: 'web_search' }],
        messages: user(),
      });
      assert.equal(r.status, 200);
      const b = lastUp('/chat/completions').body;
      assert.equal(b.max_tokens, 3000);
      assert.equal('reasoning_effort' in b, false);
      assert.equal(r.headers.get('x-zp-dropped'), 'tool web_search, reasoning');
    },
  },
  {
    name: 'x-zp-dropped is exposed to browsers',
    run: async () => {
      const r = await messages({
        model: 'oa/m1',
        max_tokens: 10,
        tools: [{ type: 'web_search_20250305', name: 'web_search' }],
        messages: user(),
      });
      assert.match(r.headers.get('access-control-expose-headers') || '', /\bx-zp-dropped\b/);
    },
  },
  {
    name: '/v1/messages without messages: 400 in the Anthropic shape',
    run: async () => {
      const r = await messages({ model: 'an/claude-x', max_tokens: 10 });
      assert.equal(r.status, 400);
      assert.deepEqual(r.json, {
        type: 'error',
        error: { type: 'invalid_request_error', message: 'messages is required' },
      });
    },
  },
  {
    name: 'count_tokens on a native Anthropic model asks the provider',
    run: async () => {
      const r = await h.call('/v1/messages/count_tokens', {
        headers: xKey(),
        body: { model: 'an/claude-x', messages: user() },
      });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json, { input_tokens: 4242 });
      assert.equal(lastUp('/messages/count_tokens').body.model, 'claude-x');
    },
  },
  {
    name: 'count_tokens on an OpenAI model is estimated without an upstream call',
    run: async () => {
      const before = h.upstream.requests.length;
      const r = await h.call('/v1/messages/count_tokens', {
        headers: xKey(),
        body: { model: 'oa/m1', messages: user() },
      });
      assert.equal(r.status, 200);
      assert.ok(r.json.input_tokens > 0 && r.json.input_tokens < 100, String(r.json.input_tokens));
      assert.equal(h.upstream.requests.length, before);
    },
  },
  {
    name: 'count_tokens on a native model whose count fails falls back to the estimate',
    run: async () => {
      const r = await h.call('/v1/messages/count_tokens', {
        headers: xKey(),
        body: { model: 'an/broken', messages: user() },
      });
      assert.equal(r.status, 200);
      assert.ok(r.json.input_tokens > 0 && r.json.input_tokens < 100, String(r.json.input_tokens));
    },
  },
  {
    name: 'count_tokens without a model is estimated',
    run: async () => {
      const r = await h.call('/v1/messages/count_tokens', { headers: xKey(), body: { messages: user('hello') } });
      assert.equal(r.status, 200);
      assert.ok(r.json.input_tokens > 0);
    },
  },
  {
    name: 'count_tokens counts the system prompt and the tools',
    run: async () => {
      const count = async (extra) =>
        (await h.call('/v1/messages/count_tokens', { headers: xKey(), body: { messages: user('hello'), ...extra } }))
          .json.input_tokens;
      const bare = await count({});
      const full = await count({ system: 'be brief please', tools: [WEATHER_TOOL_ANTHROPIC] });
      assert.ok(full > bare, `${full} <= ${bare}`);
    },
  },
  {
    name: '/v1/responses → OpenAI provider, translated, whole',
    run: async () => {
      const r = await responses({ model: 'oa/m1', input: 'hi' });
      assert.equal(r.status, 200);
      assert.deepEqual(
        [r.json.object, r.json.status, r.json.model, r.json.output_text, r.json.usage.input_tokens],
        ['response', 'completed', 'oa/m1', 'hi', 1000],
      );
      assert.equal(h.upstream.requests.at(-1).path, '/chat/completions');
    },
  },
  {
    name: '/v1/responses → OpenAI provider, translated, stream',
    run: async () => {
      const r = await responses({ model: 'oa/m1', input: 'hi', stream: true });
      assert.equal(r.status, 200);
      const ev = sseEvents(r.text);
      assert.equal(ev[0].event, 'response.created');
      assert.equal(ev[0].data.response.model, 'oa/m1');
      assert.ok(ev.some((e) => e.event === 'response.output_text.delta'));
      assert.equal(ev.at(-1).event, 'response.completed');
    },
  },
  {
    name: '/v1/responses → OpenAI provider: max_output_tokens is max_tokens',
    run: async () => {
      await responses({ model: 'oa/m1', input: 'hi', max_output_tokens: 77 });
      assert.equal(lastUp('/chat/completions').body.max_tokens, 77);
    },
  },
  {
    name: '/v1/responses → OpenAI provider with previous_response_id: 400',
    run: async () => {
      const r = await responses({ model: 'oa/m1', input: 'hi', previous_response_id: 'resp_0' });
      assert.equal(r.status, 400);
      assert.equal(r.json.error.type, 'invalid_request_error');
      assert.match(r.json.error.message, /previous_response_id/);
    },
  },
  {
    name: '/v1/responses → OpenAI provider: instructions become the system message',
    run: async () => {
      await responses({ model: 'oa/m1', input: 'hi', instructions: 'be brief' });
      assert.deepEqual(lastUp('/chat/completions').body.messages[0], { role: 'system', content: 'be brief' });
    },
  },
  {
    name: '/v1/responses → oa/tools: a function tool both ways',
    run: async () => {
      const r = await responses({
        model: 'oa/tools',
        input: 'hi',
        tools: [{ type: 'function', name: 'get_weather', parameters: { type: 'object' } }],
      });
      assert.equal(r.status, 200);
      assert.deepEqual(lastUp('/chat/completions').body.tools[0], {
        type: 'function',
        function: { name: 'get_weather', description: '', parameters: { type: 'object' } },
      });
      const call = r.json.output.find((o) => o.type === 'function_call');
      assert.ok(call, JSON.stringify(r.json.output));
      assert.deepEqual([call.call_id, call.name, call.arguments], ['call_1', 'get_weather', '{"city":"Paris"}']);
    },
  },
  {
    name: '/v1/responses → native Responses provider, whole: body unchanged but the model',
    run: async () => {
      const body = { model: 'rs/gpt-5-codex', input: 'hi', reasoning: { effort: 'low' } };
      const r = await responses(body);
      assert.equal(r.status, 200);
      assert.equal(r.json.model, 'rs/gpt-5-codex');
      assert.equal(r.json.output[0].content[0].text, 'native');
      const up = lastUp('/responses');
      assert.equal(up.headers.authorization, `Bearer ${UPSTREAM_SECRET.rs}`);
      assert.deepEqual(up.body, { ...body, model: 'gpt-5-codex' });
    },
  },
  {
    name: '/v1/responses → native Responses provider, stream: events pass with the requested model',
    run: async () => {
      const r = await responses({ model: 'rs/gpt-5-codex', input: 'hi', stream: true });
      assert.equal(r.status, 200);
      const ev = sseEvents(r.text);
      assert.deepEqual(
        ev.map((e) => e.event),
        ['response.created', 'response.output_text.delta', 'response.completed'],
      );
      assert.equal(ev[0].data.response.model, 'rs/gpt-5-codex');
      assert.equal(ev[2].data.response.model, 'rs/gpt-5-codex');
    },
  },
  {
    name: '/v1/responses → native Responses provider keeps previous_response_id',
    run: async () => {
      const r = await responses({ model: 'rs/gpt-5-codex', input: 'hi', previous_response_id: 'resp_0' });
      assert.equal(r.status, 200);
      assert.equal(lastUp('/responses').body.previous_response_id, 'resp_0');
    },
  },
  {
    name: '/v1/responses → Anthropic provider, translated',
    run: async () => {
      const r = await responses({ model: 'an/claude-x', input: 'hi' });
      assert.equal(r.status, 200);
      assert.deepEqual([r.json.object, r.json.model, r.json.output_text], ['response', 'an/claude-x', 'ok']);
    },
  },
  {
    name: 'Gemini generateContent → OpenAI provider',
    run: async () => {
      const r = await gemini('oa/m1', 'generateContent', gContents());
      assert.equal(r.status, 200);
      const c = r.json.candidates[0];
      assert.equal(c.content.parts[0].text, 'hi');
      assert.equal(c.finishReason, 'STOP');
      assert.deepEqual(r.json.usageMetadata, {
        promptTokenCount: 1000,
        candidatesTokenCount: 500,
        totalTokenCount: 1500,
      });
      assert.equal(r.json.modelVersion, 'oa/m1');
    },
  },
  {
    name: 'Gemini generateContent → Anthropic provider',
    run: async () => {
      const r = await gemini('an/claude-x', 'generateContent', gContents());
      assert.equal(r.status, 200);
      assert.equal(r.json.candidates[0].content.parts[0].text, 'ok');
    },
  },
  {
    name: 'Gemini systemInstruction and maxOutputTokens are translated',
    run: async () => {
      await gemini('oa/m1', 'generateContent', {
        ...gContents(),
        systemInstruction: { parts: [{ text: 'be brief' }] },
        generationConfig: { maxOutputTokens: 50 },
      });
      const b = lastUp('/chat/completions').body;
      assert.deepEqual(b.messages[0], { role: 'system', content: 'be brief' });
      assert.equal(b.max_tokens, 50);
    },
  },
  {
    name: 'Gemini streamGenerateContent without alt=sse is still an event stream',
    run: async () => {
      const r = await gemini('oa/m1', 'streamGenerateContent', gContents());
      assert.equal(r.status, 200);
      assert.equal(r.headers.get('content-type'), 'text/event-stream');
      const ev = sseEvents(r.text).map((e) => e.data);
      assert.deepEqual(
        ev.slice(0, 2).map((d) => d.candidates[0].content.parts[0].text),
        ['Hel', 'lo'],
      );
      assert.equal(ev.at(-1).candidates[0].finishReason, 'STOP');
      assert.equal(ev.at(-1).usageMetadata.promptTokenCount, 10);
    },
  },
  {
    name: 'Gemini countTokens is estimated without an upstream call',
    run: async () => {
      const before = h.upstream.requests.length;
      const r = await gemini('oa/m1', 'countTokens', gContents());
      assert.equal(r.status, 200);
      assert.ok(r.json.totalTokens > 0);
      assert.equal(h.upstream.requests.length, before);
    },
  },
  {
    name: 'GET /v1beta/models lists the models in the Gemini shape, no agents or collab entries',
    run: async () => {
      const r = await h.call('/v1beta/models', { method: 'GET', headers: gKey() });
      assert.equal(r.status, 200);
      const m = r.json.models.find((x) => x.name === 'models/oa/m1');
      assert.ok(m, JSON.stringify(r.json.models.map((x) => x.name)));
      assert.equal(m.displayName, 'oa/m1');
      assert.deepEqual(m.supportedGenerationMethods, ['generateContent', 'streamGenerateContent', 'countTokens']);
      assert.ok(r.json.models.every((x) => !/^models\/(agent|collab)\//.test(x.name)));
    },
  },
  {
    name: 'Gemini key in ?key= with no key header',
    run: async () => {
      const r = await h.call(`/v1beta/models/oa%2Fm1:generateContent?key=${h.key}`, { body: gContents() });
      assert.equal(r.status, 200);
    },
  },
  {
    name: 'embeddings → OpenAI provider',
    run: async () => {
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/m1', input: 'a' } });
      assert.equal(r.status, 200);
      assert.deepEqual(r.json.data[0].embedding, [0.1, 0.2]);
      assert.equal(r.json.usage.prompt_tokens, 3);
      assert.equal(lastUp('/embeddings').body.model, 'm1');
    },
  },
  {
    name: 'embeddings with an array input',
    run: async () => {
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'oa/m1', input: ['a', 'b'] } });
      assert.deepEqual(
        r.json.data.map((d) => d.index),
        [0, 1],
      );
      assert.deepEqual(lastUp('/embeddings').body.input, ['a', 'b']);
    },
  },
  {
    name: 'embeddings → private provider',
    run: async () => {
      const r = await h.call('/v1/embeddings', { key: h.key, body: { model: 'pv/m1', input: 'a' } });
      assert.equal(r.status, 200);
    },
  },
  {
    name: 'GET /v1/models: a list of models, each with object, an integer created and owned_by',
    run: async () => {
      const r = await h.call('/v1/models', { method: 'GET', key: h.key });
      assert.equal(r.status, 200);
      assert.equal(r.json.object, 'list');
      for (const m of r.json.data) {
        assert.equal(m.object, 'model', m.id);
        assert.ok(Number.isInteger(m.created), m.id);
        assert.equal(typeof m.owned_by, 'string', m.id);
      }
    },
  },
  {
    name: 'GET /v1/models: provider models and agent/auto',
    run: async () => {
      const r = await h.call('/v1/models', { method: 'GET', key: h.key });
      const by = Object.fromEntries(r.json.data.map((m) => [m.id, m]));
      assert.deepEqual([by['oa/m1'].owned_by, by['oa/m1'].created], ['oa', 1700000000]);
      assert.ok(by['an/claude-x'] && by['pv/m1'], Object.keys(by).join(','));
      assert.equal(by['agent/auto'].owned_by, 'zaafpulse');
    },
  },
  {
    name: 'GET /v1/models/{id}: encoded and plain slash both answer',
    run: async () => {
      const want = { id: 'oa/m1', object: 'model', owned_by: 'oa', created: 1700000000 };
      for (const p of ['/v1/models/oa%2Fm1', '/v1/models/oa/m1']) {
        const r = await h.call(p, { method: 'GET', key: h.key });
        assert.equal(r.status, 200, p);
        assert.deepEqual(r.json, want);
      }
    },
  },
  {
    name: 'GET /v1/models/{id} of an unknown model: 404 model_not_found',
    run: async () => {
      const r = await h.call('/v1/models/oa%2Fnope', { method: 'GET', key: h.key });
      assert.equal(r.status, 404);
      assert.equal(r.json.error.code, 'model_not_found');
      assert.equal(r.json.error.type, 'invalid_request_error');
    },
  },
  {
    name: 'GET /v1/models/{id} with broken URL encoding: 400',
    run: async () => {
      const r = await h.call('/v1/models/%E0%A4%A', { method: 'GET', key: h.key });
      assert.equal(r.status, 400);
      // With the Go gateway in front, Go's HTTP server refuses a request target with a broken escape itself, before
      // any handler, with its own plain-text 400 (P4a: accepted; only the body differs, for a request no client sends).
      if (['front', 'go'].includes(process.env.ZP_ENGINE) && r.text === '400 Bad Request') return;
      assert.equal(r.json.error.message, 'model id is not valid URL encoding');
    },
  },
  {
    name: 'OPTIONS preflight: 204 with CORS headers for every key header',
    run: async () => {
      const r = await h.call('/v1/chat/completions', {
        method: 'OPTIONS',
        // A page on this machine: with sign-in off, server.js refuses any other origin (403) before the gateway.
        headers: { origin: 'http://localhost:5173', 'access-control-request-method': 'POST' },
      });
      assert.equal(r.status, 204);
      assert.equal(r.headers.get('access-control-allow-origin'), '*');
      const allowed = (r.headers.get('access-control-allow-headers') || '').toLowerCase();
      for (const x of ['authorization', 'x-api-key', 'x-goog-api-key', 'anthropic-version'])
        assert.ok(allowed.includes(x), `${x} in ${allowed}`);
    },
  },
  {
    name: 'JSON answers carry CORS and no-store; an unknown route is 404',
    run: async () => {
      const r = await h.call('/v1/models', { method: 'GET', key: h.key });
      assert.equal(r.headers.get('access-control-allow-origin'), '*');
      assert.equal(r.headers.get('cache-control'), 'no-store');
      const n = await h.call('/v1/nope', { key: h.key, body: {} });
      assert.equal(n.status, 404);
      assert.equal(n.json.error.message, 'no gateway route POST /v1/nope');
    },
  },
];

for (const c of CASES) test(`formats: ${c.name}`, c.run);

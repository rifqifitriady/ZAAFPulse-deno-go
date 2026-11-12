// Fixtures for the Go port of lib/gateway/translate.js (gw/translate): every request, response and stream function,
// over the inputs of test/gateway-compat.test.js and test/router9.test.js plus edge cases.
//   node scripts/fixtures/gen-translate.js
//   deno run --allow-read --allow-write=gw/testdata scripts/fixtures/gen-translate.js
const { writeCases } = require('./lib');
const tr = require('../../lib/gateway/translate');

const cases = [];
const add = (name, fn, args, call) => cases.push({ name: `${fn}-${name}`, fn, args, call });
const tagged = (body) => ({ body, soft: body[tr.SOFT] ?? null, dropped: body[tr.DROPPED] ?? null });

/* ---------- requests and answers ---------- */
const a2o = (name, b) => add(name, 'anthropicToOpenAI', [b], ([x]) => tagged(tr.anthropicToOpenAI(x)));
const o2a = (name, j, model) => add(name, 'openAIToAnthropic', [j, model], ([x, m]) => tr.openAIToAnthropic(x, m));
const o2aReq = (name, b) => add(name, 'openAIToAnthropicRequest', [b], ([x]) => tr.openAIToAnthropicRequest(x));
const usage = (name, ...args) => add(name, 'anthropicUsageToOpenAI', args, (xs) => tr.anthropicUsageToOpenAI(...xs));
const aResp = (name, j, model) =>
  add(name, 'anthropicResponseToOpenAI', [j, model], ([x, m]) => tr.anthropicResponseToOpenAI(x, m));
const r2o = (name, b) =>
  add(name, 'responsesToOpenAI', [b], ([x]) => {
    const { body, custom } = tr.responsesToOpenAI(x);
    return { ...tagged(body), custom: [...custom] };
  });
const o2r = (name, j, model, custom) =>
  add(name, 'openAIToResponses', custom ? [j, model, custom] : [j, model], ([x, m, c]) =>
    tr.openAIToResponses(x, m, c ? new Set(c) : undefined),
  );
const g2o = (name, b, model, stream) =>
  add(name, 'geminiToOpenAI', [b, model, stream], ([x, m, s]) => tr.geminiToOpenAI(x, m, s));
const o2g = (name, j, model) => add(name, 'openAIToGemini', [j, model], ([x, m]) => tr.openAIToGemini(x, m));
const est = (name, body) =>
  add(name, 'estimateTokens', body === undefined ? [] : [body], ([x]) => tr.estimateTokens(x));

const oaReply = (
  content,
  extra = {},
  u = { prompt_tokens: 100, completion_tokens: 20, prompt_tokens_details: { cached_tokens: 40 } },
) => ({
  id: 'c1',
  choices: [
    {
      index: 0,
      message: { role: 'assistant', content, ...extra },
      finish_reason: extra.tool_calls ? 'tool_calls' : 'stop',
    },
  ],
  usage: u,
});

// router9: Anthropic ⇄ OpenAI, Responses and Gemini
const r9Anthropic = {
  model: 'claude-x',
  system: [{ type: 'text', text: 'be brief' }],
  max_tokens: 100,
  tools: [
    { name: 'Bash', description: 'run', input_schema: { type: 'object', properties: { command: { type: 'string' } } } },
  ],
  tool_choice: { type: 'any' },
  messages: [
    { role: 'user', content: 'list files' },
    {
      role: 'assistant',
      content: [
        { type: 'text', text: 'ok' },
        { type: 'tool_use', id: 'tu1', name: 'Bash', input: { command: 'ls' } },
      ],
    },
    {
      role: 'user',
      content: [
        { type: 'tool_result', tool_use_id: 'tu1', content: [{ type: 'text', text: 'a.js' }] },
        { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAA' } },
        { type: 'text', text: 'and this?' },
      ],
    },
  ],
};
a2o('router9', r9Anthropic);
o2aReq('router9-back', tagged(tr.anthropicToOpenAI(JSON.parse(JSON.stringify(r9Anthropic)))).body);
o2a(
  'router9-tool-call',
  oaReply(null, {
    tool_calls: [{ id: 'c9', type: 'function', function: { name: 'Bash', arguments: '{"command":"pwd"}' } }],
  }),
  'claude-x',
);
r2o('router9', {
  model: 'gpt-x',
  instructions: 'sys',
  input: [
    { role: 'user', content: [{ type: 'input_text', text: 'fix it' }] },
    { type: 'function_call', call_id: 'f1', name: 'shell', arguments: '{"cmd":["ls"]}' },
    { type: 'function_call_output', call_id: 'f1', output: 'a.js' },
    { type: 'custom_tool_call', call_id: 'p1', name: 'apply_patch', input: '*** Begin Patch' },
  ],
  tools: [
    { type: 'function', name: 'shell', parameters: { type: 'object' } },
    { type: 'custom', name: 'apply_patch', description: 'patch' },
  ],
});
o2r(
  'router9-custom',
  oaReply(null, {
    tool_calls: [
      { id: 'p2', type: 'function', function: { name: 'apply_patch', arguments: '{"input":"*** Begin Patch\\n"}' } },
    ],
  }),
  'gpt-x',
  ['apply_patch'],
);
g2o(
  'router9',
  {
    systemInstruction: { parts: [{ text: 'sys' }] },
    contents: [
      { role: 'user', parts: [{ text: 'hi' }] },
      { role: 'model', parts: [{ functionCall: { name: 'read', args: { p: 1 } } }] },
      { role: 'user', parts: [{ functionResponse: { name: 'read', response: { ok: true } } }] },
    ],
    tools: [
      {
        functionDeclarations: [{ name: 'read', parameters: { type: 'OBJECT', properties: { p: { type: 'NUMBER' } } } }],
      },
    ],
    generationConfig: { maxOutputTokens: 50 },
  },
  'gem',
  false,
);
o2g('router9', oaReply('yo'), 'gem');

// gateway-compat: derived reasoning, OpenAI → Anthropic options, thinking back, server tools
a2o('derived-effort', {
  model: 'x',
  max_tokens: 4000,
  thinking: { type: 'enabled', budget_tokens: 10000 },
  messages: [{ role: 'user', content: 'hi' }],
});
const tools = [{ type: 'function', function: { name: 'read', parameters: { type: 'object' } } }];
const m = [{ role: 'user', content: 'hi' }];
o2aReq('temperature-clamped', { model: 'c', messages: m, temperature: 1.5, top_p: 0.9 });
o2aReq('top-p', { model: 'c', messages: m, top_p: 0.9 });
o2aReq('parallel-false', { model: 'c', messages: m, tools, parallel_tool_calls: false });
o2aReq('required-parallel-false', {
  model: 'c',
  messages: m,
  tools,
  tool_choice: 'required',
  parallel_tool_calls: false,
});
o2aReq('thinking-high', { model: 'c', messages: m, reasoning_effort: 'high', temperature: 0.2 });
o2aReq('thinking-small', { model: 'c', messages: m, reasoning_effort: 'high', max_tokens: 4000 });
o2aReq('thinking-too-small', { model: 'c', messages: m, reasoning_effort: 'low', max_tokens: 1500 });
o2aReq('thinking-forced-tool', { model: 'c', messages: m, tools, tool_choice: 'required', reasoning_effort: 'low' });
const loop = [
  { role: 'user', content: 'read it' },
  {
    role: 'assistant',
    content: null,
    tool_calls: [{ id: 't1', type: 'function', function: { name: 'read', arguments: '{}' } }],
  },
  {
    role: 'tool',
    tool_call_id: 't1',
    content: [
      { type: 'text', text: 'see' },
      { type: 'image_url', image_url: { url: 'data:image/png;base64,QUJD' } },
    ],
  },
];
o2aReq('tool-loop', { model: 'c', messages: loop, tools, reasoning_effort: 'medium' });
const schema = { type: 'object', properties: { a: { type: 'string' } }, required: ['a'], additionalProperties: false };
o2aReq('json-schema', {
  model: 'c',
  messages: m,
  response_format: { type: 'json_schema', json_schema: { name: 'x', schema } },
});
o2aReq('json-object-refused', { model: 'c', messages: m, response_format: { type: 'json_object' } });
o2aReq('json-schema-missing', { model: 'c', messages: m, response_format: { type: 'json_schema', json_schema: {} } });
aResp(
  'thinking',
  {
    content: [
      { type: 'thinking', thinking: 'hmm', signature: 's' },
      { type: 'text', text: 'ok' },
    ],
    stop_reason: 'end_turn',
    usage: {},
  },
  'c',
);
a2o('server-tools', {
  model: 'x',
  max_tokens: 2000,
  thinking: { type: 'enabled', budget_tokens: 1024 },
  output_config: { format: { type: 'json_schema', schema: { type: 'object' } } },
  tools: [
    { name: 'Read', input_schema: { type: 'object' } },
    { type: 'bash_20250124', name: 'bash' },
    { type: 'text_editor_20250728', name: 'str_replace_based_edit_tool' },
    { type: 'web_search_20250305', name: 'web_search', max_uses: 3 },
  ],
  tool_choice: { type: 'auto', disable_parallel_tool_use: true },
  messages: [
    { role: 'user', content: 'look' },
    {
      role: 'assistant',
      content: [
        { type: 'server_tool_use', id: 'srv1', name: 'web_search', input: { query: 'q' } },
        { type: 'web_search_tool_result', tool_use_id: 'srv1', content: [] },
        { type: 'tool_use', id: 'tu1', name: 'Read', input: { f: 'a.png' } },
      ],
    },
    {
      role: 'user',
      content: [
        {
          type: 'tool_result',
          tool_use_id: 'tu1',
          content: [
            { type: 'text', text: 'the screenshot' },
            { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'QUJD' } },
          ],
        },
        { type: 'text', text: 'what is in it?' },
      ],
    },
  ],
});
a2o('server-tool-forced', {
  model: 'x',
  tools: [{ type: 'web_search_20250305', name: 'web_search' }],
  tool_choice: { type: 'tool', name: 'web_search' },
  messages: [{ role: 'user', content: 'q' }],
});
r2o('reasoning-schema-builtin', {
  model: 'x',
  input: 'hi',
  reasoning: { effort: 'high', summary: 'auto' },
  text: { format: { type: 'json_schema', name: 'answer', schema: { type: 'object' }, strict: true } },
  tools: [{ type: 'web_search' }, { type: 'function', name: 'f', parameters: { type: 'object' } }],
});
r2o('previous-response-id', { model: 'x', previous_response_id: 'resp_1', input: 'hi' });
a2o('dropped-reasoning', {
  max_tokens: 3000,
  thinking: { type: 'enabled', budget_tokens: 2000 },
  tools: [{ type: 'web_search_20250305', name: 'web_search' }],
  messages: [{ role: 'user', content: 'hi' }],
  model: 'oai/gpt-5',
});

// Edge cases: Anthropic → OpenAI
a2o('empty', {});
a2o('system-string-and-blocks', {
  model: 'x',
  system: ['plain', { type: 'text', text: 'two' }, { type: 'text' }],
  messages: [
    { role: 'user', content: [] },
    { role: 'assistant', content: '' },
    { role: 'assistant', content: [{ type: 'thinking', thinking: 't' }] },
    { role: 'assistant', content: [{ type: 'tool_use', id: 'x1', name: 'f' }] },
    {
      role: 'user',
      content: [
        { type: 'tool_result', tool_use_id: 'x1', is_error: true, content: 'boom' },
        { type: 'tool_result', tool_use_id: 'x2' },
        {
          type: 'tool_result',
          tool_use_id: 'x3',
          content: [{ type: 'image', source: { type: 'url', url: 'https://i/x.png' } }, { type: 'other' }],
        },
        { type: 'document', source: { type: 'text', data: 'doc text' } },
        { type: 'document', source: { type: 'base64', data: 'QUJD' } },
        { type: 'text', text: 'end' },
      ],
    },
  ],
  temperature: 0,
  top_p: 1,
  stop_sequences: ['\n\n', 'END'],
  stream: true,
});
a2o('effort-config', {
  model: 'x',
  messages: [{ role: 'user', content: 'q' }],
  thinking: { type: 'adaptive' },
  output_config: { effort: 'max' },
  tools: [{ type: 'custom', name: 'c1', description: 'custom' }, { name: 'plain' }],
  tool_choice: { type: 'tool', name: 'c1' },
});
a2o('effort-bad', { model: 'x', messages: [], output_config: { effort: 'ultra' }, tool_choice: { type: 'none' } });
a2o('effort-low-budget', { model: 'x', messages: [], thinking: { type: 'enabled', budget_tokens: 'abc' } });
a2o('effort-mid-budget', { model: 'x', messages: [], thinking: { type: 'enabled', budget_tokens: '8000' } });
a2o('tool-choice-none-with-tools', {
  model: 'x',
  messages: [],
  tools: [
    { name: 'f', input_schema: { type: 'object' } },
    { type: 'computer_20250124', name: 'computer' },
  ],
  tool_choice: { type: 'none' },
});
a2o('tool-choice-weird', {
  model: 'x',
  messages: [],
  tools: [{ name: 'f', input_schema: { type: 'object' } }],
  tool_choice: { type: 'whatever' },
});
a2o('unicode', {
  model: 'x',
  messages: [
    { role: 'user', content: 'héllo 😀   "quoted" \\ back' },
    {
      role: 'assistant',
      content: [
        { type: 'tool_use', id: 'u', name: 'f', input: { z: 1, a: [1, { y: 'é', b: null }], 2: 'n', 1: true } },
      ],
    },
  ],
});
a2o('missing-messages-type-error', {
  model: 'x',
  tools: [{ type: 'bash_20250124' }],
  messages: [{ role: 'user', content: [{ type: 'tool_result', content: [null] }] }],
});
o2a('text-and-usage', oaReply('hi', {}, { prompt_tokens: 10, completion_tokens: 5 }), 'm');
o2a('no-choices', {}, 'm');
o2a(
  'length',
  { choices: [{ message: { content: [{ type: 'text', text: 'a' }, 'b', { type: 'x' }] }, finish_reason: 'length' }] },
  'm',
);
o2a(
  'filter-and-bad-args',
  {
    choices: [
      {
        message: {
          content: '',
          tool_calls: [
            { function: { name: 'f', arguments: '{bad' } },
            { id: 'k', function: { name: 'g', arguments: { already: 'object' } } },
          ],
        },
        finish_reason: 'content_filter',
      },
    ],
    usage: { prompt_tokens: 5, prompt_tokens_details: { cached_tokens: 9 } },
  },
  undefined,
);
o2a(
  'unknown-finish-with-tools',
  {
    choices: [
      { message: { tool_calls: [{ id: 'a', function: { name: 'f', arguments: '' } }] }, finish_reason: 'weird' },
    ],
  },
  'm',
);
o2a('tool-call-without-function', { choices: [{ message: { tool_calls: [{ id: 'a' }] } }] }, 'm');

// Edge cases: OpenAI → Anthropic request
o2aReq('roles-merge', {
  model: 'c',
  messages: [
    { role: 'system', content: 'one' },
    { role: 'developer', content: [{ type: 'text', text: 'two' }] },
    { role: 'user', content: 'a' },
    {
      role: 'user',
      content: [
        { type: 'text', text: '' },
        { type: 'image_url', image_url: { url: 'https://x/y.png' } },
        { type: 'image_url' },
      ],
    },
    { role: 'assistant', content: 'b', tool_calls: [{ id: 't1', function: { name: 'f', arguments: '{"q":1}' } }] },
    { role: 'tool', tool_call_id: 't1', content: 'r1' },
    { role: 'tool', tool_call_id: 't2', content: [{ type: 'text', text: 'r2' }] },
    { role: 'assistant', content: '' },
    { role: 'function', content: 'odd role' },
  ],
  stop: 'END',
  max_completion_tokens: 50,
  stream: true,
  tools: [...tools, { type: 'retrieval' }, { type: 'function', function: { name: 'g', description: 'd' } }],
  tool_choice: { type: 'function', function: { name: 'g' } },
});
o2aReq('data-urls', {
  model: 'c',
  messages: [
    {
      role: 'user',
      content: [
        { type: 'image_url', image_url: { url: 'data:image/png;base64,AA\nBB' } },
        { type: 'image_url', image_url: { url: 'data:a\nb;base64,QQ' } },
        { type: 'image_url', image_url: { url: 'data:;base64,x' } },
        { type: 'image_url', image_url: { url: 'data:a;b;base64,x' } },
        { type: 'image_url', image_url: 'not an object' },
      ],
    },
  ],
});
o2aReq('choice-none-parallel', { model: 'c', messages: m, tools, tool_choice: 'none', parallel_tool_calls: false });
o2aReq('choice-auto', { model: 'c', messages: m, tools, tool_choice: 'auto', reasoning_effort: 'xhigh' });
o2aReq('choice-unknown', { model: 'c', messages: m, tools, tool_choice: 'other', reasoning_effort: 'minimal' });
o2aReq('temperature-string', { model: 'c', messages: m, temperature: 'warm', top_p: 0.5 });
o2aReq('temperature-negative', { model: 'c', messages: m, temperature: -3 });
o2aReq('stop-array', { model: 'c', messages: m, stop: ['a', 'b'], max_tokens: 10 });
o2aReq('effort-medium-big', { model: 'c', messages: m, reasoning_effort: 'medium', max_tokens: 100000 });
o2aReq('empty', {});
o2aReq('tool-loop-single', {
  model: 'c',
  messages: [{ role: 'tool', tool_call_id: 'x', content: 'r' }],
  reasoning_effort: 'low',
});
o2aReq('null-tool-call-function', { model: 'c', messages: [{ role: 'assistant', tool_calls: [{ id: 'a' }] }] });
usage('empty');
usage('full', { input_tokens: 10, output_tokens: 3, cache_read_input_tokens: 4, cache_creation_input_tokens: 2 });
usage('null', null);
aResp(
  'tool-use',
  {
    id: 'msg_1',
    content: [
      { type: 'text', text: 'a' },
      { type: 'tool_use', id: 'tu', name: 'f', input: { b: 2, a: 1 } },
      { type: 'tool_use', id: 'tv', name: 'g' },
      { type: 'thinking' },
    ],
    stop_reason: 'tool_use',
    usage: { input_tokens: 7, output_tokens: 2, cache_creation_input_tokens: 1 },
  },
  'm',
);
aResp('empty', {}, 'm');
aResp('stops', { content: [], stop_reason: 'pause_turn' });
aResp('refusal', { content: [{ type: 'text', text: '' }], stop_reason: 'refusal', usage: null });

// Edge cases: Responses → OpenAI
r2o('assistant-merge', {
  model: 'r',
  input: [
    { role: 'system', content: 's' },
    {
      role: 'developer',
      content: [
        { type: 'input_text', text: 'd' },
        { type: 'input_image', image_url: 'https://x' },
      ],
    },
    {
      role: 'user',
      content: [
        { type: 'input_text', text: 'look' },
        { type: 'input_image', image_url: 'data:x' },
        { type: 'input_image' },
      ],
    },
    { role: 'user', content: [{ type: 'input_image', image_url: 'u' }] },
    {
      type: 'message',
      role: 'assistant',
      content: [{ type: 'output_text', text: 'a1' }, { type: 'refusal', refusal: 'no' }, { text: 'raw' }],
    },
    { role: 'assistant', content: 'a2' },
    { type: 'function_call', call_id: 'c1', name: 'f' },
    { type: 'reasoning', summary: [] },
    { type: 'custom_tool_call_output', call_id: 'c1', output: [{ type: 'output_text', text: 'x' }, { text: 'y' }, {}] },
    { type: 'function_call_output', call_id: 'c2', output: { ok: true } },
    { type: 'function_call_output', call_id: 'c3' },
    { type: 'custom_tool_call', call_id: 'c4', name: 'patch' },
    { role: 'user', content: null },
  ],
  max_output_tokens: 99,
  temperature: 0.4,
  top_p: 0.3,
  stream: true,
  reasoning: { effort: 'nope' },
  text: { format: { type: 'json_object' } },
  tools: [
    { type: 'custom', name: 'patch' },
    { type: 'function', name: 'f', description: 'fd' },
    { type: 'file_search' },
  ],
  tool_choice: 'required',
  parallel_tool_calls: false,
});
r2o('tool-choice-named', {
  model: 'r',
  input: [],
  tools: [{ type: 'function', name: 'f' }],
  tool_choice: { type: 'function', name: 'f' },
  text: { format: { type: 'json_schema', description: 'desc', strict: false } },
});
r2o('tool-choice-builtin', { model: 'r', input: 'x', tool_choice: { type: 'web_search' } });
r2o('tool-choice-object-auto', {
  model: 'r',
  input: 'x',
  tools: [{ type: 'function', name: 'f' }],
  tool_choice: { type: 'auto' },
});
r2o('item-reference', { model: 'r', input: [{ type: 'item_reference', id: 'i1' }] });
r2o('empty', {});
o2r('text', oaReply('hello'), 'r');
o2r(
  'length',
  {
    choices: [{ message: { content: 'cut' }, finish_reason: 'length' }],
    usage: { prompt_tokens: 3, completion_tokens: 9, completion_tokens_details: { reasoning_tokens: 4 } },
  },
  'r',
);
o2r(
  'tools',
  {
    choices: [
      {
        message: {
          content: null,
          tool_calls: [
            { function: { name: 'f' } },
            { id: 'x', function: { name: 'p', arguments: '{"input":5}' } },
            { id: 'y', function: { name: 'p', arguments: 'not json' } },
            { id: 'z', function: { name: 'p', arguments: '{"other":1}' } },
          ],
        },
      },
    ],
  },
  'r',
  ['p'],
);
o2r('empty', {}, 'r');

// Edge cases: Gemini
g2o(
  'snake-case',
  {
    system_instruction: { parts: [{ text: 'a' }, {}, { text: 'b' }] },
    contents: [
      { role: 'user', parts: [{ text: 'hi' }, { inline_data: { mime_type: 'image/png', data: 'QQ==' } }] },
      {
        role: 'model',
        parts: [
          { text: 'thinking', thought: true },
          { text: 'answer' },
          { functionCall: { name: 'f', args: { a: 1 } } },
          { functionCall: { id: 'own', name: 'f' } },
        ],
      },
      {
        role: 'user',
        parts: [
          { functionResponse: { name: 'f', response: { r: 1 } } },
          { functionResponse: { name: 'f' } },
          { functionResponse: { name: 'g', id: 'gid' } },
          { functionResponse: { name: 'h' } },
        ],
      },
      { role: 'model', parts: [] },
      { parts: [{ inlineData: { mimeType: 'image/jpeg', data: 'AA' } }] },
    ],
    tools: [
      {
        function_declarations: [
          { name: 'f', parametersJsonSchema: { type: 'OBJECT', items: [{ type: 'STRING' }], nested: { type: 5 } } },
        ],
      },
      {},
      { functionDeclarations: [{ name: 'g' }] },
    ],
    generation_config: { temperature: 0, topP: 0.5, stopSequences: ['x'] },
  },
  'gm',
  true,
);
g2o('empty', {}, 'gm');
o2g(
  'tools',
  {
    choices: [
      {
        message: { content: 'x', tool_calls: [{ function: { name: 'f', arguments: '{"a":[1]}' } }] },
        finish_reason: 'tool_calls',
      },
    ],
    usage: { prompt_tokens: 2, completion_tokens: 3, prompt_tokens_details: { cached_tokens: 1 } },
  },
  'g',
);
o2g('empty', {}, 'g');
o2g('filter', { choices: [{ message: {}, finish_reason: 'content_filter' }] }, 'g');

est('none');
est('null', null);
est('ascii', { model: 'm', messages: [{ role: 'user', content: 'hello world' }] });
est('unicode', { s: 'é😀 \n"\\', n: 1e21, f: 0.1, u: [null, true] });
est('string', 'abc');

/* ---------- streams ---------- */
const parallel = [
  {
    choices: [
      { index: 0, delta: { tool_calls: [{ index: 0, id: 'call_a', function: { name: 'f', arguments: '' } }] } },
    ],
  },
  { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: '{"x":1}' } }] } }] },
  {
    choices: [
      { index: 0, delta: { tool_calls: [{ index: 0, id: 'call_b', function: { name: 'g', arguments: '' } }] } },
    ],
  },
  { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: '{"y":2}' } }] } }] },
  { choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
];
const textChunks = [
  { id: 'x', choices: [{ index: 0, delta: { role: 'assistant', content: '' } }] },
  { choices: [{ index: 0, delta: { content: 'Hel' } }] },
  { choices: [{ index: 0, delta: { content: 'lo é😀' } }] },
  { choices: [{ index: 0, delta: { tool_calls: [{ function: { name: 'f', arguments: '{"a"' } }] } }] },
  { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: 'late', function: { arguments: ':1}' } }] } }] },
  { choices: [{ index: 0, delta: { content: 'after' } }] },
  { choices: [{ index: 0, delta: {}, finish_reason: 'length' }] },
  { choices: [], usage: { prompt_tokens: 12, completion_tokens: 7, prompt_tokens_details: { cached_tokens: 2 } } },
];
const ops = (chunks, { open = false, tail = [] } = {}) => [
  ...(open ? [['open']] : []),
  ...chunks.map((c) => ['push', c]),
  ['finish'],
  ...tail,
];
const runOps = (s, list) => list.map(([op, arg]) => s[op](arg));
const aStream = (name, model, list) =>
  add(name, 'anthropicStream', [model, list], ([mo, l]) => runOps(tr.anthropicStream(mo), l));
const rStream = (name, model, custom, list) =>
  add(name, 'responsesStream', [model, custom, list], ([mo, c, l]) =>
    runOps(tr.responsesStream(mo, c ? new Set(c) : undefined), l),
  );
const gStream = (name, model, list) =>
  add(name, 'geminiStream', [model, list], ([mo, l]) => runOps(tr.geminiStream(mo), l));

aStream('parallel', 'm', ops(parallel));
aStream('text', 'm', ops(textChunks, { open: true, tail: [['ping'], ['error', 'upstream "broke"']] }));
aStream('empty', 'm', [['finish']]);
aStream('no-model', null, [['ping'], ['push', { choices: [{ delta: { content: 'x' } }] }], ['finish']]);
aStream(
  'tool-then-text',
  'm',
  ops([
    { choices: [{ delta: { tool_calls: [{ index: 1, function: { name: 'f' } }] } }] },
    { choices: [{ delta: { content: 'x', tool_calls: [{ index: 1, id: 'i2', function: { arguments: '{}' } }] } }] },
    {
      choices: [
        {
          delta: { tool_calls: [{ index: 0, id: 'i3', function: { name: 'g', arguments: '[]' } }] },
          finish_reason: 'stop',
        },
      ],
    },
    { choices: [{ delta: {} }] },
    { usage: { prompt_tokens: 1 } },
  ]),
);
rStream('parallel', 'm', undefined, ops(parallel));
rStream('late-id', 'm', undefined, [
  [
    'push',
    { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { name: 'f', arguments: '{"a"' } }] } }] },
  ],
  [
    'push',
    { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: 'call_z', function: { arguments: ':1}' } }] } }] },
  ],
  ['finish'],
]);
rStream('text', 'm', undefined, ops(textChunks, { open: true, tail: [['error', 'bad']] }));
rStream(
  'custom',
  'm',
  ['patch'],
  ops([
    { choices: [{ delta: { content: 'thinking out loud' } }] },
    {
      choices: [
        {
          delta: {
            tool_calls: [{ index: 0, id: 'p1', function: { name: 'patch', arguments: '{"input":"*** Begin' } }],
          },
        },
      ],
    },
    { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: ' Patch"}' } }] } }] },
    { choices: [{ delta: { tool_calls: [{ index: 1, function: { arguments: '{}' } }] } }] },
    { choices: [{ delta: { tool_calls: [{ index: 1, function: { name: 'named-late' } }] } }] },
    {
      choices: [
        { delta: { tool_calls: [{ index: 2, id: 'p3', function: { name: 'patch', arguments: 'raw text' } }] } },
      ],
    },
    { choices: [{ delta: {}, finish_reason: 'tool_calls' }], usage: { prompt_tokens: 3, completion_tokens: 4 } },
  ]),
);
rStream('empty', 'm', undefined, [['finish']]);
rStream('error-first', 'm', undefined, [['error', 'x'], ['open'], ['finish']]);
gStream('parallel', 'm', ops(parallel));
gStream('text', 'm', ops(textChunks, { tail: [['error', 'oops']] }));
gStream('empty', 'm', [['finish']]);
gStream(
  'bad-args',
  'm',
  ops([
    {
      choices: [
        {
          delta: { tool_calls: [{ id: 'a', function: { name: 'f', arguments: '{nope' } }] },
          finish_reason: 'content_filter',
        },
      ],
    },
  ]),
);

const aEvents = (name, model, events) =>
  add(name, 'anthropicEventsToOpenAI', [model, events], ([mo, evs]) => {
    const f = tr.anthropicEventsToOpenAI(mo);
    return evs.map((e) => f(e));
  });
aEvents('full', 'm', [
  { type: 'message_start', message: { id: 'msg', usage: { input_tokens: 9, cache_read_input_tokens: 2 } } },
  { type: 'content_block_start', index: 0, content_block: { type: 'thinking', thinking: '' } },
  { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'hmm' } },
  { type: 'content_block_delta', index: 0, delta: { type: 'signature_delta', signature: 's' } },
  { type: 'content_block_stop', index: 0 },
  { type: 'content_block_start', index: 1, content_block: { type: 'text', text: '' } },
  { type: 'content_block_delta', index: 1, delta: { type: 'text_delta', text: 'Hi é' } },
  { type: 'content_block_start', index: 2, content_block: { type: 'tool_use', id: 'tu1', name: 'f', input: {} } },
  { type: 'content_block_delta', index: 2, delta: { type: 'input_json_delta', partial_json: '{"a":' } },
  { type: 'content_block_delta', index: 2, delta: { type: 'input_json_delta', partial_json: '1}' } },
  { type: 'content_block_delta', index: 7, delta: { type: 'input_json_delta', partial_json: 'x' } },
  { type: 'content_block_start', index: 3, content_block: { type: 'tool_use', id: 'tu2', name: 'g', input: {} } },
  { type: 'ping' },
  { type: 'message_delta', delta: { stop_reason: 'tool_use' }, usage: { output_tokens: 15 } },
  { type: 'message_stop' },
]);
aEvents('bare', 'm', [
  { type: 'message_start' },
  { type: 'content_block_delta', index: 0 },
  { type: 'message_delta' },
  { type: 'message_stop' },
  { type: 'error', error: { type: 'overloaded_error', message: 'busy' } },
  { type: 'error' },
  { type: 'unknown' },
]);
aEvents('stop-unknown', undefined, [
  { type: 'message_delta', delta: { stop_reason: 'refusal' }, usage: null },
  { type: 'message_stop' },
]);

const whole = (name, j) => add(name, 'wholeToChunks', [j], ([x]) => tr.wholeToChunks(x));
whole('text', oaReply('hi'));
whole(
  'tools',
  oaReply(null, { tool_calls: [{ id: 'a', type: 'function', function: { name: 'f', arguments: '{}' } }, { id: 'b' }] }),
);
whole('empty', {});
whole('content-parts', {
  choices: [{ message: { content: [{ text: 'a' }, 'b'] }, finish_reason: 'length' }],
  usage: null,
});

const sse = (name, chunks) =>
  add(name, 'readSse', [chunks], async ([list]) => {
    const enc = new TextEncoder();
    const parts = list.map((c) => (typeof c === 'string' ? enc.encode(c) : Uint8Array.from(c)));
    const body = new ReadableStream({
      start(ctl) {
        for (const p of parts) ctl.enqueue(p);
        ctl.close();
      },
    });
    const out = [];
    await tr.readSse(body, (ev, data) => {
      out.push([ev, data]);
    });
    return out;
  });
sse('basic', ['event: a\ndata: {"x":1}\n\n', 'data: plain\n\n']);
sse('crlf-split', ['event: a\r\nda', 'ta: 1\r\n\r', '\ndata:2\ndata:  3\n\n: comment\n\nevent:  b \n\n', 'data: tail']);
sse('mixed-separators', ['data: a\n\r\ndata: b\r\n\ndata: c\n\n\n\ndata: d\n\n']);
sse('utf8-split', [
  [100, 97, 116, 97, 58, 32, 0xc3],
  [0xa9, 0xf0, 0x9f],
  [0x98, 0x80, 10, 10],
]);
sse('empty', []);
sse('done-marker', ['data: [DONE]\n\n', 'event: ping\n\n', 'id: 4\ndata: x\nretry: 1\n\n']);

writeCases('translate', cases);

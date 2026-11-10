// @ts-check
// The shapes that cross the future control-plane ⇄ gateway IPC (P3), as JSDoc typedefs only: there is no runtime code
// here. Producers annotate their return values with them, `deno task typecheck` checks both sides, and P3 turns them
// into the versioned IPC schema. Money in the IPC is `Money` (integer nano-USD, lib/money.js); the stored files keep
// their float USD fields (D3), converted with toNano/fromNano at the boundary.

/**
 * A whole number of nano-USD (1e-9 USD): a safe integer, so at most about US$9 million. lib/money.js converts.
 * @typedef {number} Money
 */

/**
 * The guardrails outcome of one call, as the usage record carries it (lib/gateway/guardrails.js). Hits name the check
 * and a label with a count, never the matched text.
 * @typedef {object} GuardrailHit
 * @property {string} check 'secrets', 'pii' or 'injection'
 * @property {string} label
 * @property {number} count
 */
/**
 * @typedef {object} GuardrailOutcome
 * @property {string} action 'redacted', 'logged' or 'blocked'
 * @property {GuardrailHit[]} hits
 * @property {boolean} [error] a check in log mode threw and the request went on
 */

/**
 * One gateway call as lib/gateway/usage.js records it (a line of gateway-usage.jsonl; GET /api/gateway/logs shows it
 * without traceId). Tokens are counts; `cost` is float USD rounded by the pricer, `toNano(cost)` in the IPC.
 * @typedef {object} UsageRecord
 * @property {number} ts epoch ms
 * @property {string} keyId a key id, or 'dashboard' for the dashboard's own calls
 * @property {string} provider provider id (for an MCP call: the integration prefix)
 * @property {string} model the model name the caller asked for (provider/model, router/x or agent model)
 * @property {string} [requested] the client's own name when a model map changed it
 * @property {string} [routedTo] the route target that answered, when not `model`
 * @property {'chat'|'agent'|'embeddings'|'mcp'} [kind]
 * @property {'openai'|'anthropic'|'responses'|'gemini'} [format]
 * @property {boolean} [stream]
 * @property {number} status HTTP status of the attempt (599: no answer)
 * @property {number} ms
 * @property {number|null} [ttft] ms to the first token of a stream
 * @property {number} [input]
 * @property {number} [output]
 * @property {number} [cached]
 * @property {number} cost US$ (float)
 * @property {string} [error]
 * @property {GuardrailOutcome} [guard]
 * @property {'claude-code'|'codex'} [client]
 * @property {true} [local] a loopback caller with no forwarding header
 * @property {number} [savedTokens] tokens the token saver cut
 * @property {string} [agent]
 * @property {string} [sessionId]
 * @property {string} [runId]
 * @property {string} [traceId]
 */

/**
 * What the budget guard is asked before a call goes to a route target (lib/cost/guard.js check()).
 * @typedef {object} BudgetCheckInput
 * @property {string|null} [keyId]
 * @property {string} [model]
 * @property {string} [provider]
 * @property {string} [agent]
 * @property {string} [runId]
 */
/**
 * A refusal from the guard: the gateway answers 402 insufficient_quota with `message`.
 * @typedef {object} BudgetRefusal
 * @property {string} budget budget id
 * @property {string} message
 */
/**
 * One budget as the guard enforces it (lib/cost/budgets.js evaluateBudget, forecast off): the IPC pushes a list of
 * these as the budget snapshot. Amounts in the budget's currency (`amount`, `spent`) and in USD.
 * @typedef {object} BudgetView
 * @property {string} id
 * @property {string} name
 * @property {'global'|{ dim: string, value: string }} scope
 * @property {'day'|'week'|'month'} period
 * @property {string} currency
 * @property {'list'|'billed'} measure
 * @property {'none'|'block'|'pause'} enforce
 * @property {number} amount
 * @property {string} from first day of the period (YYYY-MM-DD)
 * @property {string} to last day of the period
 * @property {string} state 'ok', 'warn_<threshold>' (e.g. warn_80), 'exceeded' or 'no_data'
 * @property {number|null} spentUSD
 * @property {number|null} amountUSD
 * @property {string|null} reason why the state is no_data
 */
/**
 * @typedef {object} BudgetSnapshot
 * @property {number} at epoch ms the snapshot was taken (the gateway refuses metered calls when it is older than 5 s)
 * @property {BudgetView[]} budgets
 */

/**
 * One governance audit entry as the gateway hands it to the single audit writer (server.js govRecord, lib/governance/
 * audit.js record). The writer adds the actor from the request context and the chain fields (seq, ts, prev, mac).
 * @typedef {object} AuditTarget
 * @property {string} type
 * @property {string} [id]
 * @property {string} [name]
 */
/**
 * @typedef {object} AuditEvent
 * @property {string} action e.g. key.create, key.update, run.approve, session.delete, knowledge.ingest
 * @property {AuditTarget} [target]
 * @property {'ok'|'refused'|'error'} [outcome] 'ok' when left out
 * @property {number} [status]
 * @property {Array<{ path: string, from?: unknown, to?: unknown }>} [changes] lib/governance/diff.js
 * @property {{ via: string, keyId?: string, user?: string, rule?: string }} [actor]
 */

/**
 * The gateway section of config.json as the gateway engine needs it, without secrets: a provider's credential is a
 * reference here and the secret itself is handed over separately (program plan §3, rule 3).
 * @typedef {object} GatewayProviderConfig
 * @property {string} id
 * @property {string} preset
 * @property {string} baseUrl
 * @property {string[]} [models]
 * @property {Record<string, string>} [aliases]
 * @property {boolean} [enabled]
 * @property {'private'|'external'} [tier] left out: automatic (from the preset)
 * @property {boolean} [approved]
 * @property {{ type: string, tokenUrl?: string, clientId?: string, scope?: string, grant?: 'client_credentials'|'refresh_token' }} [auth] OAuth2 or Google service-account settings; the client secret is a stored credential, never here
 * @property {'openai'|'anthropic'} [format]
 * @property {{ env: string }|{ keychain: string }} [apiKey] a reference, never a value
 * @property {boolean} [responses]
 * @property {boolean} [reasoning]
 * @property {'max_tokens'|'max_completion_tokens'} [tokenParam]
 */
/**
 * @typedef {object} GatewayRouteConfig
 * @property {string} name router/<name>
 * @property {string[]} targets
 * @property {'fallback'|'round-robin'} [strategy]
 * @property {string} [description]
 */
/**
 * @typedef {object} GuardrailsPolicy
 * @property {'external'|'all'} scope
 * @property {'off'|'log'|'redact'|'block'} secrets
 * @property {'off'|'log'|'redact'|'block'} pii
 * @property {'off'|'log'|'block'} injection
 */
/**
 * A key as the gateway enforces it (lib/gateway/keys.js pub(): no hash, no secret).
 * @typedef {object} GatewayKeyConfig
 * @property {string} id
 * @property {string} name
 * @property {string[]} scopes
 * @property {string[]} models
 * @property {string[]} agents
 * @property {string[]} knowledge
 * @property {number|null} budgetUSD
 * @property {number} rpm
 * @property {boolean} privateOnly
 * @property {number|null} expiresAt
 * @property {number|null} revokedAt
 * @property {Partial<GuardrailsPolicy>} guardrails
 * @property {string} prefix the first characters of the secret, for the list
 * @property {boolean} approveOwnRuns
 * @property {number} createdAt
 * @property {number|null} lastUsedAt
 */
/**
 * @typedef {object} GatewayConfigSection
 * @property {GatewayProviderConfig[]} providers
 * @property {GatewayRouteConfig[]} routes
 * @property {GatewayKeyConfig[]} keys
 * @property {GuardrailsPolicy} guardrails
 * @property {{ model: string, plannerModel: string, codeModel: string }} defaults
 * @property {boolean} tokenSaver
 * @property {Array<{ match: string, to: string }>} modelMap
 */

module.exports = {};

<div align="center">

**English** • [Русский](README.md)

</div>

# claude-code-gateway

<p align="center">
  <a href="https://github.com/Mukller">
    <img src="https://img.shields.io/badge/Anton%20Petnitsky-Developer-0d1117?style=for-the-badge&logo=github&logoColor=white&labelColor=0d1117&color=58a6ff" alt="Anton Petnitsky" />
  </a>
</p>

A lightweight Go gateway for **Claude Code**: a single Anthropic Messages API-style entry point with
multi-provider support, key rotation, cross-provider fallback, logging, and cost tracking.
The idea and feature set are inspired by [OmniRoute](https://github.com/diegosouzapw/OmniRoute); the implementation is my own,
stdlib + yaml only.

## Features

- **Anthropic-compatible API**: `/v1/messages` (streaming SSE + non-streaming), `/v1/messages/count_tokens`, `/v1/models`
- **Provider types**:
  - `anthropic` — direct api.anthropic.com (passthrough)
  - `anthropic-compat` — any Anthropic-compatible proxy (custom base_url, bearer/x-api-key)
  - `openai` — OpenAI-compatible backends (9router, OpenRouter, DeepSeek, GLM, Kimi, Ollama...) with full
    protocol translation both ways, including streaming SSE; `send_stream_options` option for exact usage
  - `bedrock` — AWS Bedrock (InvokeModel / invoke-with-response-stream): SigV4 signing from stdlib,
    binary event-stream decoding, key format `AKID:SECRET[:SESSION_TOKEN]`
  - `vertex` — Google Vertex AI (`rawPredict`/`streamRawPredict`, native Anthropic format):
    Express API-key (`?key=`), bearer token, or **service account** (`auth_style: sa` +
    `service_account_json`: JSON key or path to file — JWT signed locally,
    OAuth token cached and refreshed automatically)
- **Web dashboard** `/admin/dashboard`: today's cards, per-provider/per-model breakdowns,
  recent request feed with errors, auto-refresh
- **Key rotation**: multiple keys per provider, round-robin, cooldown on 401/403/429/5xx
  with exponential backoff + upstream `Retry-After` respected
- **Fallback chains**: model → list of targets; on key/provider failure the request moves on
- **Prefix routing**: rules like `anthropic/* → provider anthropic`
- **Model discovery**: model list is pulled from upstream (`/v1/models`) automatically
- **claude/ aliases**: duplicates models under `claude/<id>` so they appear in Claude Code's native model picker
  (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`)
- **Logs and cost**: every record — tokens, latency, status, USD cost estimate; JSONL file + aggregates
- **Response cache (opt-in)**: identical non-streaming requests served from an LRU+TTL cache
  (`cache.enabled: true`) — for replays and evaluations; such records are marked `"cached":true` in logs
- **Balancing strategies** (LiteLLM): `balance_strategy: weighted | least_busy | latency`
  in rules — weighted-random by weights, fewest active requests, or provider's rolling
  EMA latency
- **Auto price sync** (new-api): `pricing_sync` pulls prices for all models from OpenRouter
  every interval; explicit rules from `pricing:` always take priority
- **Guardrails+** (Portkey): PII presets (`pii_presets: [email, phone, card]` — masked
  in outgoing prompts), prompt-injection detection (`injection_detection: block|flag|off`),
  optional buffered stream scanning (`scan_streams: true`)
- **Postgres persistence** (LiteLLM): `state.postgres_url` — budgets and limits survive
  restarts and stay consistent across replicas (takes priority over Redis)
- **Distributed state** (one-api/LiteLLM multi-replica style): `state.redis_url` —
  RPM/TPM counters, budgets, and response cache live in Redis (own RESP2 client on stdlib,
  zero dependencies); without Redis everything works locally in-memory
- **config.yaml editor**: view/edit right from the dashboard with pre-apply validation
  (`GET/POST /admin/config/yaml`) and rollback via `POST /admin/config/rollback` (+`.bak`)
- **Semantic cache** (like Bifrost): optional via an embeddings endpoint — similar
  prompts get cached responses (cosine similarity, configurable threshold);
  hits are marked `[sem]` in the dashboard
- **Guardrails** (Portkey-style): request/response block patterns (regex), input token
  limit, tool deny list — violators get `400 blocked by guardrail`
- **Runtime client management** (one-api style): edit a client's budget via API/dashboard
  without restart (`POST /admin/tokens/update`), config snapshot at `/admin/config`
- **MCP server** (like OmniRoute): control the gateway from Claude — stats, logs, models,
  budgets, hot-reload, and cost estimation. HTTP transport `/mcp` or stdio (`-mcp`)
- **Request transformers** (like CCR transformers): `max_tokens_cap`, `set:key=value`,
  `reasoning_effort`, `drop_keys`, `system_prefix` — at the provider level
- **Combo models**: one alias → a chain of different models with fallback
  (`combo/fast` → glm-flash, then gpt-oss)
- **Scenario routing** (idea from claude-code-router): separate chains for
  long context (`long_context.threshold_tokens`), image requests (`image`),
  and thinking requests (`thinking`) — e.g. heavy context goes to a larger-window model
- **Client budgets and limits** (idea from LiteLLM virtual keys / uni-api): named tokens
  with a USD-per-period limit, a **model allowlist** (`allowed_models`) and a **TPM ceiling**
  (tokens estimated before sending); when exhausted — `429`, foreign model — `403`;
  `/admin/tokens` shows spend and reset date
- **Weights and load balancing** (one-api): `weight` per provider + `load_balance: true` in a rule —
  weighted distribution instead of strict failover order
- **Circuit breaker** (one-api/gpt-load): 5 consecutive errors — provider skipped for 2 minutes,
  keys keep rotating independently
- **Health probes**: periodic polling of upstream `/v1/models` (`probe_interval`),
  status/latency visible in `/admin/keys`
- **TTFT** (like Helicone): time-to-first-byte of each stream in every log entry
- **CSV export** (`/admin/export.csv`) and per-key pool statistics (`/admin/keys`)
- **Rate limit** (per client token), admin API, `/metrics` in Prometheus format,
  healthcheck (`/healthz` + `-healthcheck` flag for docker/compose)
- **Webhooks**: push `usage`/`error` events to your URL with HMAC signature (`X-CCG-Signature`)
- **Launcher**: `gateway -launch -- "your claude flags"` — starts the gateway and launches Claude Code
  with env already configured

## Quick start

```bash
cp .env.example .env        # put in NINE_ROUTER_KEY and your tokens
cp config.example.yaml config.yaml   # adjust providers if needed

docker compose up -d --build
# or locally:
go run ./cmd/gateway -config config.yaml
```

## Connecting Claude Code

Point Claude Code at the gateway via environment variables (**without** `/v1` in the base URL):

```bash
export ANTHROPIC_BASE_URL=http://localhost:8090
export ANTHROPIC_AUTH_TOKEN=ccg-local-dev-token          # GATEWAY_TOKEN from .env
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1      # models from /v1/models in the /model picker
claude
```

Models without the `claude*` prefix don't show up in Claude Code's native picker, but are available via
`claude/<id>` aliases (enabled by default, see `routing.alias_claude_prefix`) or via
`ANTHROPIC_MODEL=<id>`.

## Configuration

All string values support `${VAR}` and `${VAR:-default}` (substituted from the environment).

```yaml
server:
  listen: ":8090"

auth:
  tokens: ["${GATEWAY_TOKEN}"]     # how clients authenticate to the gateway
  admin_token: "${GATEWAY_ADMIN_TOKEN}"
  allow_anon: false                # true = allow everyone (local testing only)

rate_limit_rpm: 0                  # 0 = disabled

providers:
  - name: nine-router              # any name, referenced by chains/rules
    type: openai                   # anthropic | anthropic-compat | openai
    base_url: "https://9router.kitory.lol/v1"
    keys: ["${NINE_ROUTER_KEY}"]   # as many keys as you like — rotation kicks in automatically
    discover_models: true          # pull the model catalog from upstream
    refresh_interval: 15m
    timeout: 300s

routing:
  alias_claude_prefix: true
  default_chain: [nine-router]     # where unmatched models go
  rules:
    - prefix: "anthropic/"         # model "anthropic/claude-*" -> anthropic provider
      strip_prefix: true
      chain: [anthropic]

pricing:                           # USD per 1M tokens; pattern = glob
  - pattern: "claude-sonnet*"
    input_per_mtok: 3.0
    output_per_mtok: 15.0
    cache_read_per_mtok: 0.3
    cache_write_per_mtok: 3.75
```

### Retry behavior

| Status | Action |
|---|---|
| 401/403 | key cooldown 10 minutes, try next key/provider |
| 408/409/429/500/502/503/504/529 | cooldown with exponential backoff (10s..5m), keep trying |
| other 4xx | error passed to the client as-is (fatal) |
| network | same as soft-fail |

Retry budget per request: `retry.max_attempts` (default 8).

## Admin & observability

Dashboard: **http://localhost:8090/admin/dashboard** — enter `GATEWAY_TOKEN` or
`GATEWAY_ADMIN_TOKEN` (stored in localStorage, auto-refresh 5s). Inside:
today's cards, 24h and 14-day charts (UTC), per-provider/model tables,
recent request feed.

Hot reload (edit `config.yaml` — providers/keys/routing/prices apply without restart):

```bash
curl -X POST -H "x-api-key: ccg-admin-token" http://localhost:8090/admin/reload
```

Prometheus metrics (token can be passed via header or `?token=`):

```bash
curl -H "x-api-key: ccg-admin-token" http://localhost:8090/metrics
```

Webhooks:

```yaml
webhooks:
  - url: "https://hooks.example.com/ccg"
    secret: "${WEBHOOK_SECRET}"     # X-CCG-Signature: sha256=hex(hmac(secret, body))
    events: [usage, error]          # empty = all
    timeout: 5s
```

## MCP server

The gateway itself acts as an MCP server — Claude (Desktop / Code) gets the tools
`gateway_stats`, `gateway_logs`, `gateway_models`, `gateway_providers`, `gateway_tokens`,
`gateway_reload`, and `estimate_cost`.

HTTP transport:

```bash
claude mcp add --transport http ccg http://localhost:8090/mcp --header "x-api-key: ccg-admin-token"
```

Stdio transport (the process connects to the gateway locally):

```bash
claude mcp add ccg -- ./bin/gateway -config config.yaml -mcp
```

## Transformers and combos

```yaml
providers:
  - name: nine-router
    type: openai
    # ...
    transformers:
      - "max_tokens_cap:16384"
      - "reasoning_effort:high"
      - "system_prefix:Reply in Russian."

routing:
  rules:
    - prefix: "combo/fast"       # combo/fast model inside Claude Code
      targets:
        - { provider: nine-router, model: ag/gemini-3.7-flash-low }
        - { provider: nine-router, model: ag/gpt-oss-120b-medium }
```

```bash
curl http://localhost:8090/healthz
curl -H "x-api-key: ccg-admin-token" http://localhost:8090/admin/stats
curl -H "x-api-key: ccg-admin-token" "http://localhost:8090/admin/logs?limit=20"
tail -f data/usage.jsonl
```

Launcher (starts the gateway and immediately runs Claude Code with the right env):

```bash
go run ./cmd/gateway -launch -config config.yaml -- --resume
```

Cost is calculated against the `pricing` table (first matching pattern). Models outside the table cost 0;
tokens are still logged. Adjust prices to your stack.

## 9router

The bundled config connects 9router (`nine-router`). It's OpenAI-compatible: models are pulled
automatically after the first run. If the catalog is empty or the provider expects the Anthropic protocol —
switch the type to `anthropic-compat` (an example is commented out in `config.example.yaml`).

## Development

```bash
make test      # go test ./...
make vet       # go vet ./...
make run       # local run
make docker-up # build+run in docker
```

Structure:

```
cmd/gateway             entry point + claude launcher
internal/core           Anthropic/OpenAI types, protocol translation (req/resp/SSE), stream collectors
internal/provider       key pools, request execution, routing; SigV4 and event-stream for Bedrock
internal/server         HTTP: /v1/messages, /v1/models, admin, dashboard, /metrics; e2e tests
internal/cache          LRU+TTL response cache
internal/logstore       JSONL log + aggregates (day/hour/model/provider)
internal/pricing        glob price table, cost calculation
internal/ratelimit      RPM limiter per token
internal/config         YAML + ${ENV}
```

Tests cover protocol translation, SigV4 (AWS test vectors), the event-stream decoder, GCP OAuth flow,
cache, webhooks, and end-to-end scenarios through fake upstreams: key rotation, provider fallback,
rate limiting, streaming.

## Roadmap

- Webhook deduplication (retry with backoff)
- Streaming response caching (idempotency is debatable — not doing it yet)

## Credits

Ideas honestly stolen from the best in class:

- [claude-code-router](https://github.com/musistudio/claude-code-router) — scenario routing
  (longContext / image / think), request transformers, model-picker aliases
- [LiteLLM](https://github.com/BerriAI/litellm) — virtual keys: budgets, allowed_models, TPM
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) — MCP server, fallback chains, model discovery, dashboard
- [one-api / new-api](https://github.com/songquanpeng/one-api) — channel weights, circuit breaker,
  billing CSV export, runtime key management
- [gpt-load](https://github.com/tbphp/gpt-load) — key-pool health probes
- [uni-api](https://github.com/yym68686/uni-api) — per-key rate limits in YAML
- [Helicone](https://github.com/Helicone/helicone) — TTFT metric
- [Bifrost](https://github.com/maximhq/bifrost) — embedding-based semantic cache
- [Portkey Gateway](https://github.com/Portkey-AI/gateway) — request/response guardrails

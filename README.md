# claude-code-gateway

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![Release](https://img.shields.io/github/v/release/Mukller/claude-code-gateway?style=flat-square&color=blue)](https://github.com/Mukller/claude-code-gateway/releases)
[![Tests](https://img.shields.io/badge/tests-90%2B-green?style=flat-square)](https://github.com/Mukller/claude-code-gateway/actions)
[![Docker](https://img.shields.io/badge/docker-ghcr-blue?style=flat-square&logo=docker)](https://ghcr.io/mukller/claude-code-gateway)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](LICENSE)

Self-hosted AI gateway in pure Go for **Claude Code** and any OpenAI/Anthropic-compatible client. One binary, zero heavy dependencies, multi-provider routing with key rotation, fallbacks, budgets, semantic cache, guardrails, and an MCP control surface.

> Inspired by the best: [OmniRoute](https://github.com/diegosouzapw/OmniRoute) · [claude-code-router](https://github.com/musistudio/claude-code-router) · [LiteLLM](https://github.com/BerriAI/litellm) · [one-api](https://github.com/songquanpeng/one-api) · [Bifrost](https://github.com/maximhq/bifrost) · [Portkey](https://github.com/Portkey-AI/gateway) — all credited in [Credits](#credits).

## Features

**Core**
- **6 provider types**: `anthropic`, `anthropic-compat`, `openai`, `bedrock` (SigV4 + event-stream), `vertex` (api-key / bearer / service-account), `antigravity` (free Claude Opus 4.5 via Google OAuth)
- Full **Anthropic ↔ OpenAI protocol translation**, both directions, including streaming SSE
- **Key rotation**: round-robin or fill-first, cooldown with exponential backoff, `Retry-After` respect
- **Fallback chains** + circuit breaker (5 consecutive failures → 2 min pause)
- **Load balancing**: `weighted` / `least_busy` / `latency` (EMA) strategies

**Routing**
- **Scenario routing** (from claude-code-router): `long_context` / `image` / `thinking` — route to different chains based on request content
- **Combo models**: one alias → chain of different models with fallback
- **Session-sticky routing** by `metadata.user_id`
- Prefix-based rules with model mapping and stripping

**Client management**
- **Virtual keys** (LiteLLM-style): named clients with USD budgets (daily/weekly/monthly), `allowed_models` glob patterns, TPM limits
- **Per-request headers** (Cloudflare AI Gateway-style): `x-ccg-skip-cache`, `x-ccg-cache-ttl`, `x-ccg-cache-key`, `x-ccg-collect-log`, `x-ccg-max-attempts`, `x-ccg-metadata`

**Observability**
- Web dashboard: charts (24h + 14d), per-provider/model breakdowns, live request feed, YAML config editor with validation + rollback
- Prometheus `/metrics`: requests, tokens, cost, latency histogram, per-provider/model
- Usage webhooks with HMAC signatures
- TTFT (time-to-first-byte) per streamed request
- CSV export

**Security**
- Guardrails: block/redact regex patterns on request and response, PII presets (`email`, `phone`, `card`), prompt-injection detection, denied tools, streaming scan
- Response cache: exact + **semantic** (embeddings-based), per-request TTL override
- Rate limiting: RPM + TPM per client token
- Distributed state: Redis or Postgres for multi-replica deployments

**Extensibility**
- **MCP server** (HTTP + stdio): manage the gateway from Claude — stats, logs, models, budgets, reload, cost estimation
- **Request transformers**: `max_tokens_cap`, `set:key=value`, `reasoning_effort`, `drop_keys`, `system_prefix`
- **Price auto-sync** from OpenRouter API
- **Hot config reload** (`POST /admin/reload`) — no restart needed
- YAML config editor with validation + backup + rollback

## Quick start

```bash
git clone https://github.com/Mukller/claude-code-gateway.git
cd claude-code-gateway
cp .env.example .env   # add your provider keys
docker compose up -d --build
```

Point Claude Code at the gateway:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8090
export ANTHROPIC_AUTH_TOKEN=ccg-local-dev-token
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1
claude
```

Or use a pre-built binary:

```bash
# Download from Releases
chmod +x cc-gateway-linux-amd64
./cc-gateway-linux-amd64 -config config.yaml
```

Or Docker:

```bash
docker run -d -p 8090:8090 \
  -v ./config.yaml:/app/config.yaml:ro \
  -v ./data:/app/data \
  -e NINE_ROUTER_KEY=sk-... \
  ghcr.io/mukller/claude-code-gateway:latest
```

## Provider examples

<details>
<summary>Anthropic (direct)</summary>

```yaml
providers:
  - name: anthropic
    type: anthropic
    base_url: "https://api.anthropic.com"
    keys: ["${ANTHROPIC_API_KEY}"]
```
</details>

<details>
<summary>OpenRouter</summary>

```yaml
providers:
  - name: openrouter
    type: openai
    base_url: "https://openrouter.ai/api/v1"
    keys: ["${OPENROUTER_KEY}"]
    discover_models: true
```
</details>

<details>
<summary>DeepSeek</summary>

```yaml
providers:
  - name: deepseek
    type: openai
    base_url: "https://api.deepseek.com/v1"
    keys: ["${DEEPSEEK_KEY}"]
    models:
      - deepseek-chat
      - deepseek-reasoner
```
</details>

<details>
<summary>Groq</summary>

```yaml
providers:
  - name: groq
    type: openai
    base_url: "https://api.groq.com/openai/v1"
    keys: ["${GROQ_KEY}"]
    discover_models: true
```
</details>

<details>
<summary>Mistral</summary>

```yaml
providers:
  - name: mistral
    type: openai
    base_url: "https://api.mistral.ai/v1"
    keys: ["${MISTRAL_KEY}"]
    discover_models: true
```
</details>

<details>
<summary>Together AI</summary>

```yaml
providers:
  - name: together
    type: openai
    base_url: "https://api.together.xyz/v1"
    keys: ["${TOGETHER_KEY}"]
    discover_models: true
```
</details>

<details>
<summary>xAI (Grok)</summary>

```yaml
providers:
  - name: xai
    type: openai
    base_url: "https://api.x.ai/v1"
    keys: ["${XAI_KEY}"]
```
</details>

<details>
<summary>Cerebras</summary>

```yaml
providers:
  - name: cerebras
    type: openai
    base_url: "https://api.cerebras.ai/v1"
    keys: ["${CEREBRAS_KEY}"]
```
</details>

<details>
<summary>Fireworks AI</summary>

```yaml
providers:
  - name: fireworks
    type: openai
    base_url: "https://api.fireworks.ai/inference/v1"
    keys: ["${FIREWORKS_KEY}"]
```
</details>

<details>
<summary>Ollama (local)</summary>

```yaml
providers:
  - name: ollama
    type: openai
    base_url: "http://localhost:11434/v1"
    keys: ["ollama"]
    models:
      - llama3:70b
      - codellama:34b
```
</details>

<details>
<summary>AWS Bedrock</summary>

```yaml
providers:
  - name: bedrock
    type: bedrock
    region: us-east-1
    keys: ["${AWS_ACCESS_KEY_ID}:${AWS_SECRET_ACCESS_KEY}"]
```
</details>

<details>
<summary>Google Vertex AI</summary>

```yaml
providers:
  - name: vertex
    type: vertex
    auth_style: api-key
    keys: ["${VERTEX_API_KEY}"]
```
</details>

<details>
<summary>Antigravity (free Claude Opus 4.5)</summary>

```yaml
providers:
  - name: antigravity
    type: antigravity
    antigravity_auth_file: "~/.local/share/opencode/auth.json"
```

Requires `opencode auth login` → Google → Antigravity (one-time).
</details>

## Configuration

<details>
<summary>Full config example</summary>

```yaml
server:
  listen: ":8090"
  keepalive_seconds: 15

auth:
  require_auth: true
  tokens:
    - "${GATEWAY_TOKEN}"
  admin_token: "${GATEWAY_ADMIN_TOKEN}"

clients:
  - name: anton
    token: "${GATEWAY_TOKEN}"
  - name: teammate
    token: "${TEAM_TOKEN}"
    budget_usd: 50
    budget_period: monthly
    allowed_models: ["glm*", "deepseek*"]
    tpm: 200000

providers:
  - name: anthropic
    type: anthropic
    base_url: "https://api.anthropic.com"
    keys: ["${ANTHROPIC_API_KEY}"]

  - name: openrouter
    type: openai
    base_url: "https://openrouter.ai/api/v1"
    keys: ["${OPENROUTER_KEY}"]
    discover_models: true
    weight: 3

routing:
  alias_claude_prefix: true
  default_chain: [anthropic, openrouter]
  rules:
    - prefix: "combo/fast"
      targets:
        - { provider: openrouter, model: "meta-llama/llama-4-scout" }
        - { provider: anthropic, model: "claude-haiku-4-5" }
    - prefix: "anthropic/"
      strip_prefix: true
      chain: [anthropic]
  scenarios:
    long_context:
      threshold_tokens: 80000
      chain: [anthropic]
    image:
      chain: [anthropic]

cache:
  enabled: true
  ttl: 30m

guardrails:
  request:
    pii_presets: [email]
    injection_detection: block

pricing_sync:
  enabled: true
  interval: 6h

state:
  redis_url: "redis://localhost:6379"
```
</details>

## API endpoints

| Endpoint | Description |
|---|---|
| `POST /v1/messages` | Anthropic Messages API (streaming + non-streaming) |
| `POST /v1/messages/count_tokens` | Token estimation |
| `GET /v1/models` | Model catalog (`?format=openai` for OpenAI shape) |
| `GET /healthz` | Health check |
| `GET /metrics` | Prometheus metrics |
| `POST /mcp` | MCP server (HTTP transport) |
| `GET /admin/dashboard` | Web dashboard |
| `GET /admin/stats` | Usage statistics |
| `GET /admin/logs?limit=N` | Recent request log |
| `GET /admin/tokens` | Client budgets and spend |
| `GET /admin/keys` | Provider key pool status |
| `GET /admin/config` | Sanitized config snapshot |
| `GET /admin/config/yaml` | Raw config.yaml |
| `POST /admin/config/yaml` | Update config (validated + backed up) |
| `POST /admin/config/rollback` | Rollback to previous config |
| `POST /admin/reload` | Hot reload providers/routing/pricing |
| `POST /admin/flush-cache` | Clear response cache |
| `GET /admin/export.csv` | Export usage log as CSV |

## MCP server

The gateway exposes itself as an MCP server — Claude can manage it natively:

```bash
# HTTP transport
claude mcp add --transport http ccg http://localhost:8090/mcp --header "x-api-key: ccg-admin-token"

# stdio transport
claude mcp add ccg -- ./bin/gateway -config config.yaml -mcp
```

Tools: `gateway_stats`, `gateway_logs`, `gateway_models`, `gateway_providers`, `gateway_tokens`, `gateway_reload`, `estimate_cost`.

## Architecture

```
cmd/gateway             entry point + claude launcher
internal/core           Anthropic/OpenAI types, protocol translation (req/resp/SSE)
internal/provider       key pool, request execution, routing registry, SigV4, event-stream
internal/server         HTTP handlers, dashboard, MCP, metrics, guardrails
internal/cache          LRU+TTL exact cache
internal/logstore       JSONL usage log + aggregates
internal/pricing        glob pricing table, cost calculation
internal/ratelimit      per-client RPM/TPM limiter
internal/state          Redis/Postgres/memory state backends
internal/config         YAML + env expansion
```

## Development

```bash
make test          # go test ./...
make vet           # go vet ./...
make fmt           # gofmt -w .
make run           # local start
make docker-up     # build + run in docker
```

## Credits

Ideas and inspiration from the best in class:

| Project | What we borrowed |
|---|---|
| [claude-code-router](https://github.com/musistudio/claude-code-router) | Scenario routing, transformers, model aliases |
| [LiteLLM](https://github.com/BerriAI/litellm) | Virtual keys, budgets, allowed_models, TPM |
| [OmniRoute](https://github.com/diegosouzapw/OmniRoute) | MCP server, fallback chains, model discovery, dashboard |
| [one-api / new-api](https://github.com/songquanpeng/one-api) | Channel weights, circuit breaker, CSV export, runtime key management |
| [gpt-load](https://github.com/tbphp/gpt-load) | Key pool health probes |
| [uni-api](https://github.com/yym68686/uni-api) | Per-key rate limits in YAML |
| [Helicone](https://github.com/Helicone/helicone) | TTFT metric |
| [Bifrost](https://github.com/maximhq/bifrost) | Semantic cache via embeddings |
| [Portkey Gateway](https://github.com/Portkey-AI/gateway) | Guardrails on request and response |
| [Cloudflare AI Gateway](https://developers.cloudflare.com/ai-gateway/) | Per-request headers, cache control |
| [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) | SSE keep-alive, session affinity, fill-first |

## License

MIT

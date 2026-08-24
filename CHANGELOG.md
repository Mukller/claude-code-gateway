# Changelog

All notable changes to this project will be documented in this file.

## [1.0.0] — 2026-08-23

### Core
- 6 provider types: `anthropic`, `anthropic-compat`, `openai`, `bedrock` (SigV4 + event-stream decoder), `vertex` (api-key/bearer/service-account with auto-OAuth), `antigravity` (free Claude Opus 4.5 via Google OAuth)
- Full Anthropic ↔ OpenAI protocol translation, both directions, including streaming SSE
- Anthropic-compatible API surface: `/v1/messages`, `/v1/messages/count_tokens`, `/v1/models`
- Passthrough mode for Anthropic-protocol upstreams (zero translation overhead)

### Routing
- Fallback chains with ordered failover across providers
- Scenario routing: `long_context` (token threshold), `image`, `thinking` — separate chains per request type
- Combo models: one alias → chain of different models
- Prefix-based rules with model mapping and prefix stripping
- Session-sticky routing by `metadata.user_id`
- Load balancing strategies: `weighted`, `least_busy`, `latency` (EMA)
- `claude/` prefix aliasing for Claude Code model picker discovery

### Key management
- Key rotation: round-robin or fill-first
- Cooldown with exponential backoff (10s–5min) on 429/5xx
- Hard cooldown (10 min) on 401/403
- `Retry-After` header respect
- Circuit breaker: 5 consecutive failures → provider skipped for 2 min
- Per-key statistics (attempts, fails, successes, cooldown)

### Client management
- Virtual keys: named clients with USD budgets (daily/weekly/monthly)
- `allowed_models` glob patterns per client
- RPM + TPM per-client rate limiting
- Runtime budget update via API (no restart)
- Token generation endpoint

### Observability
- Web dashboard: 24h + 14d charts, per-provider/model breakdowns, client budgets, live request feed, YAML config editor
- Prometheus `/metrics`: requests, errors, tokens, cost, latency histogram, per-provider/model
- Usage webhooks with HMAC signatures, retry on 5xx
- TTFT (time-to-first-byte) per streamed request
- CSV export
- Request/response logging with metadata support
- Health check with version, provider status, cache size

### Caching
- Exact-match response cache (LRU + TTL)
- Semantic cache via embeddings (cosine similarity)
- Per-request cache control: skip, custom TTL, custom key
- Cache status header (`X-Ccg-Cache-Status`)

### Guardrails
- Block patterns (regex) on request text
- Block patterns on response text (including buffered stream scan)
- PII presets: email, phone, card — redacted before sending upstream
- Prompt-injection detection: `off` / `flag` / `block`
- Denied tools list
- Max input tokens limit

### Extensibility
- MCP server (HTTP + stdio): 7 tools for gateway management from Claude
- Request transformers: `max_tokens_cap`, `set:key=value`, `reasoning_effort`, `drop_keys`, `system_prefix`
- Price auto-sync from OpenRouter API
- Hot config reload via API + YAML editor with validation + backup + rollback
- Config auto-reload on file change

### Infrastructure
- Distributed state: Redis (stdlib RESP2 client) or Postgres for multi-replica
- Per-request headers: skip-cache, cache-ttl, cache-key, collect-log, max-attempts, metadata
- SSE keep-alive comments during long upstream silence
- CORS allowlist
- JSON access log format
- Rate limiting per client token (RPM + TPM)
- Docker + docker-compose with healthcheck
- Helm chart for Kubernetes
- GitHub Actions CI (test + build + docker) and release (cross-built binaries + GHCR)

### Performance
- Simple request translation: ~2.2 µs (1024 B, 13 allocs)
- Tool-call translation: ~8.8 µs (2928 B, 52 allocs)
- 50KB request: ~0.3 ms (58 KB, 8 allocs)
- Response translation: ~0.5 µs (360 B, 4 allocs)
- 100-chunk stream: ~0.5 ms (270 KB, 3521 allocs)

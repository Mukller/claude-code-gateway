# claude-code-gateway

Лёгкий гейтвей на Go для **Claude Code**: единая точка входа в стиле Anthropic Messages API с
мультипровайдером, ротацией ключей, фолбэком между провайдерами, логами и подсчётом стоимости.
Идея и набор фич вдохновлены [OmniRoute](https://github.com/diegosouzapw/OmniRoute), реализация — своя,
только stdlib + yaml.

## Возможности

- **Anthropic-совместимый API**: `/v1/messages` (streaming SSE + non-streaming), `/v1/messages/count_tokens`, `/v1/models`
- **Типы провайдеров**:
  - `anthropic` — прямой api.anthropic.com (passthrough)
  - `anthropic-compat` — любой Anthropic-совместимый прокси (свой base_url, bearer/x-api-key)
  - `openai` — OpenAI-совместимые бэкенды (9router, OpenRouter, DeepSeek, GLM, Kimi, Ollama...) с полным
    переводом протокола туда-обратно, включая потоковый SSE; опция `send_stream_options` для точного usage
  - `bedrock` — AWS Bedrock (InvokeModel / invoke-with-response-stream): SigV4-подпись из stdlib,
    декодирование бинарного event-stream, ключ формата `AKID:SECRET[:SESSION_TOKEN]`
  - `vertex` — Google Vertex AI (`rawPredict`/`streamRawPredict`, нативный Anthropic-формат):
    Express API-key (`?key=`), bearer-токен, или **сервис-аккаунт** (`auth_style: sa` +
    `service_account_json`: JSON ключа или путь к файлу — JWT подписывается на месте,
    OAuth-токен кешируется и рефрешится автоматически)
- **Веб-дашборд** `/admin/dashboard`: карточки за сегодня, разбивка по провайдерам/моделям,
  лента последних запросов с ошибками, автообновление
- **Ротация ключей**: несколько ключей на провайдера, round-robin, cooldown при 401/403/429/5xx
  с экспоненциальным backoff + уважение `Retry-After` от upstream
- **Фолбэк-цепочки**: модель → список целей; при сбое ключа/провайдера запрос идёт дальше
- **Маршрутизация по префиксу**: правила вида `anthropic/* → provider anthropic`
- **Model discovery**: список моделей подтягивается с upstream (`/v1/models`) автоматически
- **claude/ алиасы**: дублирует модели под `claude/<id>`, чтобы они появлялись в родном пикере моделей Claude Code
  (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`)
- **Логи и стоимость**: каждая запись — токены, latency, статус, оценка стоимости в USD; JSONL-файл + агрегаты
- **Кэш ответов (opt-in)**: одинаковые non-stream запросы отдаются из LRU+TTL кэша
  (`cache.enabled: true`) — для реплеев и оценок; в логе такие записи помечены `"cached":true`
- **MCP-сервер** (как в OmniRoute): управляй гейтвеем из Claude — статистика, логи, модели,
  бюджеты, hot-reload и оценка стоимости. HTTP-транспорт `/mcp` или stdio (`-mcp`)
- **Трансформеры запросов** (как transformers в CCR): `max_tokens_cap`, `set:key=value`,
  `reasoning_effort`, `drop_keys`, `system_prefix` — на уровне провайдера
- **Комбо-модели**: один алиас → цепочка разных моделей с фолбэком
  (`combo/fast` → glm-flash, затем gpt-oss)
- **Сценарная маршрутизация** (идея из claude-code-router): отдельные цепочки для
  длинного контекста (`long_context.threshold_tokens`), запросов с картинками (`image`)
  и thinking-запросов (`thinking`) — например, тяжёлый контекст уходит на модель с большим окном
- **Бюджеты на клиентов** (идея из LiteLLM virtual keys): именованные токены с лимитом USD
  на день/неделю/месяц; при исчерпании — `429 budget exceeded`; `/admin/tokens` показывает
  расход и дату сброса
- **Rate limit** (на токен клиента), admin-API (`/admin/stats`, `/admin/logs`, `/admin/tokens`,
  `POST /admin/reload` — горячая перезагрузка провайдеров/роутинга/прайсинга без рестарта),
  `/metrics` в формате Prometheus, healthcheck (`/healthz` + флаг `-healthcheck` для docker/compose)
- **Вебхуки**: push событий `usage`/`error` на твой URL с HMAC-подписью (`X-CCG-Signature`)
- **Лаунчер**: `gateway -launch -- "твои флаги claude"` — поднимает гейтвей и стартует Claude Code
  с уже прописанными env

## Быстрый старт

```bash
cp .env.example .env        # вставь NINE_ROUTER_KEY и свои токены
cp config.example.yaml config.yaml   # при необходимости поправь провайдеров

docker compose up -d --build
# или локально:
go run ./cmd/gateway -config config.yaml
```

## Подключение Claude Code

Claude Code указывают на гейтвей переменными окружения (**без** `/v1` в base URL):

```bash
export ANTHROPIC_BASE_URL=http://localhost:8090
export ANTHROPIC_AUTH_TOKEN=ccg-local-dev-token          # GATEWAY_TOKEN из .env
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1      # модели из /v1/models в пикере /model
claude
```

Модели без префикса `claude*` не показываются в нативном пикере Claude Code, но доступны через
алиасы `claude/<id>` (включены по умолчанию, см. `routing.alias_claude_prefix`) либо через
`ANTHROPIC_MODEL=<id>`.

## Конфигурация

Все строковые значения поддерживают `${VAR}` и `${VAR:-default}` (подстановка из окружения).

```yaml
server:
  listen: ":8090"

auth:
  tokens: ["${GATEWAY_TOKEN}"]     # чем клиент представляется гейтвею
  admin_token: "${GATEWAY_ADMIN_TOKEN}"
  allow_anon: false                # true = пускать всех (только для локальных тестов)

rate_limit_rpm: 0                  # 0 = выключен

providers:
  - name: nine-router              # любое имя, на него ссылаются chain/rules
    type: openai                   # anthropic | anthropic-compat | openai
    base_url: "https://9router.kitory.lol/v1"
    keys: ["${NINE_ROUTER_KEY}"]   # сколько угодно ключей — ротация включится сама
    discover_models: true          # тянуть каталог моделей с upstream
    refresh_interval: 15m
    timeout: 300s

routing:
  alias_claude_prefix: true
  default_chain: [nine-router]     # куда идут все неразобранные модели
  rules:
    - prefix: "anthropic/"         # model "anthropic/claude-*" -> провайдер anthropic
      strip_prefix: true
      chain: [anthropic]

pricing:                           # USD за 1M токенов; pattern = glob
  - pattern: "claude-sonnet*"
    input_per_mtok: 3.0
    output_per_mtok: 15.0
    cache_read_per_mtok: 0.3
    cache_write_per_mtok: 3.75
```

### Поведение ретраев

| Статус | Действие |
|---|---|
| 401/403 | ключ в cooldown 10 минут, пробуем следующий ключ/провайдера |
| 408/409/429/500/502/503/504/529 | cooldown с экспон. backoff (10s..5m), пробуем дальше |
| прочие 4xx | ошибка отдаётся клиенту как есть (фатально) |
| сеть | то же, что soft-fail |

Бюджет попыток на запрос: `retry.max_attempts` (по умолчанию 8).

## Админка и наблюдение

Дашборд: **http://localhost:8090/admin/dashboard** — введи `GATEWAY_TOKEN` или
`GATEWAY_ADMIN_TOKEN` (сохраняется в localStorage, автообновление 5с). Внутри:
карточки за сегодня, графики за 24ч и 14 дней (UTC), таблицы по провайдерам/моделям,
лента последних запросов.

Горячая перезагрузка (меняешь `config.yaml` — провайдеры/ключи/роутинг/цены применяются без рестарта):

```bash
curl -X POST -H "x-api-key: ccg-admin-token" http://localhost:8090/admin/reload
```

Метрики для Prometheus (токен можно передать заголовком или `?token=`):

```bash
curl -H "x-api-key: ccg-admin-token" http://localhost:8090/metrics
```

Вебхуки:

```yaml
webhooks:
  - url: "https://hooks.example.com/ccg"
    secret: "${WEBHOOK_SECRET}"     # X-CCG-Signature: sha256=hex(hmac(secret, body))
    events: [usage, error]          # пусто = все
    timeout: 5s
```

## MCP-сервер

Гейтвей сам выступает MCP-сервером — Claude (Desktop / Code) получает инструменты
`gateway_stats`, `gateway_logs`, `gateway_models`, `gateway_providers`, `gateway_tokens`,
`gateway_reload` и `estimate_cost`.

HTTP-транспорт:

```bash
claude mcp add --transport http ccg http://localhost:8090/mcp --header "x-api-key: ccg-admin-token"
```

Stdio-транспорт (процесс подключается к гейтвею локально):

```bash
claude mcp add ccg -- ./bin/gateway -config config.yaml -mcp
```

## Трансформеры и комбо

```yaml
providers:
  - name: nine-router
    type: openai
    # ...
    transformers:
      - "max_tokens_cap:16384"
      - "reasoning_effort:high"
      - "system_prefix:Отвечай по-русски."

routing:
  rules:
    - prefix: "combo/fast"       # модель combo/fast в Claude Code
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

Лаунчер (поднимает гейтвей и сразу запускает Claude Code с нужными env):

```bash
go run ./cmd/gateway -launch -config config.yaml -- --resume
```

Стоимость считается по таблице `pricing` (первый совпавший паттерн). Модели вне таблицы — стоимость 0,
токены всё равно логируются. Правь цены под свой стек.

## 9router

В конфиге из коробки подключён 9router (`nine-router`). Он OpenAI-совместимый: модели подтягиваются
автоматически после первого запуска. Если каталог пуст или провайдер ожидает Anthropic-протокол —
переключи тип на `anthropic-compat` (пример закомментирован в `config.example.yaml`).

## Разработка

```bash
make test      # go test ./...
make vet       # go vet ./...
make run       # локальный запуск
make docker-up # сборка+запуск в docker
```

Структура:

```
cmd/gateway             точка входа + лаунчер claude
internal/core           типы Anthropic/OpenAI, трансляция протоколов (req/resp/SSE), сборщики стримов
internal/provider       пул ключей, исполнение запросов, роутинг; SigV4 и event-stream для Bedrock
internal/server         HTTP: /v1/messages, /v1/models, админка, дашборд, /metrics; e2e-тесты
internal/cache          LRU+TTL кэш ответов
internal/logstore       JSONL-лог + агрегаты (день/час/модель/провайдер)
internal/pricing        glob-таблица цен, расчёт стоимости
internal/ratelimit      лимитер RPM на токен
internal/config         YAML + ${ENV}
```

Тесты покрывают трансляцию протоколов, SigV4 (вектор AWS), event-stream декодер, GCP OAuth-флоу,
кэш, вебхуки и сквозные сценарии через фейковые upstream: ротация ключей, фолбэк провайдеров,
rate limit, стриминг.

## Roadmap

- Дедупликация вебхуков (retry с backoff)
- Кэширование стримовых ответов (идемпотентность спорна — пока не делаем)

## Credits

Идеи честно украдены у лучших в классе:

- [claude-code-router](https://github.com/musistudio/claude-code-router) — сценарная маршрутизация
  (longContext / image / think), трансформеры запросов, алиасы для пикера моделей
- [LiteLLM](https://github.com/BerriAI/litellm) — виртуальные ключи с бюджетами и spend tracking
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) — MCP-сервер, фолбэк-цепочки, model discovery, дашборд

# Contributing

## Development setup

```bash
git clone https://github.com/Mukller/claude-code-gateway.git
cd claude-code-gateway
go build ./...
go test ./...
```

Requires Go 1.26+.

## Before submitting a PR

```bash
gofmt -w .
go vet ./...
go test ./... -count=1
```

All three must pass. CI runs the same checks.

## Code style

- No comments unless explaining a non-obvious invariant
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Table-driven tests
- `gofmt` formatting (CI enforces)

## Adding a provider

1. Add the type string to `internal/config/config.go` validation
2. Add initialization in `internal/provider/provider.go` `New()` switch
3. Add payload building in `internal/server/handlers.go` `buildPayload()`
4. Add config example to `config.example.yaml`
5. Add test coverage

## Adding a feature

1. Write the test first
2. Implement
3. Update `README.md` features section
4. Update `CHANGELOG.md`
5. Update `config.example.yaml` if new config options

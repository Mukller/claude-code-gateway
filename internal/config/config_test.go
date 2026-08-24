package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadMinimal(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	os.WriteFile(path, []byte(`
server:
  listen: ":9090"
auth:
  tokens: ["tok"]
providers:
  - name: p1
    type: openai
    base_url: "https://x.test/v1"
    keys: ["k1"]
routing:
  default_chain: [p1]
`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Fatalf("listen = %s", cfg.Server.Listen)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "p1" {
		t.Fatal("provider missing")
	}
	if cfg.Retry.MaxAttempts != 8 {
		t.Fatalf("default max_attempts = %d", cfg.Retry.MaxAttempts)
	}
}

func TestLoadEnvExpansion(t *testing.T) {
	os.Setenv("TEST_GW_KEY", "sk-test-123")
	defer os.Unsetenv("TEST_GW_KEY")
	dir := t.TempDir()
	path := dir + "/config.yaml"
	os.WriteFile(path, []byte(`
providers:
  - name: p1
    type: openai
    base_url: "https://x.test/v1"
    keys: ["${TEST_GW_KEY}"]
`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].Keys[0] != "sk-test-123" {
		t.Fatalf("env not expanded: %s", cfg.Providers[0].Keys[0])
	}
}

func TestLoadEnvDefault(t *testing.T) {
	os.Unsetenv("TEST_MISSING_VAR")
	dir := t.TempDir()
	path := dir + "/config.yaml"
	os.WriteFile(path, []byte(`
providers:
  - name: p1
    type: openai
    base_url: "https://x.test/v1"
    keys: ["${TEST_MISSING_VAR:-fallback}"]
`), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].Keys[0] != "fallback" {
		t.Fatalf("default not used: %s", cfg.Providers[0].Keys[0])
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"no providers", "auth: {tokens: [x]}", "no provider"},
		{"bad type", "providers:\n  - name: x\n    type: bogus\n    base_url: 'https://x'\n    keys: [k]", "unknown type"},
		{"no keys", "providers:\n  - name: x\n    type: openai\n    base_url: 'https://x'", "at least one API key"},
		{"dup name", "providers:\n  - name: a\n    type: openai\n    base_url: 'https://x'\n    keys: [k]\n  - name: a\n    type: openai\n    base_url: 'https://y'\n    keys: [k]", "duplicate"},
		{"bad chain", "providers:\n  - name: a\n    type: openai\n    base_url: 'https://x'\n    keys: [k]\nrouting:\n  default_chain: [nonexistent]", "unknown provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/config.yaml"
			os.WriteFile(path, []byte(tt.yaml), 0o644)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if c.Server.Listen != ":8080" {
		t.Fatal("default listen")
	}
	if c.Retry.MaxAttempts != 8 {
		t.Fatal("default max_attempts")
	}
	if c.Logging.File != "data/usage.jsonl" {
		t.Fatal("default log file")
	}
	if c.Cache.TTL != 10*time.Minute {
		t.Fatal("default cache ttl")
	}
	if c.Cache.MaxEntries != 128 {
		t.Fatal("default cache max")
	}
}

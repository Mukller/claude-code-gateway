package server

import (
	"strings"
	"testing"

	"claude-code-gateway/internal/config"
)

func TestFreeModeAllows(t *testing.T) {
	s := &Server{
		cfg: configWithFreeMode(t, true, []string{"openai/*", "deepseek/*"}),
	}
	if blocked := s.enforceFreeOnly("openai/gpt-4o"); blocked != "" {
		t.Fatalf("allowed model blocked: %s", blocked)
	}
	if blocked := s.enforceFreeOnly("deepseek/deepseek-v4-flash-free"); blocked != "" {
		t.Fatalf("allowed model blocked: %s", blocked)
	}
}

func TestFreeModeBlocks(t *testing.T) {
	s := &Server{
		cfg: configWithFreeMode(t, true, []string{"openai/*"}),
	}
	blocked := s.enforceFreeOnly("anthropic/claude-opus-4")
	if blocked == "" {
		t.Fatal("paid model should be blocked")
	}
	if !strings.Contains(blocked, "free_only") {
		t.Fatalf("error missing free_only: %s", blocked)
	}
}

func TestFreeModeDisabled(t *testing.T) {
	s := &Server{
		cfg: configWithFreeMode(t, false, []string{"openai/*"}),
	}
	if blocked := s.enforceFreeOnly("anthropic/claude-opus-4"); blocked != "" {
		t.Fatalf("free_only=false should allow everything: %s", blocked)
	}
}

func TestFreeModeNoConfig(t *testing.T) {
	s := &Server{
		cfg: configWithFreeMode(t, true, nil),
	}
	if blocked := s.enforceFreeOnly("openai/gpt-4o"); blocked == "" {
		t.Fatal("free_only without free_models should block all")
	}
}

func configWithFreeMode(t *testing.T, enabled bool, models []string) *config.Config {
	t.Helper()
	return &config.Config{
		Routing: config.Routing{FreeOnly: enabled, FreeModels: models},
		Auth:     config.Auth{Tokens: []string{"x"}, AdminToken: "a"},
	}
}

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"claude-code-gateway/internal/config"
)

func TestE2EOpenAIChatCompletions(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("hello from openai endpoint"))
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	_, ts := buildTestServer(t, cfg)

	body := `{"model":"test-model","messages":[{"role":"user","content":"hello"}],"max_tokens":50}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var r map[string]any
	if json.Unmarshal(data, &r) != nil {
		t.Fatalf("bad json: %s", data)
	}
	if r["object"] != "chat.completion" {
		t.Fatalf("object = %v", r["object"])
	}
	choices, _ := r["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("no choices")
	}
	c0 := choices[0].(map[string]any)
	if c0["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]any)
	if msg["content"] != "hello from openai endpoint" {
		t.Fatalf("content = %v", msg["content"])
	}
	usage := r["usage"].(map[string]any)
	if usage["total_tokens"].(float64) <= 0 {
		t.Fatal("usage missing")
	}
}

func TestE2EOpenAIAuth(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()
	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	_, ts := buildTestServer(t, cfg)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth must 401, got %d", resp.StatusCode)
	}
}

func TestE2EOpenAIBudgetBlocked(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()
	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Clients = []config.Client{
		{Name: "zero", Token: "tok-zero", BudgetUSD: 0},
	}
	s, ts := buildTestServer(t, cfg)
	s.budgets.updateLimitByName("zero", 0)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`))
	req.Header.Set("Authorization", "Bearer tok-zero")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("zero budget must block: %d", resp.StatusCode)
	}
}

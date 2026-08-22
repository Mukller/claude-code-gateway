package server

import (
	"encoding/json"
	"strings"
	"testing"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/core"
	"claude-code-gateway/internal/provider"
)

func testProvider(t *testing.T, ptype string) *provider.Provider {
	t.Helper()
	return provider.New(config.Provider{
		Name:    "test-" + ptype,
		Type:    ptype,
		BaseURL: "https://example.test",
		Keys:    []string{"k1"},
	})
}

func TestBuildPayloadAnthropic(t *testing.T) {
	p := testProvider(t, "anthropic")
	body := []byte(`{"model":"orig","max_tokens":10,"messages":[],"temperature":0.5}`)
	out, path, err := buildPayload(p, "claude-x", body, &core.MessagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" {
		t.Fatalf("path = %s", path)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["model"] != "claude-x" || m["temperature"] != 0.5 {
		t.Fatalf("bad payload: %s", out)
	}
}

func TestBuildPayloadBedrock(t *testing.T) {
	p := testProvider(t, "bedrock")
	body := []byte(`{"model":"orig","max_tokens":10,"stream":true,"metadata":{"u":"x"},"messages":[{"role":"user","content":"hi"}]}`)
	out, path, err := buildPayload(p, "us.anthropic.claude-4:v1", body, &core.MessagesRequest{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/model/us.anthropic.claude-4%3Av1/invoke-with-response-stream" {
		t.Fatalf("path = %s", path)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	for _, k := range []string{"model", "stream", "metadata"} {
		if _, ok := m[k]; ok {
			t.Fatalf("field %q must be stripped for bedrock", k)
		}
	}
	if m["anthropic_version"] != "bedrock-2023-05-31" {
		t.Fatalf("anthropic_version = %v", m["anthropic_version"])
	}
	if _, ok := m["max_tokens"]; !ok {
		t.Fatal("max_tokens must be preserved")
	}
}

func TestBuildPayloadVertex(t *testing.T) {
	p := testProvider(t, "vertex")
	body := []byte(`{"model":"orig","messages":[{"role":"user","content":"hi"}]}`)
	out, path, err := buildPayload(p, "claude-sonnet-4@20250514", body, &core.MessagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict" {
		t.Fatalf("path = %s", path)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if _, ok := m["model"]; ok {
		t.Fatal("model must be stripped for vertex")
	}
	if m["anthropic_version"] != "2023-06-01" {
		t.Fatalf("anthropic_version = %v", m["anthropic_version"])
	}
}

func TestBuildPayloadOpenAIStreamOptions(t *testing.T) {
	p := provider.New(config.Provider{
		Name: "o", Type: "openai", BaseURL: "https://example.test",
		Keys: []string{"k"}, SendStreamOptions: true,
	})
	body := []byte(`{"model":"m","max_tokens":5,"stream":true,"messages":[]}`)
	out, path, err := buildPayload(p, "glm-x", body, &core.MessagesRequest{Stream: true, MaxTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("path = %s", path)
	}
	if !strings.Contains(string(out), `"include_usage":true`) {
		t.Fatalf("stream_options missing: %s", out)
	}
}

func TestClassifyStatus(t *testing.T) {
	if classifyStatus(401, nil, []int{401}) != failHard {
		t.Fatal("401 should be hard fail")
	}
	if classifyStatus(429, []int{429}, []int{401}) != failSoft {
		t.Fatal("429 should be soft fail")
	}
	if classifyStatus(400, []int{429}, []int{401}) != failFatal {
		t.Fatal("400 should be fatal")
	}
}

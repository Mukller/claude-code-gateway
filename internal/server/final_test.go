package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-code-gateway/internal/config"
)

func semanticEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var v []float32
		switch {
		case strings.Contains(req.Input, "capital of France"):
			v = []float32{1, 0.05}
		case strings.Contains(req.Input, "Paris is the city"):
			v = []float32{0.97, 0.1}
		default:
			v = []float32{-0.5, 0.9}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": v, "index": 0}},
		})
	}))
}

func TestE2ESemanticCache(t *testing.T) {
	upstreamHits := 0
	var mu sync.Mutex
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		mu.Lock()
		upstreamHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c",
			"choices": []map[string]any{{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "Paris!"}, "finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2},
		})
	})
	defer fake.Close()
	emb := semanticEmbedServer(t)
	defer emb.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Cache.Enabled = true
	cfg.Cache.TTL = 5 * time.Minute
	cfg.Cache.Semantic = config.SemanticCache{
		Enabled: true, Threshold: 0.95,
		Endpoint: emb.URL, Model: "test-embed",
	}
	s, ts := buildTestServer(t, cfg)

	r1c, r1b, _ := postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"What is the capital of France?"}]}`)
	if r1c != 200 || !strings.Contains(r1b, "Paris!") {
		t.Fatalf("first must hit upstream: %d %s", r1c, r1b)
	}

	r2c, r2b, _ := postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"Paris is the city of ...? tell me"}]}`)
	if r2c != 200 || !strings.Contains(r2b, "Paris!") {
		t.Fatalf("semantic hit failed: %d %s", r2c, r2b)
	}

	mu.Lock()
	hits := upstreamHits
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("second request must be served from cache, upstream hits=%d", hits)
	}
	v := s.store.Snapshot()
	last := v.Recent[len(v.Recent)-1]
	if !last.Cached || !last.SemanticHit {
		t.Fatalf("record flags: %+v", last)
	}
}

func TestE2EGuardrails(t *testing.T) {
	var mu sync.Mutex
	var lastIn string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastIn = string(body)
		mu.Unlock()
		text := "clean answer"
		if strings.Contains(lastIn, "secret") {
			text = "leaked SECRET_VALUE here"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c",
			"choices": []map[string]any{{"index": 0,
				"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: upstream.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Guardrails = config.Guardrails{
		Request: config.ReqGuardrails{
			BlockedPatterns: []string{"(?i)drop table"},
			DeniedTools:     []string{"run_shell"},
			MaxInputTokens:  100000,
		},
		Response: config.RespGuardrails{
			BlockedPatterns: []string{"SECRET_VALUE"},
		},
	}
	s, ts := buildTestServer(t, cfg)
	_ = s

	code, body, _ := postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"please DROP TABLE users"}]}`)
	if code != 400 || !strings.Contains(body, "blocked by guardrail") {
		t.Fatalf("request pattern must be blocked: %d %s", code, body)
	}

	code, body, _ = postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"tools":[{"name":"run_shell","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "not allowed") {
		t.Fatalf("denied tool must be blocked: %d %s", code, body)
	}

	code, body, _ = postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"tell me a secret"}]}`)
	if code != 400 || !strings.Contains(body, "SECRET_VALUE") {
		t.Fatalf("response pattern must block output: %d %s", code, body)
	}

	code, _, _ = postMessagesAs(t, ts.URL, "tok",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"normal question"}]}`)
	if code == 400 {
		t.Fatal("clean traffic must pass")
	}
}

func TestE2ERuntimeBudgetUpdate(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Clients = []config.Client{
		{Name: "dima", Token: "tok-dima", BudgetUSD: 100},
	}
	s, ts := buildTestServer(t, cfg)

	ur, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/tokens/update",
		strings.NewReader(`{"name":"dima","budget_usd":0}`))
	ur.Header.Set("x-api-key", "tok")
	ur.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(ur)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("update failed: %d", resp.StatusCode)
	}

	code, body, _ := postMessagesAs(t, ts.URL, "tok-dima",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "budget exceeded") {
		t.Fatalf("zeroed budget must block instantly: %d %s", code, body)
	}

	cr, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/config", nil)
	cr.Header.Set("x-api-key", "tok")
	cResp, err := http.DefaultClient.Do(cr)
	if err != nil {
		t.Fatal(err)
	}
	defer cResp.Body.Close()
	data, _ := io.ReadAll(cResp.Body)
	if !strings.Contains(string(data), `"dima"`) || strings.Contains(string(data), "sk-live") {
		t.Fatalf("config snapshot wrong:\n%s", string(data))
	}
	_ = s
}

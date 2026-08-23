package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/pricing"
	"claude-code-gateway/internal/provider"
)

type oaiCall struct {
	Key   string
	Model string
}

func newFakeOpenAI(t *testing.T, respond func(call oaiCall, stream bool, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Steam  bool   `json:"-"`
			Stream bool   `json:"stream"`
		}
		json.Unmarshal(body, &req)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		respond(oaiCall{Key: key, Model: req.Model}, req.Stream, w)
	}))
}

func sseChunks(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func testConfig(providers []config.Provider, chain []string) *config.Config {
	cfg := &config.Config{}
	cfg.Server.Listen = ":0"
	cfg.Auth.Tokens = []string{"tok"}
	cfg.Providers = providers
	cfg.Routing.DefaultChain = chain
	cfg.Routing.AliasClaudePrefix = true
	cfg.Retry.MaxAttempts = 6
	cfg.Retry.RetryStatuses = []int{408, 409, 429, 500, 502, 503, 504, 529}
	cfg.Retry.KeyFailStatus = []int{401, 403}
	return cfg
}

func buildTestServer(t *testing.T, cfg *config.Config) (*Server, *httptest.Server) {
	t.Helper()
	reg := provider.NewRegistry(&cfg.Routing, cfg.Providers)
	store, err := logstore.New(filepath.Join(t.TempDir(), "usage.jsonl"), 100)
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, reg, store, pricing.New(nil))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		store.Close()
	})
	return s, ts
}

func postMessages(t *testing.T, url string, body string, headers map[string]string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "tok")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

func TestE2EOpenAINonStream(t *testing.T) {
	var mu sync.Mutex
	var calls []oaiCall
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		if stream {
			t.Error("client asked non-stream")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-1", "model": "glm-x",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "e2e answer"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 7},
		})
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k1"}},
	}, []string{"fake"})
	s, ts := buildTestServer(t, cfg)

	code, body, _ := postMessages(t, ts.URL,
		`{"model":"glm-x","max_tokens":64,"system":"be nice","messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 {
		t.Fatalf("status %d body %s", code, body)
	}
	var mr struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &mr); err != nil {
		t.Fatalf("bad json: %v\n%s", err, body)
	}
	if mr.Type != "message" || len(mr.Content) == 0 || mr.Content[0].Text != "e2e answer" {
		t.Fatalf("bad response: %s", body)
	}
	if mr.Usage.InputTokens != 11 || mr.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", mr.Usage)
	}

	mu.Lock()
	if len(calls) != 1 || calls[0].Key != "k1" || calls[0].Model != "glm-x" {
		t.Fatalf("upstream calls = %+v", calls)
	}
	mu.Unlock()

	v := s.store.Snapshot()
	if v.Total.Requests != 1 || v.Total.InTok != 11 || v.Total.OutTok != 7 {
		t.Fatalf("stats = %+v", v.Total)
	}
}

func TestE2EOpenAIStreamTranslation(t *testing.T) {
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		if !stream {
			t.Error("expected streaming upstream call")
			return
		}
		sseChunks(w,
			`{"choices":[{"index":0,"delta":{"content":"hel"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k1"}},
	}, []string{"fake"})
	_, ts := buildTestServer(t, cfg)

	code, body, hdr := postMessages(t, ts.URL,
		`{"model":"m","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %s", hdr.Get("Content-Type"))
	}
	for _, want := range []string{"event: message_start", `"text":"hel"`, `"type":"text_delta"`, `"text":"lo"`, `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q\nbody:\n%s", want, body)
		}
	}
	idxStart := strings.Index(body, "message_start")
	idxStop := strings.Index(body, "message_stop")
	if idxStart < 0 || idxStop < idxStart {
		t.Fatal("event order broken")
	}
}

func TestE2EKeyRotation(t *testing.T) {
	var mu sync.Mutex
	var seenKeys []string
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		mu.Lock()
		seenKeys = append(seenKeys, call.Key)
		reject := call.Key == "k1"
		mu.Unlock()
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-2", "choices": []map[string]any{{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "rotated"},
				"finish_reason": "stop"}},
		})
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k1", "k2"}},
	}, []string{"fake"})
	s, ts := buildTestServer(t, cfg)

	code, body, _ := postMessages(t, ts.URL, `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"x"}]}`, nil)
	if code != 200 || !strings.Contains(body, "rotated") {
		t.Fatalf("code=%d body=%s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenKeys) != 2 || seenKeys[0] != "k1" || seenKeys[1] != "k2" {
		t.Fatalf("keys tried = %v", seenKeys)
	}
	v := s.store.Snapshot()
	if v.ByProvider["fake"].Errors != 1 {
		t.Fatalf("expected one failed attempt logged, stats %+v", v.ByProvider)
	}
}

func TestE2EProviderFallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":{"message":"overloaded"}}`))
	}))
	defer bad.Close()
	good := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-3", "choices": []map[string]any{{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "from good"},
				"finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 4, "completion_tokens": 2},
		})
	})
	defer good.Close()

	cfg := testConfig([]config.Provider{
		{Name: "bad", Type: "openai", BaseURL: bad.URL + "/v1", Keys: []string{"kb"}},
		{Name: "good", Type: "openai", BaseURL: good.URL + "/v1", Keys: []string{"kg"}},
	}, []string{"bad", "good"})
	s, ts := buildTestServer(t, cfg)

	started := time.Now()
	code, body, _ := postMessages(t, ts.URL, `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"x"}]}`, nil)
	if time.Since(started) > 10*time.Second {
		t.Fatal("fallback took too long")
	}
	if code != 200 || !strings.Contains(body, "from good") {
		t.Fatalf("code=%d body=%s", code, body)
	}
	v := s.store.Snapshot()
	if v.ByProvider["good"].Requests != 1 {
		t.Fatalf("good provider not used: %+v", v.ByProvider)
	}
}

func TestE2EAllProvidersDown(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	cfg := testConfig([]config.Provider{
		{Name: "bad", Type: "openai", BaseURL: bad.URL + "/v1", Keys: []string{"kb"}},
	}, []string{"bad"})
	_, ts := buildTestServer(t, cfg)

	code, body, _ := postMessages(t, ts.URL, `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"x"}]}`, nil)
	if code != http.StatusServiceUnavailable && code != http.StatusBadGateway {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "overloaded_error") {
		t.Fatalf("error shape wrong: %s", body)
	}
}

func TestE2ERateLimitPerToken(t *testing.T) {
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c", "choices": []map[string]any{{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.RateLimit = 1
	s, ts := buildTestServer(t, cfg)

	code1, _, _ := postMessages(t, ts.URL, `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"a"}]}`, nil)
	code2, body2, _ := postMessages(t, ts.URL, `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"b"}]}`, nil)
	if code1 != 200 {
		t.Fatalf("first request failed: %d", code1)
	}
	if code2 != http.StatusTooManyRequests || !strings.Contains(body2, "rate_limit_error") {
		t.Fatalf("second must be rate limited: %d %s", code2, body2)
	}
	if s.store.Snapshot().Total.Requests != 1 {
		t.Fatal("rate limited request must not reach provider")
	}
}

func TestE2ECountTokensAndModels(t *testing.T) {
	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: "https://unused.test/v1",
			Keys: []string{"k"}, Models: []string{"static-a", "static-b"}},
	}, []string{"fake"})
	s, ts := buildTestServer(t, cfg)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages/count_tokens",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"count me"}]}`))
	req.Header.Set("x-api-key", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ct struct {
		InputTokens int64 `json:"input_tokens"`
	}
	json.NewDecoder(resp.Body).Decode(&ct)
	if ct.InputTokens <= 0 {
		t.Fatal("token estimate missing")
	}

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/models", nil)
	req2.Header.Set("x-api-key", "tok")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var ml struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(resp2.Body).Decode(&ml)
	ids := map[string]bool{}
	for _, d := range ml.Data {
		ids[d.ID] = true
	}
	if !ids["static-a"] || !ids["claude/static-b"] {
		t.Fatalf("catalog wrong: %v", ids)
	}
	_ = s
}

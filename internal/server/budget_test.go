package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-code-gateway/internal/config"
)

type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) bump() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

func (c *counter) val() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func respondOpenAI(answer string) func(oaiCall, bool, http.ResponseWriter) {
	return func(call oaiCall, stream bool, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c", "choices": []map[string]any{{"index": 0,
				"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
		})
	}
}

func TestE2EScenarioRoutingLongContext(t *testing.T) {
	bigHits, defHits := &counter{}, &counter{}
	big := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		bigHits.bump()
		respondOpenAI("big")(call, stream, w)
	})
	defer big.Close()
	def := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		defHits.bump()
		respondOpenAI("default")(call, stream, w)
	})
	defer def.Close()

	cfg := testConfig([]config.Provider{
		{Name: "big", Type: "openai", BaseURL: big.URL + "/v1", Keys: []string{"k"}},
		{Name: "def", Type: "openai", BaseURL: def.URL + "/v1", Keys: []string{"k"}},
	}, []string{"def"})
	cfg.Routing.Scenarios = config.Scenarios{
		LongContext: &config.LongContextScenario{ThresholdTokens: 100, Chain: []string{"big"}},
	}
	_, ts := buildTestServer(t, cfg)

	long := strings.Repeat("word ", 800)
	code, body, _ := postMessages(t, ts.URL,
		`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"`+long+`"}]}`, nil)
	if code != 200 || !strings.Contains(body, "big") {
		t.Fatalf("long ctx must route to big: %d %s", code, body)
	}

	code, body, _ = postMessages(t, ts.URL,
		`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if code != 200 || !strings.Contains(body, "default") {
		t.Fatalf("short ctx must stay on default: %d %s", code, body)
	}
	if bigHits.val() != 1 || defHits.val() != 1 {
		t.Fatalf("counters: big=%d default=%d", bigHits.val(), defHits.val())
	}
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func TestE2EImageScenario(t *testing.T) {
	visionHits, textHits := &counter{}, &counter{}
	vision := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		visionHits.bump()
		respondOpenAI("vision")(call, stream, w)
	})
	defer vision.Close()
	textual := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		textHits.bump()
		respondOpenAI("text-only")(call, stream, w)
	})
	defer textual.Close()

	cfg := testConfig([]config.Provider{
		{Name: "vision", Type: "openai", BaseURL: vision.URL + "/v1", Keys: []string{"k"}},
		{Name: "text", Type: "openai", BaseURL: textual.URL + "/v1", Keys: []string{"k"}},
	}, []string{"text"})
	cfg.Routing.Scenarios = config.Scenarios{
		Image: &config.ChainScenario{Chain: []string{"vision"}},
	}
	_, ts := buildTestServer(t, cfg)

	imgReq := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNG + `"}},` +
		`{"type":"text","text":"what is this?"}]}]}`
	code, body, _ := postMessages(t, ts.URL, imgReq, nil)
	if code != 200 || !strings.Contains(body, "vision") {
		t.Fatalf("image request must hit image-scenario chain: %d %s", code, body)
	}

	code, body, _ = postMessages(t, ts.URL,
		`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"plain text"}]}`, nil)
	if code != 200 || !strings.Contains(body, "text-only") {
		t.Fatalf("plain text must stay on default chain: %d %s", code, body)
	}
	if visionHits.val() != 1 || textHits.val() != 1 {
		t.Fatalf("counters: vision=%d text=%d", visionHits.val(), textHits.val())
	}
}

func TestBudgetEnforcement(t *testing.T) {
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "c",
			"choices": []map[string]any{{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 1000000, "completion_tokens": 0},
		})
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Clients = []config.Client{
		{Name: "dima", Token: "tok-dima", BudgetUSD: 1.0, BudgetPeriod: "daily"},
	}
	cfg.Pricing = []config.PriceRule{{Pattern: "m*", InputPerMTok: 10}}
	s, ts := buildTestServer(t, cfg)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "tok-dima")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first request must pass, got %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	t.Logf("reports after req1: %+v", s.tokenReports(time.Now()))
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi again"}]}`))
	req2.Header.Set("x-api-key", "tok-dima")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(data), "budget exceeded") {
		t.Fatalf("second request must be budget-blocked: %d %s", resp2.StatusCode, string(data))
	}

	reports := s.tokenReports(time.Now())
	found := false
	for _, tr := range reports {
		if tr.Name == "dima" {
			found = true
			if tr.SpentUSD <= 0 || tr.SpentUSD > 11 {
				t.Fatalf("spent tracking wrong: %+v", tr)
			}
		}
	}
	if !found {
		t.Fatal("client report missing")
	}
}

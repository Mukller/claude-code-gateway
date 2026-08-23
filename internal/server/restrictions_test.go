package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"claude-code-gateway/internal/config"
)

func postMessagesAs(t *testing.T, tsURL string, token, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, tsURL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

func httptestServer500(t *testing.T, hits *int, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		*hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
}

func TestE2EAllowedModels(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Clients = []config.Client{
		{Name: "restricted", Token: "tok-r", AllowedModels: []string{"safe-*", "combo/*"}},
	}
	_, ts := buildTestServer(t, cfg)

	code, body, _ := postMessagesAs(t, ts.URL, "tok-r", `{"model":"dangerous-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusForbidden || !strings.Contains(body, "not allowed") {
		t.Fatalf("restricted model must 403: %d %s", code, body)
	}

	code, body, _ = postMessagesAs(t, ts.URL, "tok-r", `{"model":"safe-1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Fatalf("allowed model must pass: %d %s", code, body)
	}

	code, _, _ = postMessagesAs(t, ts.URL, "tok", `{"model":"dangerous-x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("unlimited client must pass any model: %d", code)
	}
}

func TestE2ETPMLimit(t *testing.T) {
	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	cfg.Clients = []config.Client{
		{Name: "tiny", Token: "tok-t", TPM: 10},
	}
	_, ts := buildTestServer(t, cfg)

	long := strings.Repeat("word ", 400)
	code, body, _ := postMessagesAs(t, ts.URL, "tok-t",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"`+long+`"}]}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "tokens-per-minute") {
		t.Fatalf("over-TPM request must be blocked: %d %s", code, body)
	}

	code, _, _ = postMessagesAs(t, ts.URL, "tok-t",
		`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("small request within TPM must pass: %d", code)
	}
}

func TestE2ELoadBalanceWeights(t *testing.T) {
	heavyHits, lightHits := &counter{}, &counter{}
	heavy := newFakeOpenAI(t, func(c oaiCall, s bool, w http.ResponseWriter) {
		heavyHits.bump()
		respondOpenAI("heavy")(c, s, w)
	})
	defer heavy.Close()
	light := newFakeOpenAI(t, func(c oaiCall, s bool, w http.ResponseWriter) {
		lightHits.bump()
		respondOpenAI("light")(c, s, w)
	})
	defer light.Close()

	cfg := testConfig([]config.Provider{
		{Name: "heavy", Type: "openai", BaseURL: heavy.URL + "/v1", Keys: []string{"k"}, Weight: 9},
		{Name: "light", Type: "openai", BaseURL: light.URL + "/v1", Keys: []string{"k"}, Weight: 1},
	}, nil)
	cfg.Routing.Rules = []config.Rule{{
		Prefix:      "",
		Chain:       []string{"heavy", "light"},
		LoadBalance: true,
	}}
	_, ts := buildTestServer(t, cfg)

	for i := 0; i < 20; i++ {
		code, body, _ := postMessagesAs(t, ts.URL, "tok",
			`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"x"}]}`)
		if code != 200 {
			t.Fatalf("req %d failed: %d %s", i, code, body)
		}
	}
	if h, l := heavyHits.val(), lightHits.val(); h+l != 20 {
		t.Fatalf("hits=%d+%d", h, l)
	}
}

func TestE2ECircuitBreakerOpensAndSkips(t *testing.T) {
	var mu sync.Mutex
	badHits := 0
	bad := httptestServer500(t, &badHits, &mu)
	defer bad.Close()

	good := newFakeOpenAI(t, respondOpenAI("good"))
	defer good.Close()

	cfg := testConfig([]config.Provider{
		{Name: "bad", Type: "openai", BaseURL: bad.URL + "/v1", Keys: []string{"kb"}},
		{Name: "good", Type: "openai", BaseURL: good.URL + "/v1", Keys: []string{"kg"}},
	}, []string{"bad", "good"})
	s, ts := buildTestServer(t, cfg)

	for i := 0; i < 6; i++ {
		postMessagesAs(t, ts.URL, "tok", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"x"}]}`)
	}
	mu.Lock()
	hits := badHits
	mu.Unlock()
	if hits >= 6*2 {
		t.Fatalf("breaker should reduce direct attempts after opening, hits=%d", hits)
	}
	_ = s
}

func TestE2ETTFTAndCSVExport(t *testing.T) {
	fake := newFakeOpenAI(t, func(call oaiCall, stream bool, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"index":0,"delta":{"content":"a"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		} {
			w.Write([]byte("data: " + c + "\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "fake", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"fake"})
	s, ts := buildTestServer(t, cfg)

	code, body, hdr := postMessages(t, ts.URL,
		`{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	reqID := hdr.Get("X-Ccg-Request-Id")
	if code != 200 || reqID == "" {
		t.Fatalf("stream broken or no request id: %d %s", code, body)
	}
	v := s.store.Snapshot()
	if len(v.Recent) == 0 || v.Recent[len(v.Recent)-1].TTFTMs < 0 {
		t.Fatalf("ttft not recorded: %+v", v.Recent)
	}
	if v.Recent[len(v.Recent)-1].RequestID != reqID {
		t.Fatalf("request id mismatch: %q vs %q", v.Recent[len(v.Recent)-1].RequestID, reqID)
	}

	csvReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/export.csv", nil)
	csvReq.Header.Set("x-api-key", "tok")
	resp, err := http.DefaultClient.Do(csvReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(data), "time,client,request_id") || !strings.Contains(string(data), reqID) {
		t.Fatalf("csv export wrong:\n%s", string(data))
	}
}

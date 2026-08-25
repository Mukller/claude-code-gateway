package server

import (
	"net/http"
	"strings"
	"testing"

	"claude-code-gateway/internal/core"
)

func TestParseOverridesDefaults(t *testing.T) {
	o := parseOverrides(newTestRequest())
	if o.skipCache {
		t.Fatal("default skipCache must be false")
	}
	if !o.collectLog {
		t.Fatal("default collectLog must be true")
	}
	if o.maxAttempts != 0 {
		t.Fatal("default maxAttempts must be 0")
	}
	if o.ttlOverride != 0 {
		t.Fatal("default ttl must be 0")
	}
	if o.customKey != "" {
		t.Fatal("default customKey must be empty")
	}
}

func TestParseOverridesSkipCache(t *testing.T) {
	o := parseOverrides(newTestRequestWithHeader("X-Ccg-Skip-Cache", "true"))
	if !o.skipCache {
		t.Fatal("skipCache not set")
	}
}

func TestParseOverridesCacheTTL(t *testing.T) {
	o := parseOverridesWithHeaders(map[string]string{
		"X-Ccg-Cache-Ttl": "300",
	})
	if o.ttlOverride.Seconds() != 300 {
		t.Fatalf("ttl = %v", o.ttlOverride)
	}
}

func TestParseOverridesMaxAttempts(t *testing.T) {
	o := parseOverridesWithHeaders(map[string]string{"X-Ccg-Max-Attempts": "5"})
	if o.maxAttempts != 5 {
		t.Fatalf("maxAttempts = %d", o.maxAttempts)
	}
	// Cap at 10
	o = parseOverridesWithHeaders(map[string]string{"X-Ccg-Max-Attempts": "50"})
	if o.maxAttempts != 10 {
		t.Fatalf("capped maxAttempts = %d", o.maxAttempts)
	}
}

func TestParseOverridesMetadata(t *testing.T) {
	o := parseOverridesWithHeaders(map[string]string{
		"X-Ccg-Metadata": `{"user":"test","team":"dev"}`,
	})
	if o.metadata == nil {
		t.Fatal("metadata not parsed")
	}
	if o.metadata["user"] != "test" {
		t.Fatalf("metadata user = %v", o.metadata["user"])
	}
}

func TestParseOverridesCollectLogFalse(t *testing.T) {
	o := parseOverridesWithHeaders(map[string]string{"X-Ccg-Collect-Log": "false"})
	if o.collectLog {
		t.Fatal("collectLog must be false")
	}
}

func TestParseOverridesCustomKey(t *testing.T) {
	o := parseOverridesWithHeaders(map[string]string{"X-Ccg-Cache-Key": "my-key"})
	if o.customKey != "my-key" {
		t.Fatalf("customKey = %q", o.customKey)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("30"); d.Seconds() != 30 {
		t.Fatalf("30s parse = %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Fatalf("empty parse = %v", d)
	}
	if d := parseRetryAfter("-5"); d != 0 {
		t.Fatalf("negative parse = %v", d)
	}
}

func TestClassifyStatusOverride(t *testing.T) {
	soft := []int{429, 500, 502, 503}
	hard := []int{401, 403}
	if classifyStatus(429, soft, hard) != failSoft {
		t.Fatal("429 should be soft")
	}
	if classifyStatus(401, soft, hard) != failHard {
		t.Fatal("401 should be hard")
	}
	if classifyStatus(400, soft, hard) != failFatal {
		t.Fatal("400 should be fatal")
	}
}

func TestFirstNonEmptyStr(t *testing.T) {
	if FirstNonEmptyStr("", "hello", "world") != "hello" {
		t.Fatal("basic")
	}
	if FirstNonEmptyStr("  ", "hello") != "hello" {
		t.Fatal("whitespace skip")
	}
	if FirstNonEmptyStr() != "" {
		t.Fatal("empty")
	}
}

func TestMaskToken(t *testing.T) {
	long := maskToken("ccg_sk_very_long_secret_token_here")
	if !strings.HasSuffix(long, "...") {
		t.Fatalf("long token = %s", long)
	}
	short := maskToken("abc")
	if short != "abc***" {
		t.Fatalf("short token = %s", short)
	}
}

func TestIsSSEContentType(t *testing.T) {
	if !isSSEContentType("text/event-stream") {
		t.Fatal("sse")
	}
	if !isSSEContentType("text/event-stream; charset=utf-8") {
		t.Fatal("sse with charset")
	}
	if isSSEContentType("application/json") {
		t.Fatal("json should not be sse")
	}
}

func TestBuildPayloadAnthropicPassthrough(t *testing.T) {
	p := testProvider(t, "anthropic")
	body := []byte(`{"model":"old","messages":[],"max_tokens":100}`)
	out, path, err := buildPayload(p, "new-model", body, &core.MessagesRequest{MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" {
		t.Fatalf("path = %s", path)
	}
	if !strings.Contains(string(out), `"new-model"`) {
		t.Fatal("model not patched")
	}
}

func TestWriteAnthropicError(t *testing.T) {
	// Just ensure it doesn't panic
	w := &testResponseWriter{}
	writeAnthropicError(w, 400, "test", "msg")
}

func TestWriteOpenAIError(t *testing.T) {
	w := &testResponseWriter{}
	writeOpenAIError(w, 400, "test", "msg")
}

type testResponseWriter struct {
	headers map[string][]string
	body    []byte
	status  int
}

func (w *testResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(map[string][]string)
	}
	return w.headers
}
func (w *testResponseWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}
func (w *testResponseWriter) WriteHeader(code int) { w.status = code }

func newTestRequest() *http.Request {
	req, _ := http.NewRequest("POST", "/v1/messages", nil)
	return req
}

func newTestRequestWithHeader(k, v string) *http.Request {
	req := newTestRequest()
	req.Header.Set(k, v)
	return req
}

func parseOverridesWithHeaders(headers map[string]string) reqOverrides {
	req := newTestRequest()
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return parseOverrides(req)
}

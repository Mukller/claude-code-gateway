package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-code-gateway/internal/config"
)

func TestConfigYamlEditorFlow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	initial := "server:\n  listen: \":0\"\nauth:\n  tokens: [\"tok\"]\nproviders:\n  - name: p1\n    type: openai\n    base_url: \"https://x.test/v1\"\n    keys: [\"k\"]\nrouting:\n  default_chain: [p1]\nretry:\n  max_attempts: 6\n  retry_statuses: [429,500]\n  key_fail_statuses: [401,403]\nlogging:\n  file: \"" + filepath.ToSlash(filepath.Join(dir, "u.jsonl")) + "\"\n"
	os.WriteFile(cfgPath, []byte(initial), 0o600)

	fake := newFakeOpenAI(t, respondOpenAI("ok"))
	defer fake.Close()

	cfg := testConfig([]config.Provider{
		{Name: "p1", Type: "openai", BaseURL: fake.URL + "/v1", Keys: []string{"k"}},
	}, []string{"p1"})
	s, ts := buildTestServer(t, cfg)
	s.ConfigPath = cfgPath

	get := func() string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/config/yaml", nil)
		req.Header.Set("x-api-key", "tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(); !strings.Contains(got, "name: p1") {
		t.Fatalf("GET yaml wrong:\n%s", got)
	}

	post := func(content string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/config/yaml",
			strings.NewReader(`{"content":`+jsonMarshal(content)+`}`))
		req.Header.Set("x-api-key", "tok")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	code, respBody := post("this is : not: valid: yaml: [[[\n")
	if code != 400 || !strings.Contains(jsonMust(respBody), "not applied") {
		t.Fatalf("invalid yaml must be rejected: %d %v", code, respBody)
	}
	cur, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cur), "name: p1") {
		t.Fatal("file must stay unchanged after invalid save")
	}

	valid := strings.Replace(initial, "max_attempts: 6", "max_attempts: 3", 1)
	code, respBody = post(valid)
	if code != 200 {
		t.Fatalf("valid save failed: %d %v", code, jsonMust(respBody))
	}
	cur, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(cur), "max_attempts: 3") {
		t.Fatal("file not updated")
	}
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Fatal("backup file missing")
	}

	rb, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/config/rollback", nil)
	rb.Header.Set("x-api-key", "tok")
	resp, err := http.DefaultClient.Do(rb)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rollback failed: %d", resp.StatusCode)
	}
	cur, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(cur), "max_attempts: 6") {
		t.Fatalf("rollback did not restore: %s", cur)
	}
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Fatal("backup must survive rollback for re-rollback safety")
	}
}

func jsonMust(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func jsonMarshal(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

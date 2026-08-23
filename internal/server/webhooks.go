package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/logstore"
)

type webhookTarget struct {
	url     string
	secret  string
	events  map[string]bool
	timeout time.Duration
	client  *http.Client
}

func buildWebhookTargets(cfgs []config.Webhook) []webhookTarget {
	out := make([]webhookTarget, 0, len(cfgs))
	for _, c := range cfgs {
		t := webhookTarget{
			url:     c.URL,
			secret:  c.Secret,
			events:  map[string]bool{},
			timeout: c.Timeout,
			client:  &http.Client{Timeout: c.Timeout},
		}
		for _, e := range c.Events {
			t.events[e] = true
		}
		out = append(out, t)
	}
	return out
}

func (t webhookTarget) wants(event string) bool {
	if len(t.events) == 0 {
		return true
	}
	return t.events[event]
}

type hookPayload struct {
	Event        string    `json:"event"`
	Time         time.Time `json:"time"`
	Model        string    `json:"model,omitempty"`
	TargetModel  string    `json:"target_model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Status       int       `json:"status"`
	LatencyMs    int64     `json:"latency_ms,omitempty"`
	Cached       bool      `json:"cached,omitempty"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func eventFor(r logstore.Record) string {
	if r.Error != "" || r.Status >= 400 || r.Status == 0 {
		return "error"
	}
	return "usage"
}

func (s *Server) logRecord(r logstore.Record) {
	s.store.Add(r)
	s.notifyHooks(r)
}

func (s *Server) notifyHooks(r logstore.Record) {
	if len(s.hooks) == 0 {
		return
	}
	event := eventFor(r)
	payload := hookPayload{
		Event:        event,
		Time:         r.Time,
		Model:        r.Model,
		TargetModel:  r.TargetModel,
		Provider:     r.Provider,
		Status:       r.Status,
		LatencyMs:    r.LatencyMs,
		Cached:       r.Cached,
		InputTokens:  r.InTok,
		OutputTokens: r.OutTok,
		CostUSD:      r.CostUSD,
		Error:        r.Error,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	for _, t := range s.hooks {
		if !t.wants(event) {
			continue
		}
		go deliver(t, event, body)
	}
}

func deliver(t webhookTarget, event string, body []byte) {
	req, err := http.NewRequest(http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CCG-Event", event)
	req.Header.Set("User-Agent", "cc-gateway")
	if t.secret != "" {
		mac := hmac.New(sha256.New, []byte(t.secret))
		mac.Write(body)
		req.Header.Set("X-CCG-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := t.client.Do(req)
	if err != nil {
		log.Printf("[webhook] %s %s: %v", event, t.url, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[webhook] %s %s: status %d", event, t.url, resp.StatusCode)
	}
}

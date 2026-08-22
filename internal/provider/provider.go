package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-code-gateway/internal/config"
)

type Provider struct {
	cfg  config.Provider
	pool *Pool
	http *http.Client
}

func New(cfg config.Provider) *Provider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Provider{
		cfg:  cfg,
		pool: NewPool(cfg.Keys),
		http: &http.Client{Timeout: 0},
	}
}

func (p *Provider) Name() string           { return p.cfg.Name }
func (p *Provider) Type() string           { return p.cfg.Type }
func (p *Provider) Pool() *Pool            { return p.pool }
func (p *Provider) StaticModels() []string { return p.cfg.Models }
func (p *Provider) DiscoversModels() bool {
	return p.cfg.DiscoverModels && p.Type() == "openai" && p.Pool().Len() > 0
}

func joinURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return b + path
}

func (p *Provider) buildRequest(ctx context.Context, key, path string, body []byte, stream bool) (*http.Request, error) {
	url := joinURL(p.cfg.BaseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	switch p.cfg.Type {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", p.cfg.AnthropicVersion)
	case "anthropic-compat":
		if p.cfg.AuthStyle == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Set("x-api-key", key)
		}
		req.Header.Set("anthropic-version", p.cfg.AnthropicVersion)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range p.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

type ExecResult struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
	ErrMsg string
}

func (p *Provider) Execute(ctx context.Context, key, path string, payload []byte, stream bool) (*ExecResult, error) {
	req, err := p.buildRequest(ctx, key, path, payload, stream)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	res := &ExecResult{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		resp.Body.Close()
		res.ErrMsg = extractErrMsg(resp.StatusCode, data, p.cfg.Type)
	}
	return res, nil
}

func extractErrMsg(status int, data []byte, ptype string) string {
	var msg string
	var ae struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	var oe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &ae) == nil && ae.Error.Message != "" {
		msg = fmt.Sprintf("%s: %s", FirstNonEmpty(ae.Error.Type, "api_error"), ae.Error.Message)
	} else if json.Unmarshal(data, &oe) == nil && oe.Error.Message != "" {
		msg = oe.Error.Message
	} else {
		s := strings.TrimSpace(string(data))
		if s == "" {
			s = http.StatusText(status)
		}
		msg = s
	}
	return msg
}

func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *Provider) FetchModels(ctx context.Context) ([]string, error) {
	url := joinURL(p.cfg.BaseURL, "/v1/models")
	key, ok := p.Pool().Pick()
	if !ok {
		return nil, fmt.Errorf("no keys")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var ml struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&ml); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range ml.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m.ID)
		}
	}
	sortStrings(out)
	return out, nil
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

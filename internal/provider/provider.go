package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"claude-code-gateway/internal/config"
)

type Provider struct {
	cfg    config.Provider
	pool   *Pool
	http   *http.Client
	tfs    []TransformFunc
	saOnce sync.Once
	saSrc  *SATokenSource
	saErr  error

	probeMu sync.Mutex
	probe   ProbeInfo

	consecFails   int32
	openUntilUnix int64
	inflight      int32
	lbMu          sync.Mutex
	latEMA        float64
}

func (p *Provider) BeginRequest() { atomic.AddInt32(&p.inflight, 1) }

func (p *Provider) EndRequest(latencyMs int64) {
	atomic.AddInt32(&p.inflight, -1)
	p.lbMu.Lock()
	p.latEMA = p.latEMA*0.7 + float64(latencyMs)*0.3
	p.lbMu.Unlock()
}

func (p *Provider) Inflight() int32 { return atomic.LoadInt32(&p.inflight) }
func (p *Provider) Latency() float64 {
	p.lbMu.Lock()
	defer p.lbMu.Unlock()
	return p.latEMA
}

func New(cfg config.Provider) *Provider {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	switch cfg.Type {
	case "bedrock":
		if cfg.Region == "" {
			cfg.Region = "us-east-1"
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", cfg.Region)
		}
		if cfg.AnthropicVersion == "" || cfg.AnthropicVersion == "2023-06-01" {
			cfg.AnthropicVersion = "bedrock-2023-05-31"
		}
	case "vertex":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://aiplatform.googleapis.com/v1"
		}
		if cfg.AuthStyle == "" {
			cfg.AuthStyle = "api-key"
		}
		if cfg.AnthropicVersion == "" {
			cfg.AnthropicVersion = "2023-06-01"
		}
	default:
		if cfg.AnthropicVersion == "" {
			cfg.AnthropicVersion = "2023-06-01"
		}
	}
	tfs, err := ParseTransforms(cfg.Transformers)
	if err != nil {
		log.Printf("[provider %q] transformers: %v", cfg.Name, err)
	}
	p := &Provider{
		cfg:  cfg,
		pool: NewPool(cfg.Keys),
		http: &http.Client{Timeout: 0},
		tfs:  tfs,
	}
	p.pool.SetWeight(cfg.Weight)
	return p
}

const (
	breakerThreshold = 5
	breakerOpenFor   = 2 * time.Minute
)

func (p *Provider) NoteUpstream(ok bool) {
	if ok {
		atomic.StoreInt32(&p.consecFails, 0)
		return
	}
	n := atomic.AddInt32(&p.consecFails, 1)
	if n >= breakerThreshold {
		atomic.StoreInt64(&p.openUntilUnix, time.Now().Add(breakerOpenFor).Unix())
	}
}

func (p *Provider) IsOpen(now time.Time) bool {
	u := atomic.LoadInt64(&p.openUntilUnix)
	return u > 0 && now.Unix() < u
}

func (p *Provider) OpenForSeconds() int64 {
	u := atomic.LoadInt64(&p.openUntilUnix)
	if u == 0 {
		return 0
	}
	d := u - time.Now().Unix()
	if d < 0 {
		return 0
	}
	return d
}

type ProbeInfo struct {
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

func (p *Provider) SetProbe(pi ProbeInfo) {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	p.probe = pi
}

func (p *Provider) Probe() ProbeInfo {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	return p.probe
}

func (p *Provider) Transforms() []TransformFunc { return p.tfs }
func (p *Provider) Weight() int                 { return p.cfg.Weight }
func (p *Provider) Name() string                { return p.cfg.Name }
func (p *Provider) Type() string                { return p.cfg.Type }
func (p *Provider) Pool() *Pool                 { return p.pool }
func (p *Provider) StaticModels() []string      { return p.cfg.Models }
func (p *Provider) SendStreamOptions() bool     { return p.cfg.SendStreamOptions }
func (p *Provider) AnthropicVersion() string    { return p.cfg.AnthropicVersion }
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
	switch p.cfg.Type {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", p.cfg.AnthropicVersion)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
	case "anthropic-compat":
		if p.cfg.AuthStyle == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Set("x-api-key", key)
		}
		req.Header.Set("anthropic-version", p.cfg.AnthropicVersion)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
	case "bedrock":
		creds, cerr := parseAWSKey(key)
		if cerr != nil {
			return nil, cerr
		}
		req.Header.Set("Accept", "application/json")
		signAWSRequest(req, body, creds, p.cfg.Region, "bedrock", time.Now())
	case "vertex":
		if p.cfg.AuthStyle == "bearer" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else if p.cfg.AuthStyle == "sa" {
			src, serr := p.ensureSA()
			if serr != nil {
				return nil, serr
			}
			tok, terr := src.Token(ctx)
			if terr != nil {
				return nil, terr
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		} else {
			q := req.URL.Query()
			q.Set("key", key)
			req.URL.RawQuery = q.Encode()
		}
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
	default:
		req.Header.Set("Authorization", "Bearer "+key)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
	}
	for k, v := range p.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (p *Provider) ensureSA() (*SATokenSource, error) {
	p.saOnce.Do(func() {
		raw := strings.TrimSpace(p.cfg.ServiceAccountJSON)
		if raw == "" {
			p.saErr = fmt.Errorf("vertex auth_style=sa: service_account_json is empty")
			return
		}
		var data []byte
		if strings.HasPrefix(raw, "{") {
			data = []byte(raw)
		} else {
			b, ferr := os.ReadFile(raw)
			if ferr != nil {
				p.saErr = fmt.Errorf("read service account file: %w", ferr)
				return
			}
			data = b
		}
		src, gerr := NewSATokenSource(data)
		if gerr != nil {
			p.saErr = gerr
			return
		}
		p.saSrc = src
	})
	return p.saSrc, p.saErr
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
	var aws struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &ae) == nil && ae.Error.Message != "" {
		msg = fmt.Sprintf("%s: %s", FirstNonEmpty(ae.Error.Type, "api_error"), ae.Error.Message)
	} else if json.Unmarshal(data, &oe) == nil && oe.Error.Message != "" {
		msg = oe.Error.Message
	} else if json.Unmarshal(data, &aws) == nil && aws.Message != "" {
		msg = aws.Message
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

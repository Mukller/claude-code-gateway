package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type AntigravityConfig struct {
	AccessToken  string
	RefreshToken string
	ProjectID    string
	Endpoint     string
	ExpiresAt    time.Time
}

const (
	agEndpointDaily  = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	agEndpointProd   = "https://cloudcode-pa.googleapis.com"
	agClientID       = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	agClientSecret   = "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"
	agTokenURL       = "https://oauth2.googleapis.com/token"
	agUserAgent      = "antigravity/1.11.5 windows/amd64"
	agAPIClient      = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	agClientMetadata = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

type AntigravityClient struct {
	mu           sync.Mutex
	accessToken  string
	refreshToken string
	projectID    string
	http         *http.Client
	endpoint     string
	expiresAt    time.Time
}

func NewAntigravityClient(accessToken, refreshToken, projectID string) *AntigravityClient {
	return &AntigravityClient{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		projectID:    projectID,
		http:         &http.Client{Timeout: 5 * time.Minute},
		endpoint:     agEndpointDaily,
	}
}

func (a *AntigravityClient) SetTokens(access, refresh, projectID string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accessToken = access
	a.refreshToken = refresh
	a.projectID = projectID
	a.expiresAt = expiresAt
}

func (a *AntigravityClient) ensureToken() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.expiresAt.Add(-5 * time.Minute)) {
		return nil
	}
	if a.refreshToken == "" {
		return fmt.Errorf("no refresh token")
	}
	data := fmt.Sprintf("grant_type=refresh_token&refresh_token=%s&client_id=%s&client_secret=%s",
		a.refreshToken, agClientID, agClientSecret)
	req, err := http.NewRequest("POST", agTokenURL, strings.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil || tr.AccessToken == "" {
		return fmt.Errorf("token refresh failed")
	}
	a.accessToken = tr.AccessToken
	a.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return nil
}

func (a *AntigravityClient) ChatCompletion(ctx context.Context, model string, messages json.RawMessage, maxTokens int) (map[string]any, error) {
	if err := a.ensureToken(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	token := a.accessToken
	projectID := a.projectID
	endpoint := a.endpoint
	a.mu.Unlock()

	body := map[string]any{
		"model":      model,
		"messages":   json.RawMessage(messages),
		"max_tokens": maxTokens,
		"project_id": projectID,
	}
	payload, _ := json.Marshal(body)

	var lastErr error
	_ = endpoint
	for _, ep := range []string{agEndpointDaily, agEndpointProd} {
		req, err := http.NewRequestWithContext(ctx, "POST", ep+"/v1internal:chatCompletions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", agUserAgent)
		req.Header.Set("X-Goog-Api-Client", agAPIClient)
		req.Header.Set("Client-Metadata", agClientMetadata)

		resp, err := a.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			var result map[string]any
			if json.Unmarshal(data, &result) == nil {
				return result, nil
			}
		}
		lastErr = fmt.Errorf("antigravity %s: status %d: %s", ep, resp.StatusCode, strings.TrimSpace(string(data[:agMin(len(data), 300)])))
	}
	return nil, lastErr
}

func agMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/core"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/pricing"
	"claude-code-gateway/internal/provider"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	catalog := s.reg.Catalog()
	type m struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	}
	data := make([]m, 0, len(catalog))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range catalog {
		data = append(data, m{Type: "model", ID: e.ID, DisplayName: e.ID, CreatedAt: now})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false})
}

func (s *Server) handleAdminTokensUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req struct {
		Name      string  `json:"name"`
		BudgetUSD float64 `json:"budget_usd"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "body must be {\"name\":..., \"budget_usd\":...}")
		return
	}
	if !s.budgets.updateLimitByName(req.Name, req.BudgetUSD) {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "client not found: "+req.Name)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": req.Name, "budget_usd": req.BudgetUSD,
		"note": "runtime-only until config reload/restart",
	})
}

func (s *Server) handleAdminReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	if s.ConfigPath == "" {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "config path unknown")
		return
	}
	fresh, err := config.Load(s.ConfigPath)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "reload failed: "+err.Error())
		return
	}
	if err := s.reg.Swap(&fresh.Routing, fresh.Providers); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	tbl := pricing.New(fresh.Pricing)
	s.prices.Store(&tbl)
	writeJSON(w, http.StatusOK, map[string]any{
		"reloaded":         []string{"providers", "routing", "pricing"},
		"restart_required": []string{"listen", "auth.tokens", "cache", "webhooks"},
	})
}

func (s *Server) handleConfigYaml(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleConfigYamlGet(r)
	case http.MethodPost:
		s.handleConfigYamlSet(r)
	default:
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use GET or POST")
	}
}

func (s *Server) handleConfigYamlGet(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleConfigYamlSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "body must be {\"content\": \"<yaml>\"}")
		return
	}

	tmp, err := os.CreateTemp("", "ccg-cfg-*.yaml")
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(req.Content); err != nil {
		tmp.Close()
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	tmp.Close()

	if _, err := config.Load(tmpPath); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "config invalid, not applied: "+err.Error())
		return
	}

	bakPath := s.ConfigPath + ".bak"
	if data, err := os.ReadFile(s.ConfigPath); err == nil {
		os.WriteFile(s.ConfigPath+".bak", data, 0o600)
	}
	if err := os.Rename(s.ConfigPath, s.ConfigPath+".bak"); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "backup failed: "+err.Error())
		return
	}
	if err := os.Rename(tmpPath, s.ConfigPath); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "write failed: "+err.Error())
		return
	}
	os.Chmod(s.ConfigPath, 0o600)

	res, err := s.reloadConfig()
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "backup": s.ConfigPath + ".bak", "applied": res,
	})
}

func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	bakPath := s.ConfigPath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "no backup available")
		return
	}
	cur, _ := os.ReadFile(s.ConfigPath)
	bak, err := os.ReadFile(s.ConfigPath + ".bak")
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	tmp, err := os.CreateTemp("", "ccg-rollback-*.yaml")
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	tmp.Write(bak)
	tmp.Close()

	if _, err := config.Load(tmp.Name()); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "backup itself is invalid: "+err.Error())
		return
	}
	os.WriteFile(s.ConfigPath, cur, 0o600)
	os.WriteFile(s.ConfigPath+".bak", bak, 0o600)

	res, rerr := s.reloadConfig()
	if rerr != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", rerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolled_back": true, "applied": res})
}
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"claude-code-gateway/internal/core"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/provider"
)

func (s *Server) handleTokenGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req struct {
		Name          string   `json:"name"`
		BudgetUSD     float64  `json:"budget_usd"`
		BudgetPeriod  string   `json:"budget_period"`
		AllowedModels []string `json:"allowed_models"`
		TPM           int64    `json:"tpm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", `body: {"name":"...", "budget_usd":0, ...}`)
		return
	}
	for _, c := range s.cfg.Clients {
		if strings.EqualFold(c.Name, req.Name) {
			writeAnthropicError(w, http.StatusConflict, "invalid_request_error", "client with this name already exists in config")
			return
		}
	}
	token := "ccg_sk_" + core.RandHex(24)
	s.tokensMu.Lock()
	s.tokens[token] = true
	s.tokensMu.Unlock()
	s.budgets.addClientRuntime(req.Name, token, req.BudgetUSD, req.BudgetPeriod, req.AllowedModels, req.TPM)

	writeJSON(w, http.StatusOK, map[string]any{
		"name":   req.Name,
		"token":  token,
		"budget": req.BudgetUSD,
		"period": req.BudgetPeriod,
		"note":   "runtime-only: add to config.yaml clients: section to persist",
	})
}

func (s *Server) handleWSMessages(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		writeAnthropicError(w, http.StatusUpgradeRequired, "invalid_request_error", "websocket upgrade required")
		return
	}
	// For now, WebSocket streaming is a pass-through to SSE.
	// Full bidirectional WS support requires a WS client library.
	writeAnthropicError(w, http.StatusNotImplemented, "invalid_request_error",
		"WebSocket streaming coming soon; use POST /v1/messages with stream=true for SSE")
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": Version})
}

func (s *Server) handleDebugPPROF(w http.ResponseWriter, r *http.Request) {
	// This is a placeholder — real pprof requires importing net/http/pprof
	// which adds ~2MB to the binary. Enable via build tag or separate binary.
	writeAnthropicError(w, http.StatusNotImplemented, "api_error",
		"pprof not compiled in; build with -tags pprof")
}

func (s *Server) handleRequestLog(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot()
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	model := r.URL.Query().Get("model")
	providerName := r.URL.Query().Get("provider")
	status := 0
	fmt.Sscanf(r.URL.Query().Get("status"), "%d", &status)

	var filtered []logstore.Record
	for i := len(v.Recent) - 1; i >= 0; i-- {
		rec := v.Recent[i]
		if model != "" && !strings.Contains(rec.Model, model) {
			continue
		}
		if providerName != "" && !strings.Contains(rec.Provider, providerName) {
			continue
		}
		if status > 0 && rec.Status != status {
			continue
		}
		filtered = append(filtered, rec)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": filtered, "total_matched": len(filtered)})
}

func (s *Server) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": s.cacheSize(),
		"enabled": s.cache != nil,
	})
}

var _ = provider.Success

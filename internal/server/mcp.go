package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/mcp"
	"claude-code-gateway/internal/pricing"
)

func (s *Server) reloadConfig() (any, error) {
	if s.ConfigPath == "" {
		return nil, fmt.Errorf("config path unknown")
	}
	fresh, err := config.Load(s.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("reload failed: %w", err)
	}
	if err := s.reg.Swap(&fresh.Routing, fresh.Providers); err != nil {
		return nil, err
	}
	tbl := pricing.New(fresh.Pricing)
	s.prices.Store(&tbl)
	return map[string]any{
		"reloaded":         []string{"providers", "routing", "pricing"},
		"restart_required": []string{"listen", "auth.tokens", "cache", "webhooks"},
	}, nil
}

func (s *Server) handleAdminReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	res, err := s.reloadConfig()
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if !s.isAdminToken(token) && !s.cfg.Auth.AllowAnon {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	resp, notification := s.mcp.Dispatch(body)
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) buildMCP() *mcp.Handler {
	return &mcp.Handler{
		ServerInfo: mcp.ServerInfo{Name: "claude-code-gateway", Version: Version},
		Stats:      func() any { return s.store.Snapshot() },
		Logs: func(limit int) any {
			v := s.store.Snapshot()
			if limit > 0 && limit < len(v.Recent) {
				v.Recent = v.Recent[len(v.Recent)-limit:]
			}
			return v.Recent
		},
		Models: func() any { return s.reg.Catalog() },
		Tokens: func() any { return map[string]any{"tokens": s.tokenReports(time.Now())} },
		Providers: func() any {
			out := []map[string]any{}
			for _, p := range s.reg.Providers() {
				out = append(out, map[string]any{"name": p.Name(), "type": p.Type(), "keys": p.Pool().Len()})
			}
			return out
		},
		Reload: s.reloadConfig,
		Cost: func(model string, inTok, outTok int64) any {
			cost := s.costFor(model, inTok, outTok, 0, 0)
			return map[string]any{"model": model, "input_tokens": inTok, "output_tokens": outTok, "cost_usd": cost}
		},
	}
}

func (s *Server) ServeMCPStdio(r io.Reader, w io.Writer) error {
	return mcp.ServeStream(r, w, s.mcp)
}

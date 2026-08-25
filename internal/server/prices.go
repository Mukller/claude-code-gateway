package server

import (
	"encoding/json"
	"net/http"

	"claude-code-gateway/internal/pricing"
)

func (s *Server) handleAdminPrices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetPrices(w, r)
	case http.MethodPost:
		s.handleSetPrice(w, r)
	default:
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use GET or POST")
	}
}

func (s *Server) handleGetPrices(w http.ResponseWriter, r *http.Request) {
	table := s.priceTable()
	prices := table.MatchAll()
	var autoCount int
	if ap := s.autoPrices.Load(); ap != nil {
		autoCount = len(ap.MatchAll())
	}
	s.priceOverridesMu.Lock()
	overrides := make(map[string]pricing.Price, len(s.priceOverrides))
	for k, v := range s.priceOverrides {
		overrides[k] = v
	}
	s.priceOverridesMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"prices":       prices,
		"overrides":    overrides,
		"auto_synced":  autoCount,
		"static_rules": len(s.cfg.Pricing),
	})
}

func (s *Server) handleSetPrice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model         string  `json:"model"`
		InputPerMTok  float64 `json:"input_per_mtok"`
		OutputPerMTok float64 `json:"output_per_mtok"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			`body: {"model":"...", "input_per_mtok":3, "output_per_mtok":15}`)
		return
	}
	p := pricing.Price{InputPerMTok: req.InputPerMTok, OutputPerMTok: req.OutputPerMTok}
	s.priceOverridesMu.Lock()
	s.priceOverrides[req.Model] = p
	s.priceOverridesMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"updated": req.Model, "price": p})
}

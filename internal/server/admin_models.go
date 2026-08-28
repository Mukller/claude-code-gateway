package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"claude-code-gateway/internal/provider"
)

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Provider == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", `body: {"provider":"name"}`)
		return
	}
	p := s.reg.Provider(req.Provider)
	if p == nil {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "provider not found: "+req.Provider)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	start := time.Now()
	result := map[string]any{
		"provider": p.Name(),
		"type":     p.Type(),
		"weight":   p.Weight(),
		"keys":     p.Pool().Len(),
		"circuit":  p.OpenForSeconds(),
	}
	if p.Type() == "openai" || p.Type() == "antigravity" {
		models, err := p.FetchModels(ctx)
		result["probe_latency_ms"] = time.Since(start).Milliseconds()
		if err != nil {
			result["status"] = "fail"
			result["error"] = err.Error()
		} else {
			result["status"] = "ok"
			result["models_count"] = len(models)
			if len(models) > 5 {
				result["models_sample"] = models[:5]
			} else {
				result["models_sample"] = models
			}
		}
	} else {
		result["status"] = "skip"
		result["note"] = "probe only for openai/antigravity types"
	}
	pi := p.Probe()
	if !pi.At.IsZero() {
		result["last_probe"] = map[string]any{"ok": pi.OK, "latency_ms": pi.LatencyMs, "error": pi.Error, "at": pi.At}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleModelsDetailed(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkAuth(r); !ok {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
		return
	}
	catalog := s.reg.Catalog()
	table := s.priceTable()
	type modelInfo struct {
		ID            string  `json:"id"`
		Provider      string  `json:"provider"`
		InputPerMTok  float64 `json:"input_per_mtok"`
		OutputPerMTok float64 `json:"output_per_mtok"`
		HasPricing    bool    `json:"has_pricing"`
	}
	data := make([]modelInfo, 0, len(catalog))
	for _, e := range catalog {
		mi := modelInfo{ID: e.ID, Provider: e.Provider}
		if p, ok := table.Match(e.ID); ok {
			mi.InputPerMTok = p.InputPerMTok
			mi.OutputPerMTok = p.OutputPerMTok
			mi.HasPricing = true
		}
		data = append(data, mi)
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"models": data, "count": len(data)})
}

var _ = provider.Success

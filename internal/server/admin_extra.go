package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"claude-code-gateway/internal/logstore"
)

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	path := s.cfg.Logging.File
	f, err := os.Open(path)
	if err != nil {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "usage log is empty or missing")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ccg-usage.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"time", "client", "request_id", "model", "target_model", "provider",
		"status", "latency_ms", "ttft_ms", "stream", "cached", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "cost_usd", "error"})

	dec := json.NewDecoder(f)
	for {
		var rec logstore.Record
		if err := dec.Decode(&rec); err != nil {
			break
		}
		cw.Write([]string{
			rec.Time.Format(time.RFC3339), rec.Token, rec.RequestID,
			rec.Model, rec.TargetModel, rec.Provider,
			fmt.Sprintf("%d", rec.Status), fmt.Sprintf("%d", rec.LatencyMs), fmt.Sprintf("%d", rec.TTFTMs),
			fmt.Sprintf("%v", rec.Stream), fmt.Sprintf("%v", rec.Cached),
			fmt.Sprintf("%d", rec.InTok), fmt.Sprintf("%d", rec.OutTok),
			fmt.Sprintf("%d", rec.CacheRead), fmt.Sprintf("%d", rec.CacheWrite),
			fmt.Sprintf("%.4f", rec.CostUSD), rec.Error,
		})
	}
	cw.Flush()
}

func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, p := range s.reg.Providers() {
		entry := map[string]any{
			"name":           p.Name(),
			"type":           p.Type(),
			"weight":         p.Weight(),
			"circuit_open_s": p.OpenForSeconds(),
			"keys":           p.Pool().Stats(),
		}
		if pi := p.Probe(); !pi.At.IsZero() {
			entry["probe"] = pi
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

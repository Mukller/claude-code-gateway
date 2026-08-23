package server

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/pricing"
)

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

func (s *Server) isAdminToken(token string) bool {
	return (s.cfg.Auth.AdminToken != "" && token == s.cfg.Auth.AdminToken) || s.tokens[token]
}

func promEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot()
	var b strings.Builder
	wr := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
	}
	wr("# TYPE ccg_uptime_seconds gauge\nccg_uptime_seconds %.0f\n", v.UptimeSeconds)
	wr("# TYPE ccg_cache_entries gauge\nccg_cache_entries %d\n", s.cacheSize())

	mkStat := func(name, help, typ string) {
		wr("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	statLine := func(name string, st struct {
		Requests int64   `json:"requests"`
		Errors   int64   `json:"errors"`
		InTok    int64   `json:"input_tokens"`
		OutTok   int64   `json:"output_tokens"`
		CostUSD  float64 `json:"cost_usd"`
	}, label string) {
		wr("%s_requests_total{%s} %d\n", name, label, st.Requests)
		wr("%s_errors_total{%s} %d\n", name, label, st.Errors)
		wr("%s_input_tokens_total{%s} %d\n", name, label, st.InTok)
		wr("%s_output_tokens_total{%s} %d\n", name, label, st.OutTok)
		wr("%s_cost_usd_total{%s} %.4f\n", name, label, st.CostUSD)
	}

	mkStat("ccg_total_requests", "Total requests.", "counter")
	statLine("ccg_total", v.Total, `scope="all"`)

	mkStat("ccg_provider_requests", "Requests per provider.", "counter")
	provs := make([]string, 0, len(v.ByProvider))
	for k := range v.ByProvider {
		provs = append(provs, k)
	}
	sort.Strings(provs)
	for _, p := range provs {
		statLine("ccg_provider", v.ByProvider[p], fmt.Sprintf(`provider=%q`, promEscape(p)))
	}

	mkStat("ccg_model_requests", "Requests per model.", "counter")
	models := make([]string, 0, len(v.ByModel))
	for k := range v.ByModel {
		models = append(models, k)
	}
	sort.Strings(models)
	for _, m := range models {
		st := v.ByModel[m]
		wr("ccg_model_requests_total{model=%q} %d\n", promEscape(m), st.Requests)
		wr("ccg_model_cost_usd_total{model=%q} %.4f\n", promEscape(m), st.CostUSD)
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	wr("# TYPE ccg_go_goroutines gauge\nccg_go_goroutines %d\n", runtime.NumGoroutine())
	wr("# TYPE ccg_go_heap_bytes gauge\nccg_go_heap_bytes %d\n", ms.HeapAlloc)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

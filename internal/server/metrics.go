package server

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
)

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

	buckets := []float64{50, 100, 250, 500, 1000, 2500, 5000}
	counts := make([]int64, len(buckets)+1)
	var total int64
	for _, rec := range v.Recent {
		total++
		placed := false
		for i, b := range buckets {
			if float64(rec.LatencyMs) <= b {
				counts[i]++
				placed = true
				break
			}
		}
		if !placed {
			counts[len(buckets)]++
		}
	}
	wr("# TYPE ccg_latency_ms_bucket histogram\n")
	cum := int64(0)
	for i, b := range buckets {
		cum += counts[i]
		wr("ccg_latency_ms_bucket{le=%q} %d\n", fmt.Sprintf("%.0f", b), cum)
	}
	wr("ccg_latency_ms_bucket{le=\"+Inf\"} %d\n", total)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

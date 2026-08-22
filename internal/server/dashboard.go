package server

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML []byte

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "unknown path")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":   "claude-code-gateway",
		"endpoints": []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/models", "/healthz", "/admin/dashboard", "/admin/stats", "/admin/logs"},
		"dashboard": "/admin/dashboard",
	})
}

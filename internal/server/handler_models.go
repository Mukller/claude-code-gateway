package server

import (
	"net/http"
	"time"
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

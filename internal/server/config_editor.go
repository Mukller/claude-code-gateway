package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"claude-code-gateway/internal/config"
)

func (s *Server) handleConfigYaml(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleConfigYamlGet(w, r)
	case http.MethodPost:
		s.handleConfigYamlSet(w, r)
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
		os.WriteFile(tmpPath+".err.txt", []byte(err.Error()), 0o644)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "config invalid, not applied: "+err.Error())
		return
	}

	bakPath := s.ConfigPath + ".bak"
	if data, err := os.ReadFile(s.ConfigPath); err == nil {
		os.WriteFile(bakPath, data, 0o600)
	}

	prevPerm := os.FileMode(0o600)
	if fi, err := os.Stat(s.ConfigPath); err == nil {
		prevPerm = fi.Mode().Perm()
	}
	if err := os.Rename(tmpPath, s.ConfigPath); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "write failed: "+err.Error())
		return
	}
	os.Chmod(s.ConfigPath, prevPerm)

	res, rerr := s.reloadConfig()
	if rerr != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			"saved but reload failed ("+rerr.Error()+"); use rollback endpoint")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": true, "backup": bakPath, "applied": res,
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
	bak, err := os.ReadFile(bakPath)
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

	if _, err := config.Load(tmpPath); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "backup itself is invalid: "+err.Error())
		return
	}
	os.WriteFile(s.ConfigPath, bak, prevPermOf(s.ConfigPath))
	os.WriteFile(bakPath, cur, 0o600)

	res, rerr := s.reloadConfig()
	if rerr != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", rerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolled_back": true, "applied": res})
}

func prevPermOf(p string) os.FileMode {
	if fi, err := os.Stat(p); err == nil {
		return fi.Mode().Perm()
	}
	return 0o600
}

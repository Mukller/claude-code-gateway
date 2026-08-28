package server

import "strings"

// enforceFreeOnly checks if the gateway is configured to only serve free models.
// When enabled, the requested model must be in the free_models list (or match
// a wildcard pattern like "openrouter/*").
func (s *Server) enforceFreeOnly(model string) string {
	if !s.cfg.Routing.FreeOnly {
		return ""
	}
	if len(s.cfg.Routing.FreeModels) == 0 {
		return "free_only is enabled but no free_models are configured"
	}
	lm := strings.ToLower(model)
	for _, pattern := range s.cfg.Routing.FreeModels {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if ok, _ := matchWildcard(p, lm); ok {
			return ""
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(lm, prefix) {
				return ""
			}
		}
		if p == lm {
			return ""
		}
	}
	return "model " + model + " is not in free_models list (free_only mode is on)"
}

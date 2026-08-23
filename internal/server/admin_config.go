package server

import (
	"net/http"
)

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg
	provs := []map[string]any{}
	for _, p := range cfg.Providers {
		n := 0
		for _, k := range p.Keys {
			if k != "" {
				n++
			}
		}
		provs = append(provs, map[string]any{
			"name": p.Name, "type": p.Type, "base_url": p.BaseURL,
			"keys": n, "weight": p.Weight, "transformers": p.Transformers,
			"discover_models": p.DiscoverModels, "probe_interval": p.ProbeInterval.String(),
		})
	}
	rules := []map[string]any{}
	for _, ru := range cfg.Routing.Rules {
		rules = append(rules, map[string]any{
			"prefix": ru.Prefix, "strip_prefix": ru.StripPrefix,
			"chain": ru.Chain, "targets": ru.Targets, "load_balance": ru.LoadBalance,
		})
	}
	clients := map[string]any{}
	s.budgets.mu.Lock()
	for _, ci := range s.budgets.info {
		clients[ci.Name] = map[string]any{
			"budget_usd": ci.Limit, "period": ci.Period,
			"allowed_models": ci.AllowedModels, "tpm": ci.TPM,
			"runtime_changed": s.budgets.runtimeChanged,
		}
	}
	s.budgets.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"listen":        cfg.Server.Listen,
		"providers":     provs,
		"default_chain": cfg.Routing.DefaultChain,
		"rules":         rules,
		"scenarios":     cfg.Routing.Scenarios,
		"cache":         cfg.Cache,
		"guardrails":    cfg.Guardrails,
		"clients":       clients,
		"note":          "secrets are never returned; edit config.yaml + POST /admin/reload to apply file changes",
	})
}

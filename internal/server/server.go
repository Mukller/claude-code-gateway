package server

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"claude-code-gateway/internal/cache"
	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/mcp"
	"claude-code-gateway/internal/pricing"
	"claude-code-gateway/internal/provider"
	"claude-code-gateway/internal/ratelimit"
	"claude-code-gateway/internal/state"
)

var Version = "dev"

type Server struct {
	cfg      *config.Config
	reg      *provider.Registry
	store    *logstore.Store
	prices   atomic.Pointer[pricing.Table]
	limiter  *ratelimit.Limiter
	cache    *cache.Cache
	fuzzy    *cache.SemanticIndex
	embedder *Embedder
	hooks    []webhookTarget
	budgets  *budgets
	rails    *guardrails
	mcp      *mcp.Handler

	storeBackend state.Store
	cacheTTL     time.Duration
	autoPrices   atomic.Pointer[pricing.Table]

	started    time.Time
	tokens     map[string]bool
	ConfigPath string
}

func New(cfg *config.Config, reg *provider.Registry, store *logstore.Store, prices pricing.Table) *Server {
	s := &Server{
		cfg:     cfg,
		reg:     reg,
		store:   store,
		limiter: ratelimit.New(cfg.RateLimit),
		started: time.Now(),
		tokens:  map[string]bool{},
	}
	s.prices.Store(&prices)
	if cfg.Cache.Enabled {
		s.cache = cache.New(cfg.Cache.TTL, cfg.Cache.MaxEntries)
		if cfg.Cache.Semantic.Enabled && cfg.Cache.Semantic.Endpoint != "" {
			s.fuzzy = cache.NewSemantic(cfg.Cache.Semantic.Threshold, cfg.Cache.MaxEntries)
			s.embedder = newEmbedder(cfg.Cache.Semantic)
		}
	}
	s.hooks = buildWebhookTargets(cfg.Webhooks)
	s.rails = compileGuardrails(cfg.Guardrails)
	for _, t := range cfg.Auth.Tokens {
		if t != "" {
			s.tokens[t] = true
		}
	}
	for _, c := range cfg.Clients {
		if c.Token != "" {
			s.tokens[c.Token] = true
		}
	}
	s.budgets = newBudgets(cfg.Clients)
	s.mcp = s.buildMCP()
	s.cacheTTL = cfg.Cache.TTL
	s.initStateStore()
	return s
}

func (s *Server) priceTable() pricing.Table {
	return *s.prices.Load()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("/metrics", s.requireAuth(s.handleMetrics))
	mux.HandleFunc("/admin/dashboard", s.handleDashboard)
	mux.HandleFunc("/mcp", s.requireAuth(s.handleMCP))
	mux.HandleFunc("/admin/stats", s.requireAuth(s.handleAdminStats))
	mux.HandleFunc("/admin/logs", s.requireAuth(s.handleAdminLogs))
	mux.HandleFunc("/admin/tokens", s.requireAuth(s.handleAdminTokens))
	mux.HandleFunc("/admin/tokens/update", s.requireAuth(s.handleAdminTokensUpdate))
	mux.HandleFunc("/admin/config", s.requireAuth(s.handleAdminConfig))
	mux.HandleFunc("/admin/keys", s.requireAuth(s.handleAdminKeys))
	mux.HandleFunc("/admin/export.csv", s.requireAuth(s.handleExportCSV))
	mux.HandleFunc("/admin/reload", s.requireAuth(s.handleAdminReload))
	mux.HandleFunc("/admin/config/yaml", s.requireAuth(s.handleConfigYaml))
	mux.HandleFunc("/admin/config/rollback", s.requireAuth(s.handleConfigRollback))
	mux.HandleFunc("/admin/flush-cache", s.requireAuth(s.handleFlushCache))
	return s.withRecovery(s.withCORS(s.withAccessLog(mux)))
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	allowed := s.cfg.Server.CORSOrigins
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		for _, a := range allowed {
			if a == "*" || a == origin {
				w.Header().Set("Access-Control-Allow-Origin", a)
				w.Header().Set("Access-Control-Allow-Headers", "x-api-key, authorization, content-type, anthropic-version, anthropic-beta")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				break
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.status == 0 {
		sw.status = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r)
		st := sw.status
		if st == 0 {
			st = 200
		}
		if s.cfg.Logging.JSONFormat {
			b, _ := json.Marshal(map[string]any{
				"ts": start.UTC().Format(time.RFC3339Nano), "method": r.Method,
				"path": r.URL.Path, "status": st, "ms": time.Since(start).Milliseconds(),
			})
			log.Println(string(b))
			return
		}
		log.Printf("%s %s -> %d (%dms)", r.Method, r.URL.Path, st, time.Since(start).Milliseconds())
	})
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic in %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				writeAnthropicError(w, http.StatusInternalServerError, "api_error", "internal gateway error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		ok := false
		if _, found := s.tokens[token]; found && token != "" {
			ok = true
		}
		if !ok && s.cfg.Auth.AdminToken != "" && token == s.cfg.Auth.AdminToken {
			ok = true
		}
		if !ok && s.cfg.Auth.AllowAnon {
			ok = true
		}
		if !ok {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API token")
			return
		}
		next(w, r)
	}
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("x-api-key"); h != "" {
		return h
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && (h[:7] == "Bearer " || h[:7] == "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) checkAuth(r *http.Request) (string, bool) {
	token := extractToken(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" && s.cfg.Auth.AllowAnon {
		return "anon", true
	}
	if _, ok := s.tokens[token]; ok {
		return token, true
	}
	return "", false
}

func maskToken(t string) string {
	if len(t) <= 10 {
		return t[:min(len(t), 3)] + "***"
	}
	return t[:10] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAnthropicError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": msg,
		},
	})
}

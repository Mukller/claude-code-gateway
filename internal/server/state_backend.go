package server

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"claude-code-gateway/internal/cache"
	"claude-code-gateway/internal/state"
)

func (s *Server) initStateStore() {
	url := s.cfg.State.RedisURL
	if pgURL := s.cfg.State.PostgresURL; pgURL != "" {
		pg, err := state.NewPostgres(pgURL, orDefault(s.cfg.State.Prefix, "ccg"))
		if err != nil {
			log.Printf("[state] postgres unavailable (%v), trying redis/memory", err)
		} else {
			s.storeBackend = pg
			s.limiter.SetStore(pg, "")
			s.budgets.SetStore(pg, "")
			log.Printf("[state] using postgres")
			return
		}
	}
	if url == "" {
		s.storeBackend = state.NewMemory()
		return
	}
	rs, err := state.NewRedis(url, s.cfg.State.Prefix)
	if err != nil || !rs.Healthy() {
		log.Printf("[state] redis unavailable (%v), falling back to in-memory state", err)
		s.storeBackend = state.NewMemory()
		return
	}
	if s.cfg.State.Prefix == "" {
		rs.SetPrefix("ccg:")
	} else if !strings.HasSuffix(s.cfg.State.Prefix, ":") {
		rs.SetPrefix(s.cfg.State.Prefix + ":")
	}
	s.storeBackend = rs
	s.limiter.SetStore(rs, "")
	s.budgets.SetStore(rs, "")
	log.Printf("[state] using redis at %s (prefix %s)", url, rs.GetPrefix())
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type cachedEntryWire struct {
	B   string `json:"b"`
	In  int64  `json:"i"`
	Out int64  `json:"o"`
}

func cacheEncode(e cache.Entry) []byte {
	wire := cachedEntryWire{
		B:   base64.StdEncoding.EncodeToString(e.Body),
		In:  e.In,
		Out: e.Out,
	}
	b, _ := json.Marshal(wire)
	return b
}

func cacheDecode(b []byte) (cache.Entry, bool) {
	var wire cachedEntryWire
	if json.Unmarshal(b, &wire) != nil {
		return cache.Entry{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(wire.B)
	if err != nil {
		return cache.Entry{}, false
	}
	return cache.Entry{Body: raw, In: wire.In, Out: wire.Out}, true
}

func (s *Server) cacheGet(key string) (cache.Entry, bool) {
	if e, ok := s.cache.Get(key); ok {
		return e, true
	}
	if rs, ok := s.storeBackend.(*state.RedisStore); ok && s.cacheTTL > 0 {
		if raw, found, err := rs.GetBytes("cache:" + key); err == nil && found {
			if e, ok := cacheDecode(raw); ok {
				return e, true
			}
		}
	}
	return cache.Entry{}, false
}

func (s *Server) cachePutAll(key string, e cache.Entry) {
	s.cache.Put(key, e)
	if rs, ok := s.storeBackend.(*state.RedisStore); ok && s.cacheTTL > 0 {
		_ = rs.SetTTLBytes("cache:"+key, cacheEncode(e), s.cacheTTL)
	}
}

func (s *Server) handleFlushCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	if s.cache != nil {
		s.cache.Flush()
	}
	writeJSON(w, http.StatusOK, map[string]any{"flushed": true})
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type reqOverrides struct {
	skipCache   bool
	ttlOverride time.Duration
	customKey   string
	collectLog  bool
	maxAttempts int
	metadata    map[string]any
}

func parseOverrides(r *http.Request) reqOverrides {
	o := reqOverrides{collectLog: true}
	if v := headerBool(r, "x-ccg-skip-cache"); v {
		o.skipCache = true
	}
	if v := r.Header.Get("x-ccg-cache-ttl"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			o.ttlOverride = time.Duration(secs) * time.Second
		}
	}
	if v := r.Header.Get("x-ccg-cache-key"); v != "" {
		o.customKey = strings.TrimSpace(v)
	}
	if h := r.Header.Get("x-ccg-collect-log"); strings.TrimSpace(h) != "" && !headerBool(r, "x-ccg-collect-log") {
		o.collectLog = false
	}
	if v := r.Header.Get("x-ccg-max-attempts"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 10 {
				n = 10
			}
			o.maxAttempts = n
		}
	}
	if v := r.Header.Get("x-ccg-metadata"); v != "" {
		var md map[string]any
		if json.Unmarshal([]byte(v), &md) == nil && len(md) <= 5 {
			o.metadata = md
		}
	}
	return o
}

func headerBool(r *http.Request, name string) bool {
	v := strings.TrimSpace(strings.ToLower(r.Header.Get(name)))
	switch v {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return false
}

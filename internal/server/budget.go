package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/state"
)

type clientInfo struct {
	Name          string
	Limit         float64
	Period        string
	AllowedModels []string
	TPM           int64
}

type budgetState struct {
	spent     float64
	periodKey string
	tpmWin    int64
	tpmUsed   int64
}

type budgets struct {
	mu             sync.Mutex
	info           map[string]clientInfo
	states         map[string]*budgetState
	runtimeChanged bool
	store          state.Store
	prefix         string
}

func (b *budgets) SetStore(s state.Store, prefix string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store = s
	b.prefix = prefix
}

func (b *budgets) budgetKey(token string, now time.Time) string {
	sum := sha256.Sum256([]byte(token))
	short := hex.EncodeToString(sum[:4])
	return "budget:" + short + ":" + periodKeyNow(periodOf(b, token), now)
}

func periodOf(b *budgets, token string) string {
	if ci, ok := b.info[token]; ok {
		return ci.Period
	}
	return ""
}

func (b *budgets) statesMu(fn func(m map[string]*budgetState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(b.states)
}

func newBudgets(clients []config.Client) *budgets {
	b := &budgets{
		info:   map[string]clientInfo{},
		states: map[string]*budgetState{},
	}
	for _, c := range clients {
		if c.Token == "" {
			continue
		}
		limit := c.BudgetUSD
		if limit <= 0 {
			limit = -1
		}
		b.info[c.Token] = clientInfo{
			Name: c.Name, Limit: limit, Period: c.BudgetPeriod,
			AllowedModels: c.AllowedModels, TPM: c.TPM,
		}
	}
	return b
}

func matchAnyPattern(patterns []string, model string) bool {
	lm := strings.ToLower(model)
	for _, p := range patterns {
		if ok, err := path.Match(strings.ToLower(strings.TrimSpace(p)), lm); err == nil && ok {
			return true
		}
		if strings.EqualFold(p, lm) {
			return true
		}
	}
	return false
}

func (b *budgets) allowsModel(token, model string) bool {
	ci, ok := b.lookup(token)
	if !ok || len(ci.AllowedModels) == 0 {
		return true
	}
	return matchAnyPattern(ci.AllowedModels, model)
}

func periodKeyNow(period string, now time.Time) string {
	switch strings.ToLower(period) {
	case "daily":
		return now.UTC().Format("2006-01-02")
	case "weekly":
		y, w := now.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "monthly":
		return now.UTC().Format("2006-01")
	default:
		return "lifetime"
	}
}

func nextReset(period string, now time.Time) time.Time {
	switch strings.ToLower(period) {
	case "daily":
		t := now.UTC().AddDate(0, 0, 1)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case "weekly":
		y, w := now.ISOWeek()
		start := isoWeekStart(y, w)
		return start.AddDate(0, 0, 7)
	case "monthly":
		return now.UTC().AddDate(0, 1, -now.UTC().Day()+1).Truncate(24 * time.Hour)
	default:
		return time.Time{}
	}
}

func isoWeekStart(year, week int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	if wd := int(jan4.Weekday()); wd == 0 {
		jan4 = jan4.AddDate(0, 0, -6)
	} else {
		jan4 = jan4.AddDate(0, 0, -(wd - 1))
	}
	return jan4.AddDate(0, 0, (week-1)*7)
}

func (b *budgets) lookup(token string) (clientInfo, bool) {
	ci, ok := b.info[token]
	return ci, ok
}

func (b *budgets) spentFor(token string, ci clientInfo, now time.Time) float64 {
	if b.store != nil {
		if v, err := b.store.GetFloat(b.budgetKey(token, now)); err == nil {
			return v
		}
	}
	st := b.states[token]
	if st == nil || st.periodKey != periodKeyNow(ci.Period, now) {
		return 0
	}
	return st.spent
}

func (b *budgets) exceeded(token string, now time.Time) (clientInfo, float64, bool) {
	ci, ok := b.lookup(token)
	if !ok || ci.Limit < 0 {
		return ci, 0, false
	}
	spent := b.spentFor(token, ci, now)
	return ci, spent, spent >= ci.Limit
}

func (b *budgets) add(token string, usd float64, now time.Time) {
	ci, ok := b.lookup(token)
	if !ok || ci.Limit < 0 || usd <= 0 {
		return
	}
	if b.store != nil {
		if _, err := b.store.IncrFloat(b.budgetKey(token, now), usd); err == nil {
			return
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := periodKeyNow(ci.Period, now)
	st := b.states[token]
	if st == nil || st.periodKey != key {
		st = &budgetState{periodKey: key}
		b.states[token] = st
	}
	st.spent += usd
}

type tokenReport struct {
	Name         string    `json:"name"`
	Token        string    `json:"token"`
	BudgetUSD    float64   `json:"budget_usd"`
	SpentUSD     float64   `json:"spent_usd"`
	RemainingUSD float64   `json:"remaining_usd,omitempty"`
	Period       string    `json:"period,omitempty"`
	ResetsAt     time.Time `json:"resets_at,omitempty"`
	Unlimited    bool      `json:"unlimited"`
}

func (s *Server) tokenReports(now time.Time) []tokenReport {
	var out []tokenReport
	seen := map[string]bool{}
	s.budgets.mu.Lock()
	for tok, ci := range s.budgets.info {
		seen[tok] = true
		spent := s.budgets.spentFor(tok, ci, now)
		tr := tokenReport{
			Name: ci.Name, Token: maskToken(tok),
			BudgetUSD: ci.Limit, SpentUSD: round4(spent),
			Period: ci.Period,
		}
		if ci.Limit >= 0 {
			tr.RemainingUSD = round4(ci.Limit - spent)
			tr.ResetsAt = nextReset(ci.Period, now)
		} else {
			tr.Unlimited = true
		}
		out = append(out, tr)
	}
	for tok := range s.tokens {
		if seen[tok] {
			continue
		}
		out = append(out, tokenReport{
			Name: "unnamed", Token: maskToken(tok), Unlimited: true,
		})
	}
	s.budgets.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.tokenReports(time.Now())})
}

func (b *budgets) updateLimitByName(name string, limit float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for tok, ci := range b.info {
		if strings.EqualFold(ci.Name, name) {
			ci.Limit = limit
			b.info[tok] = ci
			return true
		}
	}
	return false
}

func (b *budgets) addClientRuntime(name, token string, budget float64, period string, models []string, tpm int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := budget
	if limit <= 0 {
		limit = -1
	}
	b.info[token] = clientInfo{
		Name: name, Limit: limit, Period: period,
		AllowedModels: models, TPM: tpm,
	}
}

type updateBudgetReq struct {
	Name      string  `json:"name"`
	BudgetUSD float64 `json:"budget_usd"`
}

func (s *Server) handleAdminTokensUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "use POST")
		return
	}
	var req updateBudgetReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "body: {\"name\":..., \"budget_usd\":...}")
		return
	}
	if !s.budgets.updateLimitByName(req.Name, req.BudgetUSD) {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "client not found: "+req.Name)
		return
	}
	s.budgets.statesMu(func(m map[string]*budgetState) {
		for tok := range m {
			if s.budgets.info[tok].Name == req.Name {
				delete(m, tok)
			}
		}
	})
	s.budgets.runtimeChanged = true
	writeJSON(w, http.StatusOK, map[string]any{
		"updated":    req.Name,
		"budget_usd": req.BudgetUSD,
		"note":       "runtime-only until config reload/restart",
	})
}

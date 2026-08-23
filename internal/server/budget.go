package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"claude-code-gateway/internal/config"
)

type clientInfo struct {
	Name   string
	Limit  float64
	Period string
}

type budgetState struct {
	spent     float64
	periodKey string
}

type budgets struct {
	mu     sync.Mutex
	info   map[string]clientInfo
	states map[string]*budgetState
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
		b.info[c.Token] = clientInfo{Name: c.Name, Limit: c.BudgetUSD, Period: c.BudgetPeriod}
	}
	return b
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
	st := b.states[token]
	if st == nil || st.periodKey != periodKeyNow(ci.Period, now) {
		return 0
	}
	return st.spent
}

func (b *budgets) exceeded(token string, now time.Time) (clientInfo, float64, bool) {
	ci, ok := b.lookup(token)
	if !ok || ci.Limit <= 0 {
		return ci, 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	spent := b.spentFor(token, ci, now)
	return ci, spent, spent >= ci.Limit
}

func (b *budgets) add(token string, usd float64, now time.Time) {
	ci, ok := b.lookup(token)
	if !ok || ci.Limit <= 0 || usd <= 0 {
		return
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
		if ci.Limit > 0 {
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

func (s *Server) tokenLabel(token string) string {
	if ci, ok := s.budgets.lookup(token); ok && ci.Name != "" {
		return ci.Name
	}
	return maskToken(token)
}

func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tokens": s.tokenReports(time.Now())})
}

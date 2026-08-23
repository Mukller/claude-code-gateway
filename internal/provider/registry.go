package provider

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"claude-code-gateway/internal/config"
)

type Target struct {
	Name  string
	Model string
}

type CatalogEntry struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type compiledRule struct {
	prefix      string
	strip       bool
	chain       []string
	mmap        map[string]string
	targets     []Target
	loadBalance bool
	strategy    string
}

type Registry struct {
	mu         sync.RWMutex
	providers  map[string]*Provider
	order      []string
	rules      []compiledRule
	defChain   []string
	alias      bool
	scenarios  config.Scenarios
	affinity   bool
	discovered map[string][]string

	genMu     sync.Mutex
	rootCtx   context.Context
	genCancel context.CancelFunc
}

func NewRegistry(routing *config.Routing, provCfgs []config.Provider) *Registry {
	reg := &Registry{
		providers:  map[string]*Provider{},
		defChain:   routing.DefaultChain,
		alias:      routing.AliasClaudePrefix,
		discovered: map[string][]string{},
	}
	reg.apply(routing, provCfgs)
	return reg
}

func (r *Registry) apply(routing *config.Routing, provCfgs []config.Provider) {
	provs := map[string]*Provider{}
	var order []string
	for _, pc := range provCfgs {
		p := New(pc)
		provs[p.Name()] = p
		order = append(order, p.Name())
	}
	var rules []compiledRule
	for _, rc := range routing.Rules {
		strategy := rc.BalanceStrategy
		if strategy == "" && rc.LoadBalance {
			strategy = "weighted"
		}
		cr := compiledRule{
			prefix:      rc.Prefix,
			strip:       rc.StripPrefix,
			chain:       rc.Chain,
			mmap:        rc.ModelMap,
			loadBalance: strategy != "",
			strategy:    strings.ToLower(strategy),
		}
		for _, t := range rc.Targets {
			cr.targets = append(cr.targets, Target{Name: t.Provider, Model: t.Model})
		}
		rules = append(rules, cr)
	}
	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			if len(rules[j].prefix) > len(rules[i].prefix) {
				rules[i], rules[j] = rules[j], rules[i]
			}
		}
	}
	r.providers = provs
	r.order = order
	r.rules = rules
	r.defChain = routing.DefaultChain
	r.alias = routing.AliasClaudePrefix
	r.scenarios = routing.Scenarios
	r.affinity = routing.SessionAffinity
	r.discovered = map[string][]string{}
}

func hasUsableProvider(provCfgs []config.Provider) bool {
	for _, pc := range provCfgs {
		n := 0
		for _, k := range pc.Keys {
			if k != "" {
				n++
			}
		}
		if n > 0 || (pc.Type == "vertex" && pc.AuthStyle == "sa") {
			return true
		}
	}
	return false
}

func (r *Registry) Swap(routing *config.Routing, provCfgs []config.Provider) error {
	if !hasUsableProvider(provCfgs) {
		return fmt.Errorf("reload rejected: no provider with keys")
	}
	r.genMu.Lock()
	defer r.genMu.Unlock()
	if r.genCancel != nil {
		r.genCancel()
		r.genCancel = nil
	}
	r.mu.Lock()
	r.apply(routing, provCfgs)
	r.mu.Unlock()
	if r.rootCtx != nil {
		genCtx, cancel := context.WithCancel(r.rootCtx)
		r.genCancel = cancel
		go r.runDiscovery(genCtx)
	}
	return nil
}

func (r *Registry) Provider(name string) *Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[name]
}

func (r *Registry) Providers() []*Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Provider, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.providers[n])
	}
	return out
}

type ResolveInfo struct {
	EstTokens int64
	HasImage  bool
	Thinking  bool
	SessionID string
}

func (r *Registry) Resolve(model string, inf ResolveInfo) ([]Target, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	pick := func(tg []Target, m string) ([]Target, string) {
		if r.affinity && inf.SessionID != "" && len(tg) > 1 {
			tg = stickyRotate(tg, inf.SessionID)
		}
		live := make([]Target, 0, len(tg))
		for _, t := range tg {
			if p := r.providers[t.Name]; p != nil && p.IsOpen(now) {
				continue
			}
			live = append(live, t)
		}
		if len(live) == 0 {
			return tg, m
		}
		return live, m
	}
	if tg, m, ok := r.matchScenarioLocked(model, inf); ok {
		return pick(tg, m)
	}
	if tg, m, ok := r.matchRulesLocked(model); ok {
		return pick(tg, m)
	}
	if r.alias && strings.HasPrefix(model, "claude/") && len(model) > len("claude/") {
		stripped := strings.TrimPrefix(model, "claude/")
		if tg, m, ok := r.matchRulesLocked(stripped); ok {
			return pick(tg, m)
		}
	}
	tg := make([]Target, 0, len(r.defChain))
	for _, n := range r.defChain {
		tg = append(tg, Target{Name: n, Model: model})
	}
	return pick(tg, model)
}

func chainTargets(chain []string, m string) []Target {
	tg := make([]Target, 0, len(chain))
	for _, n := range chain {
		tg = append(tg, Target{Name: n, Model: m})
	}
	return tg
}

func stickyRotate(tg []Target, sessionID string) []Target {
	h := fnv32(sessionID)
	off := int(h) % len(tg)
	out := make([]Target, 0, len(tg))
	out = append(out, tg[off:]...)
	out = append(out, tg[:off]...)
	return out
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func (r *Registry) matchScenarioLocked(model string, inf ResolveInfo) ([]Target, string, bool) {
	sc := r.scenarios
	if lc := sc.LongContext; lc != nil && lc.ThresholdTokens > 0 && inf.EstTokens >= lc.ThresholdTokens && len(lc.Chain) > 0 {
		return chainTargets(lc.Chain, model), model, true
	}
	if inf.HasImage && sc.Image != nil && len(sc.Image.Chain) > 0 {
		return chainTargets(sc.Image.Chain, model), model, true
	}
	if inf.Thinking && sc.Thinking != nil && len(sc.Thinking.Chain) > 0 {
		return chainTargets(sc.Thinking.Chain, model), model, true
	}
	return nil, "", false
}

func (r *Registry) matchRulesLocked(model string) ([]Target, string, bool) {
	for _, ru := range r.rules {
		if ru.prefix == "" || strings.HasPrefix(model, ru.prefix) {
			if len(ru.targets) > 0 {
				tg := make([]Target, len(ru.targets))
				copy(tg, ru.targets)
				if ru.loadBalance {
					tg = r.orderByStrategyLocked(tg, ru.strategy)
				}
				return tg, tg[0].Model, true
			}
			m := model
			if ru.strip && ru.prefix != "" {
				m = strings.TrimPrefix(m, ru.prefix)
			}
			if mapped, has := ru.mmap[m]; has {
				m = mapped
			}
			if ru.loadBalance && len(ru.chain) > 1 {
				tmp := make([]Target, 0, len(ru.chain))
				for _, n := range ru.chain {
					tmp = append(tmp, Target{Name: n, Model: m})
				}
				return r.orderByStrategyLocked(tmp, ru.strategy), m, true
			}
			tg := make([]Target, 0, len(ru.chain))
			for _, n := range ru.chain {
				tg = append(tg, Target{Name: n, Model: m})
			}
			return tg, m, true
		}
	}
	return nil, "", false
}

func (r *Registry) orderByStrategyLocked(tg []Target, strategy string) []Target {
	out := make([]Target, len(tg))
	copy(out, tg)
	switch strategy {
	case "least_busy":
		sort.SliceStable(out, func(i, j int) bool {
			pi, pj := r.providers[out[i].Name], r.providers[out[j].Name]
			ii, ij := int32(0), int32(0)
			if pi != nil {
				ii = pi.Inflight()
			}
			if pj != nil {
				ij = pj.Inflight()
			}
			if ii != ij {
				return ii < ij
			}
			return r.weightOf(out[i].Name) > r.weightOf(out[j].Name)
		})
	case "latency":
		sort.SliceStable(out, func(i, j int) bool {
			pi, pj := r.providers[out[i].Name], r.providers[out[j].Name]
			var li, lj float64
			if pi != nil && pi.Latency() > 0 {
				li = pi.Latency()
			}
			if pj != nil && pj.Latency() > 0 {
				lj = pj.Latency()
			}
			if li != lj {
				return li < lj
			}
			return r.weightOf(out[i].Name) > r.weightOf(out[j].Name)
		})
	default:
		sort.SliceStable(out, func(i, j int) bool {
			return weightedKey(r.weightOf(out[i].Name)) > weightedKey(r.weightOf(out[j].Name))
		})
	}
	return out
}

func (r *Registry) weightedShuffleLocked(tg []Target) []Target {
	return r.orderByStrategyLocked(tg, "weighted")
}

func weightedKey(w int) float64 {
	if w <= 0 {
		w = 1
	}
	return rand.Float64() * float64(w)
}

func (r *Registry) weightOf(name string) int {
	if p := r.providers[name]; p != nil {
		return p.cfg.Weight
	}
	return 1
}

func (r *Registry) StartDiscovery(ctx context.Context) {
	r.genMu.Lock()
	defer r.genMu.Unlock()
	r.rootCtx = ctx
	genCtx, cancel := context.WithCancel(ctx)
	r.genCancel = cancel
	go r.runDiscovery(genCtx)
}

func (r *Registry) runDiscovery(ctx context.Context) {
	for _, p := range r.Providers() {
		interval := p.cfg.RefreshInterval
		if p.DiscoversModels() && interval > 0 {
			go r.probeLoop(ctx, p, interval)
			continue
		}
		if p.cfg.ProbeInterval > 0 && p.Type() == "openai" {
			go r.probeLoop(ctx, p, p.cfg.ProbeInterval)
		}
	}
}

func (r *Registry) probeLoop(ctx context.Context, p *Provider, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	r.refreshModels(p)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refreshModels(p)
		}
	}
}

func (r *Registry) refreshModels(p *Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	models, err := p.FetchModels(ctx)
	pi := ProbeInfo{
		OK:        err == nil,
		LatencyMs: time.Since(started).Milliseconds(),
		At:        time.Now(),
	}
	if err != nil {
		pi.Error = err.Error()
		log.Printf("[probe] provider %q: %v", p.Name(), err)
	} else {
		r.mu.Lock()
		r.discovered[p.Name()] = models
		r.mu.Unlock()
		log.Printf("[probe] provider %q: ok, %d models, %dms", p.Name(), len(models), pi.LatencyMs)
	}
	p.SetProbe(pi)
}

func (r *Registry) Catalog() []CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CatalogEntry
	seen := map[string]bool{}
	add := func(id, prov string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, CatalogEntry{ID: id, Provider: prov})
	}
	for _, name := range r.order {
		p := r.providers[name]
		for _, m := range p.StaticModels() {
			add(m, name)
		}
		for _, m := range r.discovered[name] {
			add(m, name)
		}
	}
	if r.alias {
		n := len(out)
		for i := 0; i < n; i++ {
			id := out[i].ID
			if !strings.HasPrefix(id, "claude/") && !strings.HasPrefix(id, "anthropic/") {
				add("claude/"+id, out[i].Provider)
			}
		}
	}
	return out
}

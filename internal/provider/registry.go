package provider

import (
	"context"
	"log"
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
	prefix string
	strip  bool
	chain  []string
	mmap   map[string]string
}

type Registry struct {
	mu         sync.RWMutex
	providers  map[string]*Provider
	order      []string
	rules      []compiledRule
	defChain   []string
	alias      bool
	discovered map[string][]string
}

func NewRegistry(routing *config.Routing, provCfgs []config.Provider) *Registry {
	reg := &Registry{
		providers:  map[string]*Provider{},
		defChain:   routing.DefaultChain,
		alias:      routing.AliasClaudePrefix,
		discovered: map[string][]string{},
	}
	for _, pc := range provCfgs {
		p := New(pc)
		reg.providers[p.Name()] = p
		reg.order = append(reg.order, p.Name())
	}
	for _, rc := range routing.Rules {
		reg.rules = append(reg.rules, compiledRule{
			prefix: rc.Prefix,
			strip:  rc.StripPrefix,
			chain:  rc.Chain,
			mmap:   rc.ModelMap,
		})
	}
	for i := 0; i < len(reg.rules); i++ {
		for j := i + 1; j < len(reg.rules); j++ {
			if len(reg.rules[j].prefix) > len(reg.rules[i].prefix) {
				reg.rules[i], reg.rules[j] = reg.rules[j], reg.rules[i]
			}
		}
	}
	return reg
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

func (r *Registry) Resolve(model string) ([]Target, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tg, m, ok := r.matchRulesLocked(model); ok {
		return tg, m
	}
	if r.alias && strings.HasPrefix(model, "claude/") && len(model) > len("claude/") {
		stripped := strings.TrimPrefix(model, "claude/")
		if tg, m, ok := r.matchRulesLocked(stripped); ok {
			return tg, m
		}
	}
	tg := make([]Target, 0, len(r.defChain))
	for _, n := range r.defChain {
		tg = append(tg, Target{Name: n, Model: model})
	}
	return tg, model
}

func (r *Registry) matchRulesLocked(model string) ([]Target, string, bool) {
	for _, ru := range r.rules {
		if ru.prefix == "" || strings.HasPrefix(model, ru.prefix) {
			m := model
			if ru.strip && ru.prefix != "" {
				m = strings.TrimPrefix(m, ru.prefix)
			}
			if mapped, has := ru.mmap[m]; has {
				m = mapped
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

func (r *Registry) StartDiscovery(ctx context.Context) {
	for _, p := range r.Providers() {
		if !p.DiscoversModels() {
			continue
		}
		go func(p *Provider) {
			interval := p.cfg.RefreshInterval
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
		}(p)
	}
}

func (r *Registry) refreshModels(p *Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := p.FetchModels(ctx)
	if err != nil {
		log.Printf("[discovery] provider %q: %v", p.Name(), err)
		return
	}
	r.mu.Lock()
	r.discovered[p.Name()] = models
	r.mu.Unlock()
	log.Printf("[discovery] provider %q: %d models", p.Name(), len(models))
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

package provider

import (
	"context"
	"testing"

	"claude-code-gateway/internal/config"
)

func swapRouting(chain []string) *config.Routing {
	return &config.Routing{
		DefaultChain: chain,
		Rules: []config.Rule{
			{Prefix: "special/", StripPrefix: true, Chain: chain},
		},
	}
}

func TestRegistrySwap(t *testing.T) {
	reg := NewRegistry(swapRouting([]string{"a"}), []config.Provider{
		{Name: "a", Type: "openai", BaseURL: "https://a.test/v1", Keys: []string{"k1"}},
	})

	tg, _ := reg.Resolve("anything", ResolveInfo{})
	if len(tg) != 1 || tg[0].Name != "a" {
		t.Fatalf("initial resolve = %+v", tg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.StartDiscovery(ctx)

	if err := reg.Swap(swapRouting([]string{"b"}), []config.Provider{
		{Name: "b", Type: "openai", BaseURL: "https://b.test/v1", Keys: []string{"k2"}},
	}); err != nil {
		t.Fatal(err)
	}

	if reg.Provider("a") != nil {
		t.Fatal("old provider must be gone after swap")
	}
	if reg.Provider("b") == nil {
		t.Fatal("new provider missing")
	}
	tg, model := reg.Resolve("special/x", ResolveInfo{})
	if len(tg) != 1 || tg[0].Name != "b" || tg[0].Model != "x" || model != "x" {
		t.Fatalf("resolve after swap = %+v (model %q)", tg, model)
	}

	if err := reg.Swap(swapRouting([]string{"ghost"}), []config.Provider{
		{Name: "ghost", Type: "openai", BaseURL: "https://g.test", Keys: []string{}},
	}); err == nil {
		t.Fatal("swap without usable providers must fail")
	}
	if reg.Provider("b") == nil {
		t.Fatal("failed swap must keep current providers")
	}
}

func TestRegistryComboTargets(t *testing.T) {
	reg := NewRegistry(&config.Routing{
		Rules: []config.Rule{
			{
				Prefix: "combo/fast",
				Targets: []config.RouteTarget{
					{Provider: "cheap", Model: "glm-x"},
					{Provider: "premium", Model: "claude-haiku"},
				},
			},
		},
	}, []config.Provider{
		{Name: "cheap", Type: "openai", BaseURL: "https://c.test/v1", Keys: []string{"k"}},
		{Name: "premium", Type: "anthropic", BaseURL: "https://p.test", Keys: []string{"k"}},
	})
	tg, m := reg.Resolve("combo/fast", ResolveInfo{})
	if len(tg) != 2 {
		t.Fatalf("targets = %+v", tg)
	}
	if tg[0].Name != "cheap" || tg[0].Model != "glm-x" {
		t.Fatalf("target[0] = %+v", tg[0])
	}
	if tg[1].Name != "premium" || tg[1].Model != "claude-haiku" {
		t.Fatalf("target[1] = %+v", tg[1])
	}
	if m != "glm-x" {
		t.Fatalf("out model = %s", m)
	}
}

func TestRegistryAliasAfterSwap(t *testing.T) {
	reg := NewRegistry(&config.Routing{AliasClaudePrefix: true}, []config.Provider{
		{Name: "a", Type: "openai", BaseURL: "https://a.test/v1", Keys: []string{"k"}},
	})
	reg.mu.Lock()
	reg.discovered["a"] = []string{"glm-x"}
	reg.mu.Unlock()

	catalog := reg.Catalog()
	found := false
	for _, e := range catalog {
		if e.ID == "claude/glm-x" {
			found = true
		}
	}
	if !found {
		t.Fatal("alias entry missing in catalog")
	}
}

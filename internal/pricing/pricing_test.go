package pricing

import (
	"math"
	"testing"

	"claude-code-gateway/internal/config"
)

func TestExactMatch(t *testing.T) {
	tbl := New([]config.PriceRule{
		{Pattern: "claude-sonnet-4", InputPerMTok: 3, OutputPerMTok: 15},
	})
	p, ok := tbl.Match("claude-sonnet-4")
	if !ok || p.InputPerMTok != 3 {
		t.Fatalf("exact match failed: %+v ok=%v", p, ok)
	}
}

func TestGlobMatch(t *testing.T) {
	tbl := New([]config.PriceRule{
		{Pattern: "claude-*", InputPerMTok: 3, OutputPerMTok: 15},
		{Pattern: "gpt-4o*", InputPerMTok: 2.5, OutputPerMTok: 10},
	})
	p, ok := tbl.Match("claude-opus-4")
	if !ok || p.InputPerMTok != 3 {
		t.Fatal("glob claude-* failed")
	}
	p, _ = tbl.Match("gpt-4o-mini")
	if p.InputPerMTok != 2.5 {
		t.Fatal("glob gpt-4o* failed")
	}
	_, ok = tbl.Match("unknown-model")
	if ok {
		t.Fatal("unknown model should not match")
	}
}

func TestCost(t *testing.T) {
	p := Price{InputPerMTok: 3, OutputPerMTok: 15}
	c := Cost(p, 1_000_000, 500_000, 0, 0)
	if math.Abs(c-10.5) > 0.001 {
		t.Fatalf("cost = %v, want 10.5", c)
	}
	c = Cost(p, 0, 0, 0, 0)
	if c != 0 {
		t.Fatal("zero cost for zero tokens")
	}
}

func TestCostWithCache(t *testing.T) {
	p := Price{InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}
	c := Cost(p, 100_000, 50_000, 200_000, 100_000)
	expected := float64(100_000)/1e6*3 + float64(50_000)/1e6*15 + float64(200_000)/1e6*0.3 + float64(100_000)/1e6*3.75
	if math.Abs(c-expected) > 0.001 {
		t.Fatalf("cost = %v, want %v", c, expected)
	}
}

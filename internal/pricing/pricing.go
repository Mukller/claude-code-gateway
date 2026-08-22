package pricing

import (
	"math"
	"path"
	"strings"

	"claude-code-gateway/internal/config"
)

type Price struct {
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok,omitempty"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok,omitempty"`
}

type Table struct {
	exact    map[string]Price
	patterns []namedPattern
}

type namedPattern struct {
	pattern string
	price   Price
}

func New(rules []config.PriceRule) Table {
	t := Table{exact: map[string]Price{}}
	for _, r := range rules {
		p := Price{
			InputPerMTok:      r.InputPerMTok,
			OutputPerMTok:     r.OutputPerMTok,
			CacheReadPerMTok:  r.CacheReadPerMTok,
			CacheWritePerMTok: r.CacheWritePerMTok,
		}
		if strings.ContainsAny(r.Pattern, "*?[") {
			t.patterns = append(t.patterns, namedPattern{r.Pattern, p})
		} else {
			t.exact[strings.ToLower(r.Pattern)] = p
		}
	}
	return t
}

func (t Table) Match(model string) (Price, bool) {
	if t.exact != nil {
		if p, ok := t.exact[strings.ToLower(model)]; ok {
			return p, true
		}
	}
	lm := strings.ToLower(model)
	for _, np := range t.patterns {
		ok, err := path.Match(strings.ToLower(np.pattern), lm)
		if err == nil && ok {
			return np.price, true
		}
	}
	return Price{}, false
}

func Cost(p Price, in, out, cacheRead, cacheWrite int64) float64 {
	c := float64(in)/1e6*p.InputPerMTok +
		float64(out)/1e6*p.OutputPerMTok +
		float64(cacheRead)/1e6*p.CacheReadPerMTok +
		float64(cacheWrite)/1e6*p.CacheWritePerMTok
	return math.Round(c*10000) / 10000
}

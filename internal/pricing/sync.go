package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"claude-code-gateway/internal/config"
)

type orModel struct {
	ID      string `json:"id"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

func FetchOpenRouter(ctx context.Context, endpoint string) ([]config.PriceRule, error) {
	if endpoint == "" {
		endpoint = "https://openrouter.ai/api/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openrouter models: status %d", resp.StatusCode)
	}
	var out struct {
		Data []orModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
		return nil, err
	}
	rules := make([]config.PriceRule, 0, len(out.Data))
	for _, m := range out.Data {
		in, e1 := strconv.ParseFloat(m.Pricing.Prompt, 64)
		outp, e2 := strconv.ParseFloat(m.Pricing.Completion, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		inMTok := in * 1e6
		outMTok := outp * 1e6
		if inMTok <= 0 && outMTok <= 0 {
			continue
		}
		rules = append(rules, config.PriceRule{
			Pattern:       m.ID,
			InputPerMTok:  round4f(inMTok),
			OutputPerMTok: round4f(outMTok),
		})
	}
	return rules, nil
}

func round4f(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

package server

import (
	"context"
	"log"
	"time"

	"claude-code-gateway/internal/pricing"
)

func (s *Server) StartPriceSync(ctx context.Context) {
	ps := s.cfg.PricingSync
	if !ps.Enabled {
		return
	}
	if ps.Interval <= 0 {
		ps.Interval = 6 * time.Hour
	}
	go func() {
		t := time.NewTicker(ps.Interval)
		defer t.Stop()
		s.syncPricesOnce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.syncPricesOnce()
			}
		}
	}()
}

func (s *Server) syncPricesOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rules, err := pricing.FetchOpenRouter(ctx, s.cfg.PricingSync.Endpoint)
	if err != nil {
		log.Printf("[pricesync] %v", err)
		return
	}
	tbl := pricing.New(rules)
	s.autoPrices.Store(&tbl)
	log.Printf("[pricesync] loaded %d model prices", len(rules))
}

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"claude-code-gateway/internal/config"
	"claude-code-gateway/internal/logstore"
	"claude-code-gateway/internal/pricing"
	"claude-code-gateway/internal/provider"
	"claude-code-gateway/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()
	if envPath := os.Getenv("GATEWAY_CONFIG"); envPath != "" {
		cfgPath = &envPath
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded from %s", *cfgPath)

	store, err := logstore.New(cfg.Logging.File, cfg.Logging.RingSize)
	if err != nil {
		log.Fatalf("usage store: %v", err)
	}
	prices := pricing.New(cfg.Pricing)
	reg := provider.NewRegistry(&cfg.Routing, cfg.Providers)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reg.StartDiscovery(ctx)

	srv := server.New(cfg, reg, store, prices)
	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("gateway listening on %s (providers: %d)", cfg.Server.Listen, len(cfg.Providers))
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

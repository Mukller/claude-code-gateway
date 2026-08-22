package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	launch := flag.Bool("launch", false, "start gateway and spawn `claude` with env injected")
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

	if *launch {
		go spawnClaude(cfg.Server.Listen, cfg.Auth.Tokens, flag.Args(), stop)
	}

	<-ctx.Done()
	log.Println("shutting down...")
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func baseURLFor(listen string) string {
	if strings.HasPrefix(listen, ":") {
		return "http://127.0.0.1" + listen
	}
	if !strings.Contains(listen, "://") {
		return "http://" + listen
	}
	return listen
}

func spawnClaude(listen string, tokens []string, extraArgs []string, stop context.CancelFunc) {
	token := ""
	for _, t := range tokens {
		if t != "" {
			token = t
			break
		}
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		bin, err = exec.LookPath("claude.cmd")
	}
	if err != nil {
		log.Printf("[launch] claude CLI not found in PATH: %v", err)
		stop()
		return
	}
	base := baseURLFor(listen)
	cmd := exec.Command(bin, extraArgs...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ANTHROPIC_BASE_URL=%s", base),
		fmt.Sprintf("ANTHROPIC_AUTH_TOKEN=%s", token),
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("[launch] starting claude via %s (base_url=%s)", bin, base)
	if err := cmd.Run(); err != nil {
		log.Printf("[launch] claude exited: %v", err)
	}
	stop()
}

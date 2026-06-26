// Command bot is the 24/7 unattended entry point for the Toss trading bot.
//
// main stays thin: load config, wire dependencies, and run until a shutdown
// signal arrives. All business logic lives under internal/.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/chnu-kim/toss-trade-bot/internal/config"
	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Stop the world cleanly on SIGINT/SIGTERM so an unattended restart
	// (systemd, container orchestrator, etc.) is graceful, not abrupt.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := toss.NewClient(cfg.BaseURL, cfg.ClientID, cfg.ClientSecret)
	_ = client // wired into the trading loop as strategy/order logic lands.

	logger.Info("toss-trade-bot starting", "base_url", cfg.BaseURL)

	<-ctx.Done()
	logger.Info("shutting down")
}

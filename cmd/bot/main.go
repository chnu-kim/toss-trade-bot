// Command bot is the 24/7 unattended entry point for the Toss trading bot.
//
// main stays thin: load config, wire dependencies, and run until a shutdown
// signal arrives. All business logic lives under internal/.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/config"
	"github.com/chnu-kim/toss-trade-bot/internal/runtime"
	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// shutdownTimeout bounds how long we wait for supervised goroutines to drain
// after a shutdown signal, so an unattended restart is never blocked forever by
// a stuck loop.
const shutdownTimeout = 10 * time.Second

func main() {
	logger := runtime.NewLogger(os.Stdout)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Stop the world cleanly on SIGINT/SIGTERM so an unattended restart
	// (systemd, container orchestrator, etc.) is graceful, not abrupt.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Every long-lived loop launches via sup.Go so a panic in one is logged
	// and contained instead of crashing the process. No loops exist yet; the
	// supervisor is the boundary they will attach to as strategy/order logic
	// lands.
	sup := runtime.NewSupervisor(logger)

	client := toss.NewClient(cfg.BaseURL, cfg.ClientID, cfg.ClientSecret)
	_ = client // wired into the trading loop as strategy/order logic lands.

	logger.Info("toss-trade-bot starting", "base_url", cfg.BaseURL)

	<-ctx.Done()
	logger.Info("shutting down", "drain_timeout", shutdownTimeout.String())
	if !sup.Wait(shutdownTimeout) {
		logger.Warn("shutdown timed out with goroutines still running", "drain_timeout", shutdownTimeout.String())
	}
	logger.Info("shutdown complete")
}

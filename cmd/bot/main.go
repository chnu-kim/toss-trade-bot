// Command bot is the 24/7 unattended entry point for the Toss trading bot.
//
// main stays thin: load config, assemble dependencies, run until a shutdown
// signal arrives. Every ordering rule that matters — the clean-shutdown sentinel
// boot/shutdown judgment (ADR-0012 Decision 1(c)) and the replay gate opening
// only after the reconciler scan (ADR-0004 point 3) — lives in internal/runtime,
// where it is unit-testable against a real store. All business logic lives under
// internal/.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chnu-kim/toss-trade-bot/internal/config"
	"github.com/chnu-kim/toss-trade-bot/internal/runtime"
)

func main() {
	logger := runtime.NewLogger(os.Stdout)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Stop the world cleanly on SIGINT/SIGTERM so an unattended restart
	// (systemd, container orchestrator, ...) is graceful, not abrupt — and so
	// the run gets its chance to certify itself clean, which is what keeps the
	// next boot from coming up conservatively halted.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := runtime.Assemble(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to assemble the bot", "err", err)
		os.Exit(1)
	}

	if err := app.Run(ctx); err != nil {
		logger.Error("bot exited with an error", "err", err)
		os.Exit(1)
	}
}

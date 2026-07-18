package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/config"
	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/reconciler"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// tokenEscalationTimeout bounds the durable write of a token-refresh escalation.
// It runs on a context detached from the process lifetime (see
// tokenFailureReporter), so it needs a bound of its own.
const tokenEscalationTimeout = 30 * time.Second

// App is the assembled unattended bot: every durable and safety component wired
// together, with no strategy attached.
//
// The submit path is deliberately DORMANT. It is fully wired — journal, audit,
// kill switch, API — but nothing calls it, because no strategy exists yet
// (that is out of this issue's scope). Wiring it now means the day a strategy
// lands, the only new edge is the intent producer, not the safety plumbing.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	sup    *Supervisor

	client *toss.Client
	db     *store.DB
	// sentinel is the lifecycle seam Boot/Shutdown judge through. It is a.db in
	// production; the narrow interface keeps the judgment testable against an
	// injected store and keeps the journal out of reach.
	sentinel  SentinelStore
	sink      *audit.Writer
	guard     *killswitch.Switch
	rec       *reconciler.Reconciler
	submitter *order.Submitter
}

// Assemble builds every component in dependency order and wires the safety
// seams between them. It does not start anything and does not touch the
// clean-shutdown sentinel — that is Run's job, because the sentinel judgment is
// about a process LIFETIME, not about construction.
//
// On any failure after the store is open, the already-built resources are
// released before returning: a half-built app that leaked the single write
// connection would make the next restart fail too, and restarts are the normal
// unattended recovery path.
func Assemble(ctx context.Context, cfg config.Config, logger *slog.Logger) (app *App, err error) {
	if cfg.AccountSeqNum <= 0 {
		return nil, fmt.Errorf("runtime: accountSeq is required (set TOSS_ACCOUNT_SEQ from GET /api/v1/accounts)")
	}

	client, err := toss.NewClient(cfg.BaseURL, cfg.ClientID, cfg.ClientSecret.Reveal())
	if err != nil {
		return nil, fmt.Errorf("runtime: build toss client: %w", err)
	}
	client.SetLogger(logger)

	if err := os.MkdirAll(filepath.Dir(cfg.StorePath), 0o755); err != nil {
		return nil, fmt.Errorf("runtime: create store directory: %w", err)
	}
	db, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("runtime: open store: %w", err)
	}
	// From here on every early return must release the store.
	defer func() {
		if err != nil {
			if cerr := db.Close(); cerr != nil {
				logger.Error("failed to release store after a failed assembly", "err", cerr)
			}
		}
	}()

	sink, err := audit.New(cfg.AuditDir, audit.WithLogger(logger))
	if err != nil {
		return nil, fmt.Errorf("runtime: open audit sink: %w", err)
	}
	defer func() {
		if err != nil {
			if cerr := sink.Close(); cerr != nil {
				logger.Error("failed to release audit sink after a failed assembly", "err", cerr)
			}
		}
	}()

	// The kill switch loads the durable halt (persistence-wins). A load failure
	// is NOT fatal: New still returns a usable, boot-halted guard, and coming up
	// blocked-but-alive is strictly better than exiting, because the reconciler
	// still recovers truth while a halt only blocks new exposure (ADR-0004
	// point 1). A config validation failure, by contrast, returns a nil guard
	// and must stop the process.
	guard, kerr := killswitch.New(ctx, db, killswitch.Config{
		OrderFailureThreshold: cfg.OrderFailureThreshold,
		TokenRefreshThreshold: cfg.TokenRefreshThreshold,
		TokenRefreshWindow:    cfg.TokenRefreshWindow,
	}, killswitch.WithNotifier(slogNotifier{logger: logger}))
	if guard == nil {
		err = fmt.Errorf("runtime: build kill switch: %w", kerr)
		return nil, err
	}
	if kerr != nil {
		logger.Error("kill switch loaded fail-closed; new exposure stays blocked until a manual clear", "err", kerr)
	}

	orderClient := order.NewClient(client)

	rec, err := reconciler.New(reconciler.Config{
		Journal:                   db,
		Guard:                     guard,
		API:                       orderClient,
		Audit:                     sink,
		AccountSeq:                cfg.AccountSeqNum,
		AmbiguousBacklogThreshold: cfg.AmbiguousBacklogThreshold,
		SettleWindow:              cfg.SettleWindow,
		ReevalInterval:            cfg.ReevalInterval,
		Logger:                    logger,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: build reconciler: %w", err)
	}

	submitter, err := order.NewSubmitter(order.SubmitterConfig{
		Journal:    db,
		Audit:      sink,
		Guard:      guard,
		API:        orderClient,
		AccountSeq: cfg.AccountSeqNum,
		// Wake converges a just-submitted ambiguous intent immediately instead
		// of waiting for the reconciler's next tick.
		Wake: rec.Wake,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: build submitter: %w", err)
	}

	// Token-refresh failure is non-reconstructable, so it escalates through the
	// kill switch's COUNTED report — never a direct global trip, which would
	// bypass the threshold/window contract killswitch owns (ADR-0004 point 7).
	client.SetTokenRefreshFailureHook(tokenFailureReporter(ctx, guard, logger))

	return &App{
		cfg:       cfg,
		logger:    logger,
		sup:       NewSupervisor(logger),
		client:    client,
		db:        db,
		sentinel:  db,
		sink:      sink,
		guard:     guard,
		rec:       rec,
		submitter: submitter,
	}, nil
}

// Guard exposes the kill switch for operator tooling and tests.
func (a *App) Guard() *killswitch.Switch { return a.guard }

// Submitter exposes the dormant submit path. It has no caller yet: the strategy
// that produces intents is out of scope, and this is the seam it will attach to.
func (a *App) Submitter() *order.Submitter { return a.submitter }

// Close releases the durable resources without running the sentinel judgment.
// It is for assembly-time teardown and tests; a real shutdown goes through Run,
// which decides the sentinel BEFORE closing anything.
func (a *App) Close() error {
	return errors.Join(a.sink.Close(), a.db.Close())
}

// Run boots the bot, blocks until ctx is cancelled, and shuts down gracefully.
// It returns nil for an ordinary shutdown; the sentinel decision and any close
// failures are logged rather than turned into an exit code, because a
// supervisor restarting on a non-zero exit would just loop.
//
// Boot order (ADR-0012 Decision 1(c), ADR-0004 point 3):
//
//	sentinel read → conservative halt decision → sentinel flip to running
//	→ reconciler boot scan → replay gate opens
//
// Boot owns the first three steps and only then starts recovery, so the gate
// can never open over a stale clean marker.
//
// Shutdown order:
//
//	ctx cancelled → supervised loops drain → clean-eligibility decision
//	→ conditional clean sentinel → audit close → store close
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("toss-trade-bot starting",
		"base_url", a.cfg.BaseURL,
		"store_path", a.cfg.StorePath,
		"audit_dir", a.cfg.AuditDir)

	decision := Boot(ctx, a.sentinel, a.guard, a.logger, func() {
		// reconciler.Run performs the boot scan and only then opens the replay
		// gate, then keeps reconciling until ctx is cancelled. It runs under the
		// supervisor so a panic is contained rather than fatal.
		a.sup.Go("reconciler", func() {
			if err := a.rec.Run(ctx); err != nil && ctx.Err() == nil {
				a.logger.Error("reconciler loop stopped", "err", err)
			}
		})
	})
	if decision.Fatal {
		// The durable running marker never landed, so this process is invisible
		// to the next boot's crash detection. Nothing has been started yet;
		// release the handles and exit non-zero so the supervisor retries.
		a.logger.Error("boot refused: the clean-shutdown sentinel could not be marked running",
			"err", decision.Err)
		if cerr := a.Close(); cerr != nil {
			a.logger.Error("failed to release resources after a refused boot", "err", cerr)
		}
		return fmt.Errorf("runtime: boot refused: %w", decision.Err)
	}

	a.logger.Info("boot complete",
		"previous_sentinel", string(decision.Previous),
		"conservative_halt", decision.Conservative)

	<-ctx.Done()

	a.logger.Info("shutting down", "drain_timeout", a.cfg.ShutdownTimeout.String())
	drained := a.sup.Wait(a.cfg.ShutdownTimeout)
	if !drained {
		a.logger.Warn("shutdown drain timed out with goroutines still running; the run will not be certified clean",
			"drain_timeout", a.cfg.ShutdownTimeout.String())
	}

	// The token-refresh escalation hook stays registered through the sentinel
	// decision on purpose. Token flights are detached from the supervisor, so a
	// refresh that failed just before shutdown may still be running its
	// post-flight notification; de-registering here would DROP that report even
	// though the store is still open and could persist it — and the run could
	// then certify itself clean with an uncounted non-reconstructable failure.
	// Leaving it registered is strictly safer: a report that lands before the
	// close is durably counted (and a resulting halt survives via the durable
	// halt row even if the clean was already written), while one that lands
	// after the close fails into an in-memory latch on a process that is exiting
	// anyway — the same residual a crash has.
	sd := Shutdown(ctx, ShutdownPlan{
		Sentinel: a.sentinel,
		Guard:    a.guard,
		Drained:  drained,
		Logger:   a.logger,
		Closers: []NamedCloser{
			{Name: "audit", Close: a.sink.Close},
			{Name: "store", Close: a.db.Close},
		},
	})
	a.logger.Info("shutdown complete", "clean_sentinel", sd.WroteClean, "reason", sd.Reason)
	return nil
}

// tokenFailureGuard is the counted escalation seam for token-refresh failures.
// It is deliberately this narrow — no Trip — so the token path structurally
// cannot bypass the kill switch's counting contract.
type tokenFailureGuard interface {
	ReportTokenRefreshFailure(ctx context.Context, occurredAt time.Time) error
}

// tokenFailureReporter adapts the toss client's refresh-failure hook to the kill
// switch's counted report.
//
// It DETACHES from the process lifetime context on purpose. The hook can fire
// while shutdown is already underway, and inheriting the cancelled context would
// fail the durable counter write — which the kill switch correctly treats as
// fail-closed by latching an unpersisted halt, which in turn would refuse the
// clean sentinel and force every subsequent boot to come up halted. An operator
// outage caused by nothing but a shutdown race is a fail-closed guard firing in
// the wrong direction, so the write gets its own bounded context instead.
func tokenFailureReporter(ctx context.Context, g tokenFailureGuard, logger *slog.Logger) func(time.Time) {
	return func(at time.Time) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenEscalationTimeout)
		defer cancel()
		if err := g.ReportTokenRefreshFailure(rctx, at); err != nil {
			// The kill switch has already failed closed internally (a failed
			// durable counter write latches). Logging is all that is left, and
			// returning normally keeps the token manager's goroutine healthy.
			logger.Error("token refresh failure could not be durably counted (kill switch failed closed)",
				"err", err, "occurred_at", at)
		}
	}
}

// slogNotifier is the halt-notification adapter. The real channel (Slack, email,
// pager) is undecided, so a trip is promoted to an ERROR log — the only
// post-mortem surface an unattended process has.
//
// It satisfies killswitch's non-blocking contract: the notifier is invoked while
// the kill switch holds its transition lock, so this does one synchronous
// structured log write and never calls back into the guard.
type slogNotifier struct{ logger *slog.Logger }

func (n slogNotifier) HaltTripped(reason string) {
	n.logger.Error("KILL SWITCH TRIPPED: new exposure is blocked until a manual clear", "reason", reason)
}

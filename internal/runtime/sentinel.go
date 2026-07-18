package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// shutdownPersistTimeout bounds every durable write on the shutdown path. The
// shutdown path runs on a context DETACHED from the signal context (which is
// already cancelled by the time we get here), so it needs its own bound: a
// wedged store must not keep an unattended process from exiting, but a normal
// local fsync finishes in milliseconds.
const shutdownPersistTimeout = 30 * time.Second

// SentinelStore is the narrow slice of the store seam (#60) that the
// clean-shutdown sentinel judgment consumes. store owns the atomic set/get and
// the halt phase; this package owns the JUDGMENT — when a clean may be written
// and when an unclean boot must come up conservatively halted (ADR-0012
// Decision 1(c): "store exposes an atomic set/get seam only").
//
// It is deliberately this narrow: there is no journal, no counter, and no trip
// entry point here, so the sentinel logic structurally cannot mutate anything
// but the lifecycle value.
type SentinelStore interface {
	Lifecycle(ctx context.Context) (store.LifecycleState, error)
	SetLifecycle(ctx context.Context, s store.LifecycleState) error
	Halt(ctx context.Context) (store.HaltState, error)
}

// SentinelGuard is the narrow slice of the kill switch (#32) the sentinel
// judgment consumes.
//
// It exposes no ClearHalt: clearing a global halt is human-only (ADR-0004
// point 6), so neither the boot nor the shutdown path can ever undo a halt
// while trying to tidy the lifecycle value.
type SentinelGuard interface {
	// BootHalt raises the in-memory conservative block held until a manual
	// clear (ADR-0012 Decision 1(c) initial-authorization gate).
	BootHalt()
	// HasUnpersistedPendingHalt reports an in-memory-only halt — the sticky
	// latch of a trip whose durable write failed, or a bootHalt.
	HasUnpersistedPendingHalt() bool
	// FinalizePendingHalt durably promotes a latched pending halt. It is a
	// deliberate no-op for bootHalt, which is why the caller must re-ask
	// HasUnpersistedPendingHalt afterwards rather than trust a nil error.
	FinalizePendingHalt(ctx context.Context) error
}

// BootDecision is the outcome of the boot-time sentinel judgment, returned for
// logging rather than acted on by the caller — every fail-closed consequence has
// already been applied to the guard by the time it is returned.
type BootDecision struct {
	// Previous is the sentinel value left by the previous run, or "" if it
	// could not be read.
	Previous store.LifecycleState
	// Conservative reports that this boot came up halted and stays fail-closed
	// until an operator clears it.
	Conservative bool
	// Reason explains Conservative in operator-readable terms.
	Reason string
	// Err carries any sentinel read/write failure. It is diagnostic only: the
	// failure has already been converted into a conservative halt.
	Err error
}

// ShutdownDecision is the outcome of the shutdown-time sentinel judgment.
type ShutdownDecision struct {
	// WroteClean reports whether this run certified itself clean. When false,
	// the sentinel stays running and the next boot comes up conservatively
	// halted.
	WroteClean bool
	// Reason explains why a clean was refused (empty when it was written).
	Reason string
	// Err aggregates sentinel and closer failures for logging.
	Err error
}

// BootSentinel performs the boot-time half of the clean-shutdown sentinel
// (ADR-0012 Decision 1(c)) and returns what it decided.
//
// The order is load-bearing and is what the tests pin:
//
//  1. READ the previous lifecycle value — before it is overwritten, or the
//     run would observe its own marker and never detect an unclean predecessor.
//  2. DECIDE conservatively: anything other than a successfully-read clean
//     (running, an unreadable value, an unrecognised value) means the previous
//     run never certified itself, so a non-reconstructable global halt — one
//     whose durable write failed and which no journal replay can re-derive —
//     cannot be excluded. That is a conservative halted boot held until a
//     manual clear, not a warning.
//  3. FLIP to running atomically, overwriting any stale clean. A failed flip is
//     itself fail-closed: without a durable running marker, a crash in this run
//     would look clean to the next boot.
//
// Note on a first-ever boot: the store's fresh lifecycle row is running by
// design, so a brand-new deployment comes up conservatively halted and requires
// an explicit operator clear before it can place its first order. That is the
// intended initial-authorization gate, not a bug.
func BootSentinel(ctx context.Context, st SentinelStore, g SentinelGuard, logger *slog.Logger) BootDecision {
	var d BootDecision

	// (1) read before write.
	prev, err := st.Lifecycle(ctx)
	if err != nil {
		d.Err = fmt.Errorf("read clean-shutdown sentinel: %w", err)
		d.Conservative = true
		d.Reason = "sentinel unreadable: cannot prove the previous run shut down cleanly"
	} else {
		d.Previous = prev
		if prev != store.LifecycleClean {
			d.Conservative = true
			d.Reason = fmt.Sprintf("previous run ended unclean (sentinel=%q): an unpersisted global halt cannot be excluded", prev)
		}
	}

	// (2) fail closed BEFORE the flip, so no window exists in which the flip
	// succeeded but the block has not been raised yet.
	if d.Conservative {
		g.BootHalt()
		logger.Error("conservative halted boot: new exposure stays blocked until a manual clear",
			"reason", d.Reason, "previous_sentinel", string(d.Previous))
	}

	// (3) flip to running — attempted even when the read failed, so the NEXT
	// boot can still detect this run as unclean.
	if err := st.SetLifecycle(ctx, store.LifecycleRunning); err != nil {
		setErr := fmt.Errorf("mark clean-shutdown sentinel running: %w", err)
		d.Err = errors.Join(d.Err, setErr)
		if !d.Conservative {
			d.Conservative = true
			d.Reason = "sentinel could not be marked running: a crash in this run would look clean to the next boot"
			g.BootHalt()
		}
		logger.Error("clean-shutdown sentinel could not be marked running; booting halted",
			"err", setErr)
	}

	return d
}

// Boot runs the sentinel judgment and only then starts recovery.
//
// The sequencing is the point (ADR-0012 Decision 1(c) sentinel fail-open #1):
// startRecovery eventually opens the replay gate, so the durable running flip
// must already have landed. Callers cannot get this wrong by accident because
// Boot owns both halves — there is no exported path that starts recovery
// without first flipping the sentinel.
func Boot(ctx context.Context, st SentinelStore, g SentinelGuard, logger *slog.Logger, startRecovery func()) BootDecision {
	d := BootSentinel(ctx, st, g, logger)
	if startRecovery != nil {
		startRecovery()
	}
	return d
}

// ShutdownSentinel decides whether this run has earned a clean sentinel and
// writes it if so (ADR-0012 Decision 1(c) shutdown rule).
//
// A clean is written only when ALL of the following hold:
//
//   - drained: the supervised loops actually finished. A drain timeout means a
//     goroutine may still be mutating state, so the exit is not a proven-normal
//     path.
//   - no in-memory-only halt survives. If the guard reports one, we first try
//     to make it durable; a failure refuses the clean (sentinel fail-open #2).
//     We then ASK AGAIN, because FinalizePendingHalt is a no-op for a bootHalt:
//     trusting its nil error would let a boot-halted run certify itself clean
//     and silently reopen on the next boot.
//   - no durable pending/halted global halt. This arm is read-only — an
//     unresolved halt refuses the clean with no write entry point at all — and
//     an unreadable phase refuses it too (state unknown is blocked, ADR-0004
//     point 3).
//
// Refusing simply leaves the sentinel at running, which is exactly what makes
// the next boot conservative.
func ShutdownSentinel(ctx context.Context, st SentinelStore, g SentinelGuard, drained bool, logger *slog.Logger) ShutdownDecision {
	var d ShutdownDecision

	refuse := func(reason string, err error) ShutdownDecision {
		d.Reason = reason
		d.Err = errors.Join(d.Err, err)
		logger.Warn("clean-shutdown sentinel refused; next boot will come up conservatively halted",
			"reason", reason, "err", err)
		return d
	}

	if !drained {
		return refuse("shutdown drain timed out: supervised work may still be in flight", nil)
	}

	// (i) in-memory-only halt: persist it, then re-ask.
	if g.HasUnpersistedPendingHalt() {
		if err := g.FinalizePendingHalt(ctx); err != nil {
			return refuse("an unpersisted global halt could not be made durable", fmt.Errorf("finalize pending halt: %w", err))
		}
		if g.HasUnpersistedPendingHalt() {
			// A bootHalt (or any latch finalize cannot promote) is still
			// standing. It is in-memory by contract, so the ONLY way to carry it
			// across the restart is to leave the sentinel unclean.
			return refuse("an in-memory halt (e.g. a conservative boot halt) is still standing and cannot be persisted", nil)
		}
	}

	// (ii) durable unresolved halt: read-only refusal.
	halt, err := st.Halt(ctx)
	if err != nil {
		return refuse("global halt phase unreadable: an unresolved halt cannot be excluded", fmt.Errorf("read halt phase: %w", err))
	}
	if halt.Phase != store.HaltNone {
		return refuse(fmt.Sprintf("durable global halt is %s and needs a manual clear", halt.Phase), nil)
	}

	if err := st.SetLifecycle(ctx, store.LifecycleClean); err != nil {
		return refuse("clean sentinel write failed", fmt.Errorf("write clean sentinel: %w", err))
	}

	d.WroteClean = true
	logger.Info("clean-shutdown sentinel written: next boot may trust durable state")
	return d
}

// NamedCloser is one shutdown sink. The name is for diagnostics only — a
// failing close in an unattended process is otherwise anonymous in the logs.
type NamedCloser struct {
	Name  string
	Close func() error
}

// ShutdownPlan configures Shutdown.
type ShutdownPlan struct {
	Sentinel SentinelStore
	Guard    SentinelGuard
	// Drained reports whether the supervisor drained within its budget.
	Drained bool
	// Closers are closed in slice order AFTER the sentinel decision. Production
	// order is audit then store: the sentinel judgment reads the halt phase and
	// writes the lifecycle value, so the store must still be open.
	Closers []NamedCloser
	Logger  *slog.Logger
	// Timeout bounds the detached durable writes. Zero means
	// shutdownPersistTimeout.
	Timeout time.Duration
}

// Shutdown runs the graceful-shutdown tail: the conditional clean sentinel
// first, then every sink in order.
//
// It detaches from the caller's context on purpose. By the time shutdown runs,
// the signal context is ALREADY cancelled — reusing it would make every durable
// write (finalize, clean) fail with context.Canceled, so the process would
// silently never certify itself clean and every restart would come up halted.
// The detached context is bounded by Timeout so a wedged store cannot hold the
// process open forever.
//
// Every closer runs even if an earlier one failed: refusing the clean is a
// safety decision, not a reason to strand a file handle. Errors are aggregated
// into the decision.
func Shutdown(ctx context.Context, p ShutdownPlan) ShutdownDecision {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = shutdownPersistTimeout
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	d := ShutdownSentinel(sctx, p.Sentinel, p.Guard, p.Drained, p.Logger)

	for _, c := range p.Closers {
		if c.Close == nil {
			continue
		}
		if err := c.Close(); err != nil {
			d.Err = errors.Join(d.Err, fmt.Errorf("close %s: %w", c.Name, err))
			p.Logger.Error("shutdown sink failed to close", "sink", c.Name, "err", err)
		}
	}
	return d
}

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// reasonBootScanFailed is the fail-closed promotion reason when the restart scan
// itself could not run. ADR-0004 point 3 is explicit that a kill switch which
// cannot rebuild its state boots BLOCKED, not "no evidence found".
const reasonBootScanFailed = "reconciler-boot-scan-failed"

// ErrTickerStopped is returned by Run when the injected re-evaluation ticker
// channel closed. Every bounded-ness claim in ADR-0014 rests on that cadence
// continuing to run, so its disappearance is a fail-closed condition, not a quiet
// exit.
var ErrTickerStopped = errors.New("reconciler: re-evaluation ticker stopped")

// BootScan runs the two sequential restart passes and opens the replay gate
// between them (ADR-0014 Decision 9).
//
// The gate is opened only after pass 1 has injected every re-derived block. Per
// ADR-0004 point 4 the per-symbol ambiguous blocks are deliberately NOT
// persisted — they are re-derived from the journal — so opening the gate before
// that re-derivation would make a restart itself the bypass of the per-symbol
// protection.
//
// If pass 1 cannot run at all, the gate is NOT opened and the reconciler promotes
// to a fail-closed halt: a scan that failed is "state unknown", which ADR-0004
// point 3 requires to be treated as blocked.
func (r *Reconciler) BootScan(ctx context.Context) error {
	// Pass 1 — marker branching, ambiguous floor, backlog escalation, auto-clear.
	if err := r.reconcile(ctx); err != nil {
		r.promoteFailClosed(ctx, reasonBootScanFailed)
		return fmt.Errorf("reconciler: boot scan pass 1 failed (fail-closed, gate left shut): %w", err)
	}

	// Every re-derived block is now published — only now may new exposure resume.
	r.guard.NotifyScanComplete()
	r.logger.Info("restart scan complete: replay gate opened")

	// Pass 2 — audit re-emit driver. Safe after the gate: it creates no new
	// exposure, and its only blocking effect (an audit fail-closed escalation) is a
	// durable global halt that re-blocks anyway.
	if err := r.reemitAudit(ctx); err != nil {
		return fmt.Errorf("reconciler: boot scan pass 2 failed: %w", err)
	}
	return nil
}

// Reconcile runs one live reconciliation cycle: the same marker branching,
// ambiguous policy and auto-clear as the boot scan (minus the gate transition),
// followed by the audit re-emit convergence.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	if err := r.reconcile(ctx); err != nil {
		return err
	}
	return r.reemitAudit(ctx)
}

// Run is the supervised long-running loop: one boot scan, then a reconciliation
// on every tick and on every submit-path wake, until ctx is cancelled.
//
// Both the ticker and the wake matter, and neither substitutes for the other. The
// wake converges a just-submitted ambiguous intent immediately, but it only fires
// when something is being submitted — in a quiet market nothing would ever
// re-evaluate, and the delayed-halt windows ADR-0013 booked as "bounded" would be
// unbounded. The ticker closes exactly that gap (ADR-0014 Decision 11).
//
// #36 runs this under a supervisor. Re-entering Run after a supervised restart is
// safe: the boot scan is idempotent (it derives everything from the journal) and
// re-opening an already-open replay gate is a no-op.
func (r *Reconciler) Run(ctx context.Context) error {
	ticks := r.ticks
	if ticks == nil {
		t := time.NewTicker(r.reevalInterval)
		defer t.Stop()
		ticks = t.C
	}

	r.runCycle(ctx, r.BootScan)

	for {
		select {
		case <-ctx.Done():
			// A cancelled context is an ordinary shutdown, not an unsustainable loop:
			// no fail-closed promotion here.
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				// The cadence that bounds every delayed-halt window is gone. Stop the
				// bot from creating new exposure rather than running on without it
				// (ADR-0014 Decision 12).
				r.promoteFailClosed(ctx, reasonLoopUnsustainable)
				return ErrTickerStopped
			}
			r.runCycle(ctx, r.Reconcile)
		case <-r.wake:
			r.runCycle(ctx, r.Reconcile)
		}
	}
}

// runCycle executes one cycle inside a recover boundary and accounts sustained
// failure (ADR-0014 Decision 12).
//
// A single failed cycle is not an emergency — a store hiccup or a panic in one
// dependency should not halt the bot, and the next tick simply retries. Sustained
// failure IS an emergency: the loop is what keeps the escalation windows bounded,
// so once it has failed maxConsecutiveCycleFailures times in a row the reconciler
// promotes itself to a fail-closed halt instead of silently leaving those windows
// open.
func (r *Reconciler) runCycle(ctx context.Context, fn func(context.Context) error) {
	err := guardedCycle(ctx, fn)
	if err == nil {
		r.mu.Lock()
		r.consecutiveFailures = 0
		r.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		// The cycle failed because the process is shutting down. Not a sick loop.
		r.logger.Info("reconciliation cycle aborted by context cancellation", "error", err)
		return
	}

	r.mu.Lock()
	r.consecutiveFailures++
	n := r.consecutiveFailures
	r.mu.Unlock()

	r.logger.Error("reconciliation cycle failed", "consecutive_failures", n, "error", err)
	if n >= maxConsecutiveCycleFailures {
		r.promoteFailClosed(ctx, reasonLoopUnsustainable)
	}
}

// guardedCycle is the per-cycle recover boundary. A panic in one cycle (a
// derived panic inside a re-drive, a misbehaving dependency) must not kill the
// goroutine that every bounded-ness claim depends on — it becomes an error the
// caller accounts for.
func guardedCycle(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("reconciler: recovered panic in reconciliation cycle: %v\n%s", rec, debug.Stack())
		}
	}()
	return fn(ctx)
}

// promoteFailClosed stops new exposure when the reconciler cannot do its job
// (ADR-0014 Decision 12 / ADR-0004 point 3).
//
// BootHalt runs FIRST and unconditionally: it is an in-memory block that cannot
// fail, so the bot is blocked even if the durable trip below cannot be written.
// The durable Trip follows so the block survives a restart. The promotion is
// latched so a wedged loop does not re-trip on every cycle.
func (r *Reconciler) promoteFailClosed(ctx context.Context, reason string) {
	r.mu.Lock()
	already := r.promoted
	r.promoted = true
	r.mu.Unlock()
	if already {
		return
	}

	r.logger.Error("reconciler cannot sustain itself — promoting to fail-closed halt", "reason", reason)
	r.guard.BootHalt()
	r.tripGlobal(ctx, reason, r.now())
}

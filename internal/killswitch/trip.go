package killswitch

import (
	"context"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Trip is the generic trigger (ADR-0004 point 7): global halts durably (via the
// durable-before-visible 2-phase flow) and notifies; per-symbol blocks in memory
// only and, when ambiguous outcomes become frequent, escalates to a global halt.
// Risk sources should prefer the named Report* methods (they carry the counting
// and ordering contracts); Trip is the low-level entry the reconciler (#35) uses
// to re-derive per-symbol blocks and this package uses internally to escalate.
func (g *Guard) Trip(ctx context.Context, scope Scope, reason string, occurredAt time.Time) error {
	if scope.global {
		return g.tripGlobal(ctx, reason, occurredAt)
	}
	return g.tripSymbol(ctx, scope.symbol, reason, occurredAt)
}

// tripGlobal is the durable-before-visible 2-phase global trip (ADR-0012
// Decision 1). It (1) exposes the mirror as pending so CanSubmit fails closed
// during the in-flight window, (2) commits MarkHaltPending (none→pending), (3)
// commits TripHalt (pending→halted), and only then (4) exposes the mirror as
// halted and notifies. If either durable commit fails, the mirror stays pending
// (blocked) — it is never reverted to unhalted (a failed durable write is
// "blocked", not "no evidence").
func (g *Guard) tripGlobal(ctx context.Context, reason string, occurredAt time.Time) error {
	g.mu.Lock()
	if g.mirrorPhase == phaseHalted && g.durablePhase == store.HaltHalted {
		g.mu.Unlock() // already fully halted and durable; idempotent no-op
		return nil
	}
	// Pre-block: expose pending (fail-closed) BEFORE any durable write. This is
	// not "mirror-first" — pending exposes no *completed* halt, it only closes the
	// in-flight window fail-closed (ADR-0012 Decision 1).
	g.mirrorPhase = phasePending
	g.haltReason = reason
	g.gen++
	g.mu.Unlock()

	if err := g.commitPhase(ctx, store.HaltPending, reason); err != nil {
		return fmt.Errorf("killswitch: mark halt pending: %w", err) // mirror stays pending (blocked)
	}
	if err := g.commitPhase(ctx, store.HaltHalted, reason); err != nil {
		return fmt.Errorf("killswitch: trip halt: %w", err) // mirror stays pending (blocked); durable is pending
	}

	g.mu.Lock()
	g.mirrorPhase = phaseHalted
	g.haltReason = reason
	g.mu.Unlock()
	g.notify(reason, occurredAt)
	return nil
}

// tripSymbol blocks one symbol in memory and records the ambiguous occurrence.
// When ambiguous outcomes become frequent within the window it escalates to a
// global halt (ADR-0004 point 7). The ambiguous window is in-memory and
// non-persisted: on restart the reconciler re-Trip's from the journal scan with
// the original occurredAt, re-accumulating the window deterministically.
func (g *Guard) tripSymbol(ctx context.Context, symbol, reason string, occurredAt time.Time) error {
	g.mu.Lock()
	if g.blockedSymbols == nil {
		g.blockedSymbols = make(map[string]string)
	}
	g.blockedSymbols[symbol] = reason
	escalate := g.recordAmbiguousLocked(occurredAt)
	alreadyHalted := g.mirrorPhase != phaseNone
	g.mu.Unlock()

	if escalate && !alreadyHalted {
		return g.tripGlobal(ctx, reasonFrequentAmbiguous, occurredAt)
	}
	return nil
}

// recordAmbiguousLocked appends occurredAt, prunes entries outside the window
// relative to it, and reports whether the count reached the escalation
// threshold. Caller holds g.mu.
func (g *Guard) recordAmbiguousLocked(at time.Time) (escalate bool) {
	cutoff := at.Add(-g.cfg.AmbiguousWindow)
	kept := g.ambiguous[:0]
	for _, ts := range g.ambiguous {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	g.ambiguous = append(kept, at)
	return len(g.ambiguous) >= g.cfg.AmbiguousThreshold
}

// commitPhase durably commits one halt phase transition in the guard's own
// transaction (ADR-0012 Decision 2) and, on success, advances the observed
// durable phase. On failure it returns the error and leaves durablePhase
// untouched so the mirror stays blocked (fail-closed).
func (g *Guard) commitPhase(ctx context.Context, phase store.HaltPhase, reason string) error {
	err := g.store.Atomically(ctx, func(tx store.Tx) error {
		switch phase {
		case store.HaltPending:
			return tx.MarkHaltPending(ctx, reason)
		case store.HaltHalted:
			return tx.TripHalt(ctx, reason)
		default:
			return fmt.Errorf("killswitch: unsupported halt phase %q", phase)
		}
	})
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.durablePhase = phase
	g.mu.Unlock()
	return nil
}

// markHalted updates the mirror to halted AFTER the durable commit has already
// succeeded (used by the count-first Report* paths, which commit the halt inside
// their own single transaction). Idempotent: a repeated report while already
// halted neither re-bumps the generation nor re-notifies.
func (g *Guard) markHalted(reason string, at time.Time) {
	g.mu.Lock()
	already := g.mirrorPhase == phaseHalted
	g.mirrorPhase = phaseHalted
	g.durablePhase = store.HaltHalted
	if !already {
		g.haltReason = reason
		g.gen++
	}
	g.mu.Unlock()
	if !already {
		g.notify(reason, at)
	}
}

// ReportOrderFailure is the count-before-resolve entry for a failed order
// submission (ADR-0012 Decision 3). In ONE killswitch transaction it increments
// the consecutive-failure counter and, at threshold, trips the global halt — and
// it does so BEFORE the caller resolves the intent. The caller MUST wait for this
// to return nil before resolving; on error it must NOT resolve (leave the intent
// unresolved so the reconciler re-drives and re-counts). Over-counting from a
// restart re-report is tolerated (over-halt = safe).
func (g *Guard) ReportOrderFailure(ctx context.Context, occurredAt time.Time) error {
	tripped := false
	err := g.store.Atomically(ctx, func(tx store.Tx) error {
		c, err := tx.Counter(ctx, counterOrderFailures)
		if err != nil {
			return err
		}
		c.Name = counterOrderFailures
		c.Value++
		if err := tx.SetCounter(ctx, c); err != nil {
			return err
		}
		if c.Value >= int64(g.cfg.OrderFailureThreshold) {
			tripped = true
			return tx.TripHalt(ctx, reasonOrderFailures)
		}
		return nil
	})
	if err != nil {
		// fail-closed: nothing committed (rolled back). The caller must not resolve.
		return fmt.Errorf("killswitch: report order failure: %w", err)
	}
	if tripped {
		g.markHalted(reasonOrderFailures, occurredAt)
	}
	return nil
}

// ReportOrderSuccess durably resets the consecutive order-failure counter
// (ADR-0012 Decision 4). It needs no atomic coupling or ordering contract — a
// missed reset only over-counts (over-halt = safe). It does NOT clear a global
// halt already tripped: that is manual-only (ADR-0004 point 6). The caller is the
// success-confirming path (reconciler FILLED confirmation, #35).
func (g *Guard) ReportOrderSuccess(ctx context.Context) error {
	if err := g.store.Atomically(ctx, func(tx store.Tx) error {
		return tx.SetCounter(ctx, store.Counter{Name: counterOrderFailures, Value: 0})
	}); err != nil {
		return fmt.Errorf("killswitch: reset order-failure counter: %w", err)
	}
	return nil
}

// ReportTokenRefreshFailure is the counted, count-first entry for a token-refresh
// failure (ADR-0004 point 7 counter+window, ADR-0012 count-first). In ONE
// killswitch transaction it increments a persistent windowed counter and, at
// threshold, trips the global halt. The counter is reconstruction-resistant
// (persisted) so a restart below threshold does not reset it. #36 wires the token
// manager's refresh-failure seam to this method (not to a direct Trip) so the
// counting/persistence contract holds.
func (g *Guard) ReportTokenRefreshFailure(ctx context.Context, occurredAt time.Time) error {
	tripped := false
	err := g.store.Atomically(ctx, func(tx store.Tx) error {
		c, err := tx.Counter(ctx, counterTokenRefresh)
		if err != nil {
			return err
		}
		c.Name = counterTokenRefresh
		// Windowed: start a fresh window when the previous one has elapsed relative
		// to occurredAt, otherwise accumulate.
		if c.WindowStart.IsZero() || occurredAt.Sub(c.WindowStart) > g.cfg.TokenRefreshWindow {
			c.WindowStart = occurredAt
			c.Value = 1
		} else {
			c.Value++
		}
		if err := tx.SetCounter(ctx, c); err != nil {
			return err
		}
		if c.Value >= int64(g.cfg.TokenRefreshFailureThreshold) {
			tripped = true
			return tx.TripHalt(ctx, reasonTokenRefresh)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("killswitch: report token refresh failure: %w", err)
	}
	if tripped {
		g.markHalted(reasonTokenRefresh, occurredAt)
	}
	return nil
}

// ClearSymbol removes a per-symbol block (memory-only). The reconciler (#35)
// calls it when the ambiguous condition that blocked the symbol has resolved
// (ADR-0004 point 6 automatic per-symbol clear).
func (g *Guard) ClearSymbol(symbol string) {
	g.mu.Lock()
	delete(g.blockedSymbols, symbol)
	g.mu.Unlock()
}

// ClearHalt is the manual global reset (ADR-0004 point 6 — the only path that
// un-halts; there is no automatic resume). It is symmetric to the trip: the
// mirror un-halts only AFTER ClearHalt commits durably; a failed commit keeps the
// halt (fail-closed). Clearing reopens the replay gate if the scan already
// completed (gate = scanComplete && no global halt).
func (g *Guard) ClearHalt(ctx context.Context) error {
	if err := g.store.Atomically(ctx, func(tx store.Tx) error {
		return tx.ClearHalt(ctx)
	}); err != nil {
		return fmt.Errorf("killswitch: clear halt: %w", err) // fail-closed: halt stays
	}
	g.mu.Lock()
	g.mirrorPhase = phaseNone
	g.durablePhase = store.HaltNone
	g.haltReason = ""
	// No generation bump: clearing is an unblock, so it must not invalidate
	// in-flight reservations for still-allowed symbols.
	g.mu.Unlock()
	return nil
}

// BootHalt is the conservative boot-halt affordance #36 drives (ADR-0012
// Decision 1(c)): when an unclean shutdown means a durable-trip-failed pending
// global halt cannot be ruled out, #36 calls this to force the guard halted and
// keep the replay gate closed until an explicit ClearHalt. It is in-memory only —
// re-derived from the sentinel each boot — so it does not write durable state and
// does not make the graceful-shutdown query report an unpersisted pending (that
// query fires only for a *pending* mirror; this sets halted).
func (g *Guard) BootHalt(reason string, at time.Time) {
	g.mu.Lock()
	already := g.mirrorPhase == phaseHalted
	g.mirrorPhase = phaseHalted
	if !already {
		g.haltReason = reason
		g.gen++
	}
	g.mu.Unlock()
	if !already {
		g.notify(reason, at)
	}
}

// HasUnpersistedPendingHalt reports whether the guard holds an in-memory pending
// global halt the store does not yet reflect — i.e. a trip whose MarkHaltPending
// never committed (store fully down at the trip instant). #36 queries this at
// shutdown because such a halt is invisible to a store read (Halt().Phase==none):
// if true, #36 must not record a clean shutdown without first finalizing it. A
// pending that DID persist (Halt().Phase==pending) returns false here — #36
// detects that one directly via the store read.
func (g *Guard) HasUnpersistedPendingHalt() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mirrorPhase == phasePending && g.durablePhase == store.HaltNone
}

// FinalizePendingHalt durably persists an in-memory pending halt now (the
// graceful-shutdown finalize #36 drives, ADR-0012 Decision 1(c)). It re-runs
// MarkHaltPending→TripHalt. On success the pending becomes a durable, restart-
// surviving halt; on failure the halt is left as it was (still blocked) so #36
// refuses to record a clean shutdown — via this query if MarkHaltPending still
// failed, or via the store read if only TripHalt failed (durable is then pending).
func (g *Guard) FinalizePendingHalt(ctx context.Context) error {
	g.mu.Lock()
	if g.mirrorPhase != phasePending {
		g.mu.Unlock() // nothing pending to finalize (none, or already halted)
		return nil
	}
	reason := g.haltReason
	g.mu.Unlock()

	if err := g.commitPhase(ctx, store.HaltPending, reason); err != nil {
		return fmt.Errorf("killswitch: finalize pending halt (mark): %w", err)
	}
	if err := g.commitPhase(ctx, store.HaltHalted, reason); err != nil {
		return fmt.Errorf("killswitch: finalize pending halt (trip): %w", err)
	}
	g.mu.Lock()
	g.mirrorPhase = phaseHalted
	g.mu.Unlock()
	return nil
}

package killswitch

import (
	"context"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Global-halt transition invariants (I1–I6). Every path that touches the global
// halt upholds all of these; the audit at the bottom of this file maps each path
// to them.
//
//	I1 fail-closed immediacy: a path that may cause a global halt sets the mirror
//	   blocked (pending) under mu — a fast, wait-free step — BEFORE any slow wait
//	   (haltMu or a store write). So a danger is never invisible while the path
//	   waits.
//	I2 no-clobber: a path lowers the mirror only if the pending it lowers is still
//	   its own (generation unchanged). A concurrent trip bumps the generation when
//	   it pre-blocks, so its pending is never mistaken for a stale one.
//	I3 durable-before-visible: the mirror is lifted to halted only AFTER the durable
//	   TripHalt commits. Pending (blocked, more conservative) is exposed before that.
//	I4 transition ordering: durable transitions (trip vs clear) serialize on haltMu,
//	   so there is no observe-then-transition window between them.
//	I5 hot-path non-blocking: CanSubmit/Reserve/Reconfirm take only mu and never
//	   wait on the store or haltMu.
//	I6 non-halting work never delays a trip: work that does not transition the halt
//	   (ReportOrderSuccess counter reset) takes neither haltMu nor a pre-block, so a
//	   slow reset cannot stall a concurrent trip's fail-closed immediacy (I1).

// Trip is the generic trigger (ADR-0004 point 7): global halts durably (via the
// durable-before-visible 2-phase flow) and notifies; per-symbol blocks in memory
// only and, when ambiguous outcomes become frequent, escalates to a global halt.
// Risk sources should prefer the named Report* methods (they carry the counting
// contracts); Trip is the low-level entry the reconciler (#35) uses to re-derive
// per-symbol blocks and this package uses internally to escalate.
func (g *Guard) Trip(ctx context.Context, scope Scope, reason string, occurredAt time.Time) error {
	if scope.global {
		return g.tripGlobal(ctx, reason, occurredAt)
	}
	return g.tripSymbol(ctx, scope.symbol, reason, occurredAt)
}

// tripGlobal is the manual/generic global trip. It pre-blocks the mirror (I1)
// before taking haltMu for the durable write (I4).
func (g *Guard) tripGlobal(ctx context.Context, reason string, occurredAt time.Time) error {
	g.ensureBlocked(reason) // I1: fast, before the slow haltMu/store waits
	g.haltMu.Lock()
	defer g.haltMu.Unlock()
	return g.tripDurableLocked(ctx, reason, occurredAt)
}

// tripSymbol blocks one symbol in memory and records the ambiguous occurrence.
// When ambiguous outcomes become frequent within the window it escalates to a
// global halt (ADR-0004 point 7). The ambiguous window is in-memory and
// non-persisted: on restart the reconciler re-Trip's from the journal scan with
// the original occurredAt, re-accumulating the window deterministically.
//
// There is deliberately no "already halted?" pre-check on the escalation: the
// durable no-op inside tripDurableLocked makes a redundant escalation cheap, and
// pre-blocking + haltMu serialization guarantee a count-first speculative pending
// can neither suppress this escalation nor be clobbered by it. Removing that check
// is what closes the speculative-pending-suppresses-escalation fail-open.
func (g *Guard) tripSymbol(ctx context.Context, symbol, reason string, occurredAt time.Time) error {
	g.mu.Lock()
	if g.blockedSymbols == nil {
		g.blockedSymbols = make(map[string]string)
	}
	g.blockedSymbols[symbol] = reason
	escalate := g.recordAmbiguousLocked(occurredAt)
	g.mu.Unlock()

	if !escalate {
		return nil
	}
	g.ensureBlocked(reasonFrequentAmbiguous) // I1
	g.haltMu.Lock()
	defer g.haltMu.Unlock()
	return g.tripDurableLocked(ctx, reasonFrequentAmbiguous, occurredAt)
}

// recordAmbiguousLocked appends occurredAt, prunes entries outside the window
// relative to it, and reports whether the count reached the escalation threshold.
// Caller holds mu.
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

// tripDurableLocked performs the durable side of a global trip. Caller holds
// haltMu and has already ensureBlocked (mirror pending/halted). If the guard is
// already durably halted it is an idempotent no-op. Otherwise it commits
// MarkHaltPending→TripHalt and lifts the mirror to halted (I3). On a durable error
// the mirror stays blocked (fail-closed) — never reverted here.
func (g *Guard) tripDurableLocked(ctx context.Context, reason string, occurredAt time.Time) error {
	g.mu.Lock()
	durHalted := g.durablePhase == store.HaltHalted
	g.mu.Unlock()
	if durHalted {
		// Already durably halted: ensure the mirror shows halted (idempotent) and
		// stop — re-committing would just churn the durable row.
		g.mu.Lock()
		g.mirrorPhase = phaseHalted
		g.mu.Unlock()
		return nil
	}

	if err := g.commitPhase(ctx, store.HaltPending, reason); err != nil {
		return fmt.Errorf("killswitch: mark halt pending: %w", err) // mirror stays blocked
	}
	if err := g.commitPhase(ctx, store.HaltHalted, reason); err != nil {
		return fmt.Errorf("killswitch: trip halt: %w", err) // mirror stays blocked; durable is pending
	}
	g.exposeHalted(reason, occurredAt)
	return nil
}

// ensureBlocked implements I1: it makes the mirror blocked and takes generation
// ownership under mu — fast and wait-free — so a trip-causing path is fail-closed
// before it waits on haltMu or the store. It never downgrades a halted mirror
// (halted is already blocked); it always bumps the generation so a concurrent
// clear/revert can tell this pending apart from a stale one (I2). Returns the
// owned generation (used by the count-first revert path).
func (g *Guard) ensureBlocked(reason string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mirrorPhase == phaseNone {
		g.mirrorPhase = phasePending
		g.haltReason = reason
	}
	g.gen++
	return g.gen
}

// exposeHalted lifts the mirror to halted after a durable TripHalt commit
// (I3 durable-before-visible). Caller holds haltMu, so no concurrent transition
// can intervene; a concurrent trip that bumped the generation also wants halted,
// so this needs no generation guard. It notifies only on the edge into halted.
func (g *Guard) exposeHalted(reason string, at time.Time) {
	g.mu.Lock()
	already := g.mirrorPhase == phaseHalted
	g.mirrorPhase = phaseHalted
	g.durablePhase = store.HaltHalted
	if !already {
		g.haltReason = reason
	}
	g.mu.Unlock()
	if !already {
		g.notify(reason, at)
	}
}

// revertOwnedPending undoes a count-first path's pre-block when its tx did not
// trip (count below threshold). Caller holds haltMu. It reverts ONLY if the
// pending is still ours — mirror still pending AND generation unchanged (I2). A
// concurrent trip that pre-blocked bumped the generation and took ownership, so
// its pending survives.
func (g *Guard) revertOwnedPending(gen uint64) {
	g.mu.Lock()
	if g.mirrorPhase == phasePending && g.gen == gen {
		g.mirrorPhase = phaseNone
		g.haltReason = ""
		g.durablePhase = store.HaltNone
	}
	g.mu.Unlock()
}

// commitPhase durably commits one halt phase transition in the guard's own
// transaction (ADR-0012 Decision 2). Caller holds haltMu. On success it advances
// the observed durable phase; on failure it leaves durablePhase untouched so the
// mirror stays blocked (fail-closed).
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

// ReportOrderFailure is the count-before-resolve entry for a failed order
// submission (ADR-0012 Decision 3). It pre-blocks the mirror (I1) and then, in ONE
// killswitch transaction, increments the consecutive-failure counter and — at
// threshold — trips the global halt, all BEFORE the caller resolves the intent.
// The caller MUST wait for a nil return before resolving; on error it must NOT
// resolve (leave the intent unresolved so the reconciler re-drives and re-counts).
// Over-counting from a restart re-report is tolerated (over-halt = safe).
//
// The pre-block is unconditional: the report does not know whether it will cross
// the threshold until it reads the counter inside the tx, so it blocks first
// (fail-closed) and reverts afterward if the count stayed below threshold. This
// briefly blocks new submissions on every failure report — an intentional
// over-block in the safe direction (ADR-0012). The counter is read inside the tx,
// so the store's single writer makes the threshold decision exact without a
// separate pre-read lock.
func (g *Guard) ReportOrderFailure(ctx context.Context, occurredAt time.Time) error {
	gen := g.ensureBlocked(reasonOrderFailures) // I1
	g.haltMu.Lock()
	defer g.haltMu.Unlock()

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
		// fail-closed: nothing committed. The pre-block stays (blocked) since we
		// cannot rule out a threshold crossing. The caller must not resolve.
		return fmt.Errorf("killswitch: report order failure: %w", err)
	}
	if tripped {
		g.exposeHalted(reasonOrderFailures, occurredAt)
	} else {
		g.revertOwnedPending(gen)
	}
	return nil
}

// ReportOrderSuccess durably resets the consecutive order-failure counter
// (ADR-0012 Decision 4). It is NON-halting: it takes neither a pre-block nor
// haltMu, so a slow reset can never stall a concurrent trip's fail-closed
// immediacy (I6). A missed/misordered reset only over-counts (over-halt = safe),
// so it needs no ordering contract. It does NOT clear a tripped global halt —
// that is manual-only (ADR-0004 point 6). Caller is the success-confirming path
// (reconciler FILLED confirmation, #35).
func (g *Guard) ReportOrderSuccess(ctx context.Context) error {
	if err := g.store.Atomically(ctx, func(tx store.Tx) error {
		return tx.SetCounter(ctx, store.Counter{Name: counterOrderFailures, Value: 0})
	}); err != nil {
		return fmt.Errorf("killswitch: reset order-failure counter: %w", err)
	}
	return nil
}

// ReportTokenRefreshFailure is the counted, count-first entry for a token-refresh
// failure (ADR-0004 point 7 counter+window, ADR-0012 count-first). Like
// ReportOrderFailure it pre-blocks (I1), then in ONE tx increments a persistent
// windowed counter and trips at threshold. The counter is reconstruction-resistant
// (persisted) so a restart below threshold does not reset it. #36 wires the token
// manager's refresh-failure seam to this method (not to a direct Trip) so the
// counting/persistence contract holds.
func (g *Guard) ReportTokenRefreshFailure(ctx context.Context, occurredAt time.Time) error {
	gen := g.ensureBlocked(reasonTokenRefresh) // I1
	g.haltMu.Lock()
	defer g.haltMu.Unlock()

	tripped := false
	err := g.store.Atomically(ctx, func(tx store.Tx) error {
		c, err := tx.Counter(ctx, counterTokenRefresh)
		if err != nil {
			return err
		}
		c = projectTokenCounter(c, occurredAt, g.cfg.TokenRefreshWindow)
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
		g.exposeHalted(reasonTokenRefresh, occurredAt)
	} else {
		g.revertOwnedPending(gen)
	}
	return nil
}

// projectTokenCounter applies the windowed increment: a fresh window (value 1)
// when the previous one has elapsed relative to occurredAt, otherwise value++.
// Applied inside the tx so it is exact against the persisted counter.
func projectTokenCounter(c store.Counter, occurredAt time.Time, window time.Duration) store.Counter {
	c.Name = counterTokenRefresh
	if c.WindowStart.IsZero() || occurredAt.Sub(c.WindowStart) > window {
		c.WindowStart = occurredAt
		c.Value = 1
	} else {
		c.Value++
	}
	return c
}

// ClearSymbol removes a per-symbol block (memory-only). The reconciler (#35) calls
// it when the ambiguous condition that blocked the symbol has resolved (ADR-0004
// point 6 automatic per-symbol clear).
func (g *Guard) ClearSymbol(symbol string) {
	g.mu.Lock()
	delete(g.blockedSymbols, symbol)
	g.mu.Unlock()
}

// ClearHalt is the manual global reset (ADR-0004 point 6 — the only path that
// un-halts; no automatic resume). It holds haltMu across the durable write and the
// mirror lower, so it serializes with trips (I4). It lowers the mirror only if no
// concurrent trip pre-blocked during its hold (generation unchanged, I2); if a
// trip did pre-block, its pending is left standing and it re-commits a durable halt
// after this releases haltMu, so the concurrent trip is never lost. A failed
// durable write keeps the halt (fail-closed).
func (g *Guard) ClearHalt(ctx context.Context) error {
	g.haltMu.Lock()
	defer g.haltMu.Unlock()

	g.mu.Lock()
	startGen := g.gen
	g.mu.Unlock()

	if err := g.store.Atomically(ctx, func(tx store.Tx) error {
		return tx.ClearHalt(ctx)
	}); err != nil {
		return fmt.Errorf("killswitch: clear halt: %w", err) // fail-closed: halt stays
	}

	g.mu.Lock()
	g.durablePhase = store.HaltNone
	if g.gen == startGen && g.mirrorPhase != phaseNone {
		// No concurrent trip pre-blocked during our hold: safe to un-halt the mirror.
		g.mirrorPhase = phaseNone
		g.haltReason = ""
		// No generation bump: an unblock must not invalidate still-allowed reservations.
	}
	// else: a concurrent trip pre-blocked (generation advanced); leave its pending
	// so it re-halts after we release haltMu.
	g.mu.Unlock()
	return nil
}

// BootHalt is the conservative boot-halt affordance #36 drives (ADR-0012
// Decision 1(c)): when an unclean shutdown means a durable-trip-failed pending
// global halt cannot be ruled out, #36 calls this to force the guard halted and
// keep the replay gate closed until an explicit ClearHalt. It is in-memory only —
// re-derived from the sentinel each boot — so it writes no durable state and does
// not make the graceful-shutdown query report an unpersisted pending (that query
// fires only for a *pending* mirror; this sets halted). It holds haltMu so it
// serializes with other transitions (contention is nil at boot).
func (g *Guard) BootHalt(reason string, at time.Time) {
	g.haltMu.Lock()
	defer g.haltMu.Unlock()
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
// global halt the store does not yet reflect — a trip whose MarkHaltPending never
// committed. #36 queries this at shutdown because such a halt is invisible to a
// store read (Halt().Phase==none): if true, #36 must not record a clean shutdown
// without first finalizing it. A pending that DID persist (Halt().Phase==pending)
// returns false here — #36 detects that one directly via the store read.
func (g *Guard) HasUnpersistedPendingHalt() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mirrorPhase == phasePending && g.durablePhase == store.HaltNone
}

// FinalizePendingHalt durably persists an in-memory pending halt now (the
// graceful-shutdown finalize #36 drives, ADR-0012 Decision 1(c)). It holds haltMu
// so it serializes with trips/clears (I4), then re-runs MarkHaltPending→TripHalt.
// On success the pending becomes a durable, restart-surviving halt; on failure the
// halt is left blocked so #36 refuses to record a clean shutdown — via this query
// if MarkHaltPending still failed, or via the store read if only TripHalt failed
// (durable is then pending).
func (g *Guard) FinalizePendingHalt(ctx context.Context) error {
	g.haltMu.Lock()
	defer g.haltMu.Unlock()

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

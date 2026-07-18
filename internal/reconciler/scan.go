package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
)

// Trip reasons. They are recorded verbatim in the durable halt row / notifier, so
// an operator can tell which trigger fired without reading code.
const (
	reasonAmbiguousSymbol   = "ambiguous-submit"
	reasonAmbiguousBacklog  = "ambiguous-submit-backlog"
	reasonAmbiguousNoSymbol = "ambiguous-submit-unaddressable-symbol"
	reasonUnknownStatus     = "order-status-unclassifiable"
	reasonOrderRejected     = "order-rejected"
	reasonAuditFailClosed   = "audit-fail-closed"
	reasonLoopUnsustainable = "reconciler-loop-unsustainable"
)

// reconcile runs one full reconciliation cycle over the live journal. It is the
// SAME code path for the boot scan, the submit-path wake, and every live tick
// (ADR-0003 point 1: one reconciler, one verification surface).
//
// The order of the three phases is load-bearing:
//
//	phase 1  the unconditional per-symbol ambiguous floor, THEN the backlog
//	         threshold. A single ambiguous submit blocks its symbol before any
//	         threshold arithmetic happens (ADR-0014 Decision 1.1) — the floor must
//	         not be reachable only through the escalation branch.
//	phase 2  drive what can be determined: prepared-only intents close, acked
//	         intents get exactly ONE bounded GetOrder each (open orders are tracked,
//	         never polled to completion inline — ADR-0014 Decision 9).
//	phase 3  release only the per-symbol blocks whose evidence is fully gone.
//	         ClearSymbol is a boolean delete, not a refcount, so this is a
//	         level-based reconciliation against the CURRENT evidence set rather
//	         than a per-intent "one resolved ⇒ clear" (ADR-0014 Decision 4).
//
// It returns an error only for a STRUCTURAL failure (the journal scan itself
// failed), which is what the caller counts toward the Decision 12 fail-closed
// promotion. Per-intent problems are handled locally and logged: one order whose
// lookup failed must not abort the whole cycle.
func (r *Reconciler) reconcile(ctx context.Context) error {
	intents, err := r.journal.LoadUnresolvedIntents(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: load unresolved intents: %w", err)
	}

	now := r.now()
	views := make([]intentView, 0, len(intents))
	for _, in := range intents {
		views = append(views, classify(in, now, r.settleWindow))
	}

	// Phase 1 — ambiguous floor first, then the backlog escalation.
	needBlock := r.applyAmbiguousPolicy(ctx, views, now)

	// Phase 2 — establish what is establishable.
	r.driveIntents(ctx, views, now, needBlock)

	// Phase 3 — residual-zero auto-clear.
	r.reconcileSymbolBlocks(needBlock)
	return nil
}

// applyAmbiguousPolicy applies ADR-0014 Decision 1 and returns the set of symbols
// that must stay blocked because of ambiguous evidence.
//
// It never resolves an ambiguous intent, and structurally cannot: an ambiguous
// intent has no acked marker and therefore no orderId, which is the only handle
// GET /orders/{orderId} accepts. That is why this reconciler cannot destroy the
// reconstruction evidence a pending global halt would need, and why ADR-0014
// Decision 8 could drop the persistence-wins halt-phase guard entirely.
func (r *Reconciler) applyAmbiguousPolicy(ctx context.Context, views []intentView, now time.Time) map[string]struct{} {
	needBlock := make(map[string]struct{})
	backlog := 0

	for _, v := range views {
		if v.class != classAmbiguous {
			continue
		}
		// The backlog is counted over CURRENT unresolved-ambiguous intents, so it
		// cannot age out from under a hazard that has not gone away (ADR-0014
		// Decision 1.2 — a time-windowed rate would).
		backlog++

		if v.symbolErr != nil {
			// The local floor cannot be addressed without a symbol. Leaving the
			// intent unblocked would be the one fail-open this policy exists to
			// prevent, so escalate to the global halt instead of degrading to
			// nothing.
			r.logger.Error("ambiguous submit whose symbol cannot be recovered — escalating globally",
				"intent_id", v.intent.IntentID, "error", v.symbolErr)
			r.tripGlobal(ctx, reasonAmbiguousNoSymbol, now)
			continue
		}
		needBlock[v.symbol] = struct{}{}
		// Memory-only boolean set; repeated trips for the same symbol are a no-op.
		// occurredAt is the submit-attempted time — when the unidentified order
		// could first have reached the market.
		r.tripSymbol(ctx, v.symbol, reasonAmbiguousSymbol, v.submitAttemptedAt)
		r.logger.Warn("unresolved-ambiguous submit: symbol blocked, human resolution required",
			"intent_id", v.intent.IntentID, "symbol", v.symbol,
			"submit_attempted_at", v.submitAttemptedAt)
	}

	// Inclusive comparison: threshold N means "halt on the Nth". Reading it as a
	// strict > would silently weaken the configured threshold by one (ADR-0014
	// Decision 1.2). Re-firing an already-standing global halt is an idempotent
	// no-op inside killswitch, which is what makes the "operator cleared it but the
	// backlog is still over threshold" re-fire safe to run every cycle
	// (Decision 6).
	if backlog >= r.backlogThreshold {
		r.logger.Error("unresolved-ambiguous backlog at or over threshold — tripping global halt",
			"backlog", backlog, "threshold", r.backlogThreshold)
		r.tripGlobal(ctx, reasonAmbiguousBacklog, now)
	}
	return needBlock
}

// reconcileSymbolBlocks releases every per-symbol block THIS reconciler published
// whose evidence has fully gone away, and only those.
//
// Two properties matter. First, a symbol is cleared only when its residual
// evidence count is zero — clearing on the first resolution would open a symbol
// that still has other unresolved-ambiguous intents on it, because ClearSymbol is
// a boolean delete rather than a refcount (ADR-0014 Decision 4). Second, only
// symbols in r.blocked are ever cleared, so a block established by something else
// can never be released here.
func (r *Reconciler) reconcileSymbolBlocks(needBlock map[string]struct{}) {
	r.mu.Lock()
	stale := make([]string, 0, len(r.blocked))
	for sym := range r.blocked {
		if _, still := needBlock[sym]; !still {
			stale = append(stale, sym)
			delete(r.blocked, sym)
		}
	}
	r.mu.Unlock()

	for _, sym := range stale {
		r.guard.ClearSymbol(sym)
		r.logger.Info("per-symbol block auto-cleared: zero residual blocking evidence", "symbol", sym)
	}
}

// isBlocked reports whether THIS reconciler currently holds a block on symbol.
func (r *Reconciler) isBlocked(symbol string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.blocked[symbol]
	return ok
}

// tripSymbol publishes the memory-only per-symbol floor and remembers that THIS
// reconciler published it (so auto-clear stays scoped to its own blocks).
func (r *Reconciler) tripSymbol(ctx context.Context, symbol, reason string, occurredAt time.Time) {
	if err := r.guard.Trip(ctx, killswitch.ScopeSymbol, symbol, reason, occurredAt); err != nil {
		// A per-symbol trip is an in-memory map write and does not fail today; if a
		// future implementation can fail, do NOT record it as blocked (that would
		// let auto-clear later "release" a block that was never established).
		r.logger.Error("per-symbol trip failed", "symbol", symbol, "reason", reason, "error", err)
		return
	}
	r.mu.Lock()
	r.blocked[symbol] = struct{}{}
	r.mu.Unlock()
}

// tripGlobal trips the durable global halt. A failure is logged, never retried in
// a tight loop: killswitch has already published its own in-memory block carrier
// (a latch or the in-flight counter) on every failure arm, so the bot is blocked
// either way and the next cycle re-fires from the same live evidence.
func (r *Reconciler) tripGlobal(ctx context.Context, reason string, occurredAt time.Time) {
	if err := r.guard.Trip(ctx, killswitch.ScopeGlobal, "", reason, occurredAt); err != nil {
		r.logger.Error("global trip failed (kill switch has published its own in-memory block)",
			"reason", reason, "error", err)
	}
}

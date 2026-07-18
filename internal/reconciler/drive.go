package reconciler

import (
	"context"
	"errors"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// lookup pairs an acked intent with what one GetOrder established about it.
type lookup struct {
	view    intentView
	verdict verdict
	ord     order.Order
}

// orderingAt is the time an intent is ordered by for the success-reset guard: the
// submit-attempted marker, falling back to the prepared time if a journal is
// missing that marker. A zero time would sort before everything and defer every
// later reset forever, so the fallback is not cosmetic.
func (v intentView) orderingAt() time.Time {
	if !v.submitAttemptedAt.IsZero() {
		return v.submitAttemptedAt
	}
	return v.preparedAt
}

// driveIntents is phase 2: establish what can be established and act on it.
//
// It runs in two stages on purpose. Stage one performs every lookup, so the
// success-reset ordering guard in stage two can see the WHOLE in-doubt set of
// this cycle rather than only the intents visited so far — a guard that saw a
// partial set would let a fill reset the failure streak past an older, not-yet-
// visited undetermined intent.
func (r *Reconciler) driveIntents(ctx context.Context, views []intentView, now time.Time, needBlock map[string]struct{}) {
	// Stage 1 — exactly ONE bounded lookup per acked intent per cycle. The toss
	// client bounds its own backoff per call and the ticker paces re-drives, so
	// there is no unbounded retry here, and an open order is never polled to
	// completion inline (that would let one normal working limit order hold the
	// replay gate shut — ADR-0014 Decision 9 / Alternatives).
	lookups := make([]lookup, 0, len(views))
	for _, v := range views {
		if v.class != classAcked {
			continue
		}
		ord, err := r.api.GetOrder(ctx, r.accountSeq, v.orderID)
		if err != nil {
			// Bounded retries were exhausted. Do not resolve: without truth, closing
			// the intent would be a guess. It stays unresolved for a later re-drive,
			// and it holds back any later fill's success reset so a real REJECT that
			// simply has not been read yet is DELAYED, never erased (ADR-0014
			// Decision 10 + Decision 8 ordering guard).
			r.logger.Warn("order truth lookup failed; intent left unresolved for re-drive",
				"intent_id", v.intent.IntentID, "order_id", v.orderID, "error", err)
			lookups = append(lookups, lookup{view: v, verdict: verdictLookupFailed})
			continue
		}
		lookups = append(lookups, lookup{view: v, verdict: classifyStatus(ord.Status), ord: ord})
	}

	// The in-doubt set: acked intents whose truth this cycle did not establish,
	// plus submit-attempted intents still inside the settle window (their POST may
	// be in flight right now, so they too could turn out to be a rejection).
	// Settled ambiguous intents are deliberately NOT in-doubt here: they can never
	// be established, so counting them would defer every success reset forever.
	// They are covered by their own trigger (symbol floor + backlog escalation) and
	// are never reported to the order-failure counter — double counting is what
	// ADR-0012 forbids.
	var inDoubt []time.Time
	for _, l := range lookups {
		if l.verdict.undetermined() {
			inDoubt = append(inDoubt, l.view.orderingAt())
		}
	}
	for _, v := range views {
		if v.class == classSettling {
			inDoubt = append(inDoubt, v.orderingAt())
		}
	}

	// Stage 2a — prepared-only intents: the POST provably never happened.
	for _, v := range views {
		if v.class == classPreparedOnly {
			r.closePreparedOnly(ctx, v, now)
		}
	}

	// Stage 2b — apply the established verdicts in journal order.
	for _, l := range lookups {
		r.applyVerdict(ctx, l, inDoubt, needBlock)
	}
}

// closePreparedOnly closes an abandoned prepared-only intent as
// aborted-before-submit (ADR-0003 point 2). The reconciler does NOT re-issue it:
// re-deciding an order at a restart-stale price belongs to the strategy, with a
// fresh intentId (ADR-0003 point 4).
//
// The age gate is a safety requirement, not tidiness. On a LIVE tick the submit
// path may be running concurrently, sitting between its prepared commit and its
// submit-attempted commit (its per-step durable-audit budget alone is tens of
// seconds). Closing that intent would terminally resolve an order that is about
// to be POSTed for real, leaving a live order outside the unresolved set and
// therefore outside all later reconciliation. preparedAbandonAfter is the settle
// window floored by a bound that covers the submit path's durable-persist budget
// (see minPreparedAbandonWindow).
func (r *Reconciler) closePreparedOnly(ctx context.Context, v intentView, now time.Time) {
	if now.Sub(v.preparedAt) < r.preparedAbandonAfter {
		return
	}
	r.resolve(ctx, v, ResolutionAbortedBeforeSubmit)
}

// applyVerdict acts on one established (or deliberately unestablished) truth.
func (r *Reconciler) applyVerdict(ctx context.Context, l lookup, inDoubt []time.Time, needBlock map[string]struct{}) {
	v := l.view
	switch l.verdict {
	case verdictLookupFailed:
		// Already logged in stage 1. Nothing is resolved and nothing is blocked: a
		// transient lookup failure is a delay, not evidence of an unidentified order.

	case verdictUnknownStatus:
		// A standing "we cannot tell what this order is doing" — preserve and block
		// (ADR-0003). It also stays unresolved and keeps holding back later success
		// resets via the in-doubt set.
		r.logger.Error("order returned a status this build cannot classify; intent preserved and symbol blocked",
			"intent_id", v.intent.IntentID, "order_id", v.orderID, "status", string(l.ord.Status))
		if v.symbolErr != nil {
			r.tripGlobal(ctx, reasonUnknownStatus, r.now())
			return
		}
		needBlock[v.symbol] = struct{}{}
		r.tripSymbol(ctx, v.symbol, reasonUnknownStatus, v.orderingAt())

	case verdictOpen:
		// The non-blocking live tracker: record any new cumulative execution and
		// leave the intent open for the next cycle.
		r.emitFillDelta(ctx, l)

	case verdictFilled:
		// Success-reset ordering guard (ADR-0014 Decision 8). ReportOrderSuccess
		// resets the consecutive-failure counter unconditionally, so applying it
		// while an equally-old-or-older intent's truth is still undetermined could
		// erase a streak that had genuinely reached the threshold. Withholding the
		// WHOLE resolution (not just the reset) keeps the deferral reconstructible:
		// the evidence stays in the journal, so a crash does not lose it and the next
		// cycle simply retries. Deferring keeps the counter high, i.e. it errs toward
		// over-halting.
		if earlierInDoubt(inDoubt, v.orderingAt()) {
			r.logger.Info("fill success-reset deferred: an equally-old-or-older intent is still undetermined",
				"intent_id", v.intent.IntentID, "order_id", v.orderID)
			return
		}
		if !r.emitFillDelta(ctx, l) {
			// The terminal execution snapshot is not durable; do not close the intent
			// over a non-durable audit trail (ADR-0006 point 4).
			return
		}
		if err := r.guard.ReportOrderSuccess(ctx); err != nil {
			// Leaving the counter high is the conservative direction, and the intent
			// stays unresolved so the next cycle retries the reset.
			r.logger.Error("order-success counter reset failed; intent left unresolved for retry",
				"intent_id", v.intent.IntentID, "error", err)
			return
		}
		r.resolve(ctx, v, ResolutionFilled)

	case verdictCanceled:
		// A cancel is neither a failure nor a success: it must not increment the
		// order-failure counter (it is not a rejection) and must not reset the streak
		// (it is not a fill). A canceled order can still carry a partial fill, so its
		// final snapshot is recorded before the intent closes.
		if !r.emitFillDelta(ctx, l) {
			return
		}
		r.resolve(ctx, v, ResolutionCanceled)

	case verdictRejected:
		// count-before-resolve (ADR-0012 Decision 3 / ADR-0014 Decision 8): the
		// failure must be DURABLE before the evidence for it leaves the unresolved
		// set. Both of ReportOrderFailure's failure arms return non-nil with the
		// counter rolled back, so returning here without resolving is exactly what
		// makes the failure re-countable on the next cycle. Re-counting after a crash
		// between the two steps overcounts, which over-halts — the safe direction.
		if err := r.guard.ReportOrderFailure(ctx, reasonOrderRejected, v.orderingAt()); err != nil {
			r.logger.Error("order-failure report failed; intent left UNRESOLVED as re-count evidence",
				"intent_id", v.intent.IntentID, "order_id", v.orderID, "error", err)
			return
		}
		r.resolve(ctx, v, ResolutionRejected)
	}
}

// earlierInDoubt reports whether any intent whose truth is undetermined is at
// least as old as at. "At least as old" (<=, not <) is deliberate: two intents
// stamped in the same instant cannot be ordered, so the conservative reading
// wins.
func earlierInDoubt(inDoubt []time.Time, at time.Time) bool {
	for _, t := range inDoubt {
		if !t.After(at) {
			return true
		}
	}
	return false
}

// emitFillDelta durably records a changed cumulative execution snapshot and
// reports whether the caller may proceed to close the intent.
//
// Toss exposes no per-fill identifier, only this running aggregate, so a
// snapshot is emitted when it differs from the last one emitted for this orderId
// (ADR-0006). The de-dup memory is in-process only: after a restart one
// already-emitted snapshot may be re-emitted, and the audit idempotency key
// merges it downstream (at-least-once, ADR-0006 point 3).
func (r *Reconciler) emitFillDelta(ctx context.Context, l lookup) bool {
	snap := snapshotOf(l.ord.Execution)
	if isZeroDecimal(snap.FilledQuantity) {
		return true // nothing has executed yet; there is no fill to record.
	}

	r.mu.Lock()
	last, seen := r.lastFill[l.view.orderID]
	r.mu.Unlock()
	if seen && last == snap {
		return true
	}

	if _, err := r.audit.EmitFill(ctx, audit.FillEvent{
		OrderID:    l.view.orderID,
		Snapshot:   snap,
		OccurredAt: r.now(),
	}); err != nil {
		if audit.IsFailClosed(err) {
			r.escalateAuditFailClosed(ctx, "fill", l.view.intent.IntentID, err)
			return false
		}
		r.logger.Error("fill audit emit failed", "order_id", l.view.orderID, "error", err)
		return false
	}

	r.mu.Lock()
	r.lastFill[l.view.orderID] = snap
	r.mu.Unlock()
	return true
}

// resolve terminally closes an intent and immediately converges its audit trail
// (which is what emits the terminal lifecycle record).
//
// A store.ErrResolutionConflict is NEVER swallowed (#28): it means two
// components reached contradictory verdicts about the same order, and the journal
// is the restart-recovery and audit ground truth, so a silently dropped second
// verdict makes a real inconsistency undetectable. It is logged AND written to
// the independent durable audit medium. The first recorded resolution is never
// overwritten, so the audit convergence still runs against the stored verdict.
func (r *Reconciler) resolve(ctx context.Context, v intentView, resolution string) {
	err := r.journal.ResolveIntent(ctx, v.intent.IntentID, resolution)
	switch {
	case err == nil:
		r.logger.Info("intent resolved", "intent_id", v.intent.IntentID, "resolution", resolution)
	case errors.Is(err, store.ErrResolutionConflict):
		r.logger.Error("journal resolution CONFLICT: the intent was already closed with a different verdict",
			"intent_id", v.intent.IntentID, "attempted_resolution", resolution, "error", err)
		r.emitErrorRecord(ctx, v.intent.IntentID, v.orderID, "reconciler.resolve", "resolution-conflict", err.Error())
	default:
		// Not durable: leave the intent unresolved and retry on the next cycle. For
		// a rejection this is safe by construction — the failure was already counted,
		// and a re-count is an overcount, which over-halts.
		r.logger.Error("resolve intent failed; left unresolved for a later cycle",
			"intent_id", v.intent.IntentID, "resolution", resolution, "error", err)
		return
	}
	r.syncAudit(ctx, v.intent.IntentID)
}

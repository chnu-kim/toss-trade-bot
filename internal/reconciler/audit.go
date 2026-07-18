package reconciler

import (
	"context"
	"fmt"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
)

// reemitAudit is boot pass 2 and part of every live cycle: the audit re-emit
// driver #20 left to this issue (ADR-0006 point 4 recovery loop).
//
// A crash between a marker's durable journal commit and its audit record's fsync
// leaves an intent whose audit trail is incomplete. store can reconstruct exactly
// which lifecycle records are still un-acked (deterministically, from markers plus
// resolution), so the driver re-emits those and records their acks, which
// converges the per-intent fully-audited flag that gates prune (#14).
//
// It runs AFTER the marker-branching pass, never in parallel with it: the same
// intent belongs to both (pass 1 resolves it, which is what creates its terminal
// record), and a parallel pass would race that dependency. Running after the
// replay gate opens is safe — re-emitting audit records creates no new exposure
// and cannot block a symbol; the only blocking effect it can have, an audit
// fail-closed escalation, is a durable global halt that re-blocks regardless
// (ADR-0014 Decision 9).
//
// It returns an error only for a structural failure (the candidate scan itself),
// so one bad intent does not abort the recovery of the rest.
func (r *Reconciler) reemitAudit(ctx context.Context) error {
	intents, err := r.journal.LoadNotFullyAuditedIntents(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: load not-fully-audited intents: %w", err)
	}
	for _, in := range intents {
		r.syncAudit(ctx, in.IntentID)
	}
	return nil
}

// syncAudit re-emits every un-acked lifecycle record for one intent, records each
// durable ack, and then tries to close the prune gate.
//
// Ordering is the whole point: the ack is recorded only AFTER the sink returned a
// durable ack, so a crash in between re-emits the record rather than marking an
// undurable record as acked. Re-emitting an already-durable record is harmless —
// the audit idempotency key is the merge handle (at-least-once, ADR-0006 point 3).
//
// It reads no halt state. Finalization keeps converging while a global halt (or a
// bootHalt) stands, which is exactly the freeze ADR-0014 Decision 8 removed: a
// human-clear-only halt can stand for days, and freezing confirmed-order
// resolution and audit finalization behind it is an availability self-injury with
// no safety gain. The prune gate itself stays honest without any halt guard,
// because FinalizeFullyAudited requires the intent to be resolved.
func (r *Reconciler) syncAudit(ctx context.Context, intentID string) {
	records, err := r.journal.UnackedLifecycleRecords(ctx, intentID)
	if err != nil {
		r.logger.Error("could not reconstruct un-acked lifecycle records", "intent_id", intentID, "error", err)
		return
	}

	for _, rec := range records {
		if _, err := r.audit.EmitOrderLifecycle(ctx, audit.OrderLifecycleEvent{
			IntentID:   rec.IntentID,
			OrderID:    rec.OrderID,
			Marker:     rec.Marker,
			OccurredAt: rec.OccurredAt,
		}); err != nil {
			if audit.IsFailClosed(err) {
				r.escalateAuditFailClosed(ctx, "order-lifecycle:"+rec.Marker, intentID, err)
				return
			}
			r.logger.Error("lifecycle audit re-emit failed", "intent_id", intentID, "marker", rec.Marker, "error", err)
			return
		}
		if err := r.journal.RecordAuditAck(ctx, intentID, rec.Key); err != nil {
			// The record IS durable in the sink; only the ack bookkeeping failed. The
			// flag stays unset, so prune preserves the intent — fail-safe — and the
			// next cycle re-emits and re-acks.
			r.logger.Error("recording the audit ack failed; prune gate stays closed",
				"intent_id", intentID, "record_key", rec.Key, "error", err)
			return
		}
	}

	if _, err := r.journal.FinalizeFullyAudited(ctx, intentID); err != nil {
		r.logger.Error("finalizing the fully-audited flag failed", "intent_id", intentID, "error", err)
	}
}

// escalateAuditFailClosed turns a non-durable audit write into a global halt
// (ADR-0006 point 6). The escalation deliberately runs on the kill switch's OWN
// durable path (the store), never back through the audit medium that just failed,
// which is what breaks the cycle.
func (r *Reconciler) escalateAuditFailClosed(ctx context.Context, what, intentID string, cause error) {
	r.logger.Error("audit sink is not durable — tripping global halt (fail-closed)",
		"what", what, "intent_id", intentID, "error", cause)
	r.tripGlobal(ctx, reasonAuditFailClosed+":"+what, r.now())
}

// emitErrorRecord preserves a reconstruction-resistant anomaly on the audit
// medium. It is best-effort by design: it is a diagnostic, and its own failure
// must not mask the condition being reported. A fail-closed failure still
// escalates, since a sink that cannot make a record durable is itself the
// ADR-0006 point 6 condition.
func (r *Reconciler) emitErrorRecord(ctx context.Context, intentID, orderID, operation, class, message string) {
	if _, err := r.audit.EmitError(ctx, audit.ErrorEvent{
		IntentID:   intentID,
		OrderID:    orderID,
		Operation:  operation,
		ErrorClass: class,
		Message:    message,
		OccurredAt: r.now(),
	}); err != nil {
		if audit.IsFailClosed(err) {
			r.escalateAuditFailClosed(ctx, "error:"+class, intentID, err)
			return
		}
		r.logger.Error("error-record audit emit failed", "intent_id", intentID, "class", class, "error", err)
	}
}

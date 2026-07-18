package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// TestAuditAckWiringEndToEnd exercises the full happy-path wiring this issue
// establishes, across the two leaf packages, without either importing the other:
//
//	store journal (markers + resolution)
//	  → store.ReconstructLifecycleRecords / UnackedLifecycleRecords (store-local records)
//	  → audit.Sink.EmitOrderLifecycle (real writer, temp dir) returns a durable Ack
//	  → store.RecordAuditAck(intentID, rec.Key) + FinalizeFullyAudited
//	  → fully-audited flag flips only after EVERY lifecycle record is durably acked.
//
// The nil-error Ack returned by the real audit writer IS the ADR-0006 point 4
// durable-ack signal; this test consumes it exactly as a future reconciler would,
// mapping each store-local LifecycleRecord to an audit event and recording the ack
// back by the store-local key. The bridge lives in the test (a consumer), so
// neither leaf depends on the other.
func TestAuditAckWiringEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	db, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	sink, err := audit.New(filepath.Join(dir, "audit"))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	defer sink.Close()

	const id, orderID = "intent-1", "ord-1"
	if err := db.AppendIntent(ctx, store.Intent{IntentID: id, ClientOrderID: "cli-1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, id, store.MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("AppendMarker submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, id, store.MarkerAcked, orderID); err != nil {
		t.Fatalf("AppendMarker acked: %v", err)
	}
	if err := db.ResolveIntent(ctx, id, "FILLED"); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	recs, err := db.UnackedLifecycleRecords(ctx, id)
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("un-acked lifecycle records = %d, want 4", len(recs))
	}

	for i, rec := range recs {
		// A consumer maps the store-local record to an audit event. store never
		// imports audit; the mapping lives here.
		ack, err := sink.EmitOrderLifecycle(ctx, audit.OrderLifecycleEvent{
			IntentID:   rec.IntentID,
			OrderID:    rec.OrderID,
			Marker:     rec.Marker,
			OccurredAt: rec.OccurredAt,
		})
		if err != nil {
			t.Fatalf("EmitOrderLifecycle %q: %v", rec.Marker, err)
		}
		// A nil-error Ack is the durable-ack signal (ADR-0006 point 4).
		if ack.IdempotencyKey == "" {
			t.Fatalf("EmitOrderLifecycle %q returned empty idempotency key", rec.Marker)
		}
		if err := db.RecordAuditAck(ctx, rec.IntentID, rec.Key); err != nil {
			t.Fatalf("RecordAuditAck %q: %v", rec.Key, err)
		}
		done, err := db.FinalizeFullyAudited(ctx, rec.IntentID)
		if err != nil {
			t.Fatalf("FinalizeFullyAudited: %v", err)
		}
		if last := i == len(recs)-1; done != last {
			t.Fatalf("after durably acking %d/%d lifecycle records, fully-audited = %v, want %v",
				i+1, len(recs), done, last)
		}
	}

	if _, set, err := db.FullyAudited(ctx, id); err != nil || !set {
		t.Fatalf("FullyAudited after all durable acks = %v, err %v, want set", set, err)
	}
}

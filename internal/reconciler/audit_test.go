package reconciler

import (
	"context"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// TestAuditReEmit_RestartRecovery is the ADR-0006 point 4 recovery loop and the
// driver #20 left to this issue.
//
// The crash being modelled is "the journal marker committed, its audit record did
// not". On restart the un-acked lifecycle records are reconstructed
// deterministically from the journal, re-emitted, acked, and the per-intent
// fully-audited flag (the prune gate #14 reads) converges.
func TestAuditReEmit_RestartRecovery(t *testing.T) {
	r := newRig(t)
	// A full 2-marker progression whose audit records were never acked — exactly
	// what a crash between the marker commit and the audit fsync leaves behind.
	seedAcked(t, r.db, "i-1", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "10", AverageFilledPrice: ptr("1000")},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Every lifecycle transition plus the terminal record was re-emitted.
	want := map[string]bool{
		string(store.MarkerPrepared):        false,
		string(store.MarkerSubmitAttempted): false,
		string(store.MarkerAcked):           false,
		ResolutionFilled:                    false,
	}
	for _, ev := range r.sink.lifecycleEvents() {
		if ev.IntentID != "i-1" {
			continue
		}
		if _, ok := want[ev.Marker]; ok {
			want[ev.Marker] = true
		}
	}
	for marker, seen := range want {
		if !seen {
			t.Fatalf("lifecycle record %q was never re-emitted", marker)
		}
	}

	// ...and the prune gate closed.
	row, _ := readIntentRow(t, r.path, "i-1")
	if !row.fullyAudited {
		t.Fatal("the fully-audited flag did not converge after the re-emit")
	}
	// The acked record carries the orderId (ADR-0002/0006 key reuse), so the audit
	// idempotency key merges with whatever the submit path already emitted.
	for _, ev := range r.sink.lifecycleEvents() {
		if ev.Marker == string(store.MarkerAcked) && ev.OrderID != "ord-1" {
			t.Fatalf("acked record carried orderId %q, want ord-1", ev.OrderID)
		}
	}
}

// TestAuditReEmit_ResolvedOrphanIsDiscoverable: a crash between ResolveIntent and
// the terminal audit ack leaves an intent that has left the UNRESOLVED set. If the
// driver only scanned unresolved intents it would be undiscoverable forever and
// its prune gate would never close.
func TestAuditReEmit_ResolvedOrphanIsDiscoverable(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-orphan", "AAA", "ord-1")
	if err := r.db.ResolveIntent(context.Background(), "i-orphan", ResolutionFilled); err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	row, _ := readIntentRow(t, r.path, "i-orphan")
	if !row.fullyAudited {
		t.Fatal("a resolved-but-unfinalized orphan was never recovered")
	}
}

// TestAuditReEmit_Idempotent: re-running the driver must not re-emit records that
// are already acked (the ack ledger is the memory), so a steady state costs
// nothing and converges rather than growing.
func TestAuditReEmit_Idempotent(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "AAA", "ord-1")
	r.api.set("ord-1", order.OrderStatusCanceled, "AAA")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	first := len(r.sink.lifecycleEvents())
	if first == 0 {
		t.Fatal("nothing was emitted")
	}
	for i := 0; i < 3; i++ {
		if err := r.cycle(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if got := len(r.sink.lifecycleEvents()); got != first {
		t.Fatalf("lifecycle emits grew from %d to %d across idle cycles; the ack ledger is not being honoured", first, got)
	}
}

// TestAuditFailClosedTripsGlobalHalt is ADR-0006 point 6: a record that could not
// be made durable means the audit trail is broken, and an unattended bot must not
// keep trading over a broken audit trail. The escalation runs on the kill switch's
// OWN durable path, never back through the medium that just failed.
func TestAuditFailClosedTripsGlobalHalt(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "AAA", "ord-1")
	r.api.set("ord-1", order.OrderStatusCanceled, "AAA")
	r.sink.failLifecycle = true

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want a global halt after a non-durable audit write", got)
	}
	row, _ := readIntentRow(t, r.path, "i-1")
	if row.fullyAudited {
		t.Fatal("the prune gate closed even though a lifecycle record is not durable")
	}
}

// TestFillFailClosedBlocksResolution: the terminal execution snapshot must be
// durable before the intent that produced it is closed — closing it first would
// leave a filled order whose fill is not in the audit trail, and the journal that
// was its outbox becomes prune-eligible.
func TestFillFailClosedBlocksResolution(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-fill", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "10"},
	})
	r.sink.failFill = true

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertUnresolved(t, r.path, "i-fill")
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want a global halt after a non-durable fill record", got)
	}
	if r.log.contains("report-order-success") {
		t.Fatal("the success reset ran even though the fill record is not durable")
	}
}

// TestFillDeltaEmittedOncePerSnapshot: Toss exposes no per-fill identifier, only a
// running cumulative snapshot, so an unchanged snapshot must not append a new
// record on every re-drive — and a CHANGED one must.
func TestFillDeltaEmittedOncePerSnapshot(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-partial", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusPartialFilled,
		Execution: order.OrderExecution{FilledQuantity: "3"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got := len(r.sink.fillEvents()); got != 1 {
		t.Fatalf("%d fill records for an unchanged snapshot, want 1", got)
	}

	// A same-quantity fee correction still changes the financial digest, so it is a
	// distinct record (ADR-0006).
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusPartialFilled,
		Execution: order.OrderExecution{FilledQuantity: "3", Commission: ptr("12")},
	})
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got := len(r.sink.fillEvents()); got != 2 {
		t.Fatalf("%d fill records after a corrected snapshot, want 2", got)
	}
}

// TestNoFillRecordBeforeAnythingExecutes: an untouched open order has a zero
// cumulative quantity and must not manufacture a fill record.
func TestNoFillRecordBeforeAnythingExecutes(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-open", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusPending,
		Execution: order.OrderExecution{FilledQuantity: "0.00"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if got := len(r.sink.fillEvents()); got != 0 {
		t.Fatalf("%d fill records for an order with nothing executed, want 0", got)
	}
}

func TestIsZeroDecimal(t *testing.T) {
	zero := []string{"", "0", "0.0", "0.00", "+0", "-0.000"}
	nonZero := []string{"1", "0.01", "10", "0.0000001"}
	for _, s := range zero {
		if !isZeroDecimal(s) {
			t.Errorf("isZeroDecimal(%q) = false, want true", s)
		}
	}
	for _, s := range nonZero {
		if isZeroDecimal(s) {
			t.Errorf("isZeroDecimal(%q) = true, want false", s)
		}
	}
}

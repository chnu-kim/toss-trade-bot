package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// --- count-before-resolve (ADR-0012 Decision 3 / ADR-0014 Decision 8) --------

// TestCountBeforeResolve proves the ordering against the REAL kill switch and the
// REAL store: the durable failure counter must already carry this failure at the
// instant the intent leaves the unresolved set. Resolving first would permanently
// undercount (the evidence is gone, so a crash between the two can never be
// recovered) — a fail-open.
func TestCountBeforeResolve(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-rej", "005930", "ord-1")
	r.api.set("ord-1", order.OrderStatusRejected, "005930")

	var counterAtResolve int64 = -1
	r.journal.onResolve = func(string) {
		counterAtResolve = orderFailureCount(t, r.db)
	}

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertResolution(t, r.path, "i-rej", ResolutionRejected)
	if counterAtResolve != 1 {
		t.Fatalf("durable order-failure counter was %d when ResolveIntent ran, want it already incremented to 1", counterAtResolve)
	}
	report := r.log.indexOf("report-order-failure")
	resolve := r.log.indexOf("resolve:i-rej:" + ResolutionRejected)
	if report < 0 || resolve < 0 || report > resolve {
		t.Fatalf("count-before-resolve ordering violated: %v", r.log.snapshot())
	}
}

// TestCountBeforeResolve_CrashBetweenCountAndResolveOvercounts is the restart
// half of the contract. A crash after the durable count but before the resolve
// leaves the intent unresolved, so the restart re-drives it and counts it again.
// Overcounting means over-halting, which is the safe direction; the alternative
// ordering loses the failure forever.
func TestCountBeforeResolve_CrashBetweenCountAndResolveOvercounts(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-rej", "005930", "ord-1")
	r.api.set("ord-1", order.OrderStatusRejected, "005930")

	// Simulate the crash: the count commits, the resolve never lands.
	r.journal.failResolve("i-rej", errors.New("crash before resolve"))
	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if got := orderFailureCount(t, r.db); got != 1 {
		t.Fatalf("counter = %d after the first pass, want 1", got)
	}
	assertUnresolved(t, r.path, "i-rej")

	// Restart: the evidence is still in the journal, so the failure is re-counted.
	r.journal.clearResolveFailures()
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got := orderFailureCount(t, r.db); got != 2 {
		t.Fatalf("counter = %d after the re-drive, want 2 (an overcount is the safe direction)", got)
	}
	assertResolution(t, r.path, "i-rej", ResolutionRejected)
}

// --- success-reset ordering guard (ADR-0014 Decision 8, Consequence (d)) -----

// TestSuccessResetOrderingGuard is the money-safety heart of Decision 8.
//
// ReportOrderSuccess resets the consecutive-failure counter unconditionally and
// is explicitly outside the count-ordering contract, so ordering is the caller's
// duty. Without the guard, a later FILL's reset applied over an older REJECT that
// is merely slow to confirm does not DELAY the escalation, it ERASES it: the
// streak that genuinely reached the threshold is never durably observed.
//
// The discriminating assertion is the global halt at the end. With the guard the
// streak reaches the threshold and trips; without it the counter would have been
// reset to 0 mid-way and the late REJECT would count 0→1, never tripping.
func TestSuccessResetOrderingGuard(t *testing.T) {
	db, path := openStore(t)
	sw := newSwitch(t, db, killswitch.Config{
		OrderFailureThreshold: 3,
		TokenRefreshThreshold: 3,
		TokenRefreshWindow:    time.Minute,
	})
	r := newRigWith(t, db, path, sw)

	// Three rejections, the newest of which cannot be confirmed yet, and then a
	// fill that is NEWER than all of them.
	seedAcked(t, r.db, "i-1", "AAA", "ord-1")
	seedAcked(t, r.db, "i-2", "AAA", "ord-2")
	seedAcked(t, r.db, "i-3", "AAA", "ord-3")
	seedAcked(t, r.db, "i-4", "AAA", "ord-4")
	r.api.set("ord-1", order.OrderStatusRejected, "AAA")
	r.api.set("ord-2", order.OrderStatusRejected, "AAA")
	r.api.fail("ord-3", errors.New("lookup exhausted"))
	r.api.setOrder(order.Order{
		OrderID: "ord-4", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "10"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// The fill's reset is withheld while the older, still-undetermined intent
	// stands, and the whole resolution is deferred so the deferral survives a crash.
	if r.log.contains("report-order-success") {
		t.Fatal("a later fill reset the failure streak while an older intent's truth was undetermined")
	}
	assertUnresolved(t, r.path, "i-4")
	if got := orderFailureCount(t, r.db); got != 2 {
		t.Fatalf("counter = %d, want the two confirmed rejections", got)
	}
	if haltPhase(t, r.db) != store.HaltNone {
		t.Fatal("halted before the threshold was reached")
	}

	// The slow lookup finally answers: it was a rejection all along.
	r.api.clearFail("ord-3")
	r.api.set("ord-3", order.OrderStatusRejected, "AAA")
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	// The streak reached the threshold and the escalation actually happened.
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want the escalation to have fired — a delayed rejection must not be erased", got)
	}
	assertResolution(t, r.path, "i-3", ResolutionRejected)
	// Only now may the fill's reset apply.
	if !r.log.contains("report-order-success") {
		t.Fatal("the deferred success reset was never applied after the older intent was established")
	}
	assertResolution(t, r.path, "i-4", ResolutionFilled)
}

// TestSuccessResetNotDeferredByNewerInDoubt: the guard is about ordering, not
// about blocking on any in-doubt intent whatsoever. A NEWER undetermined intent
// cannot have been part of the streak that preceded this fill, so it must not
// hold the reset back — otherwise a permanently stuck lookup would freeze every
// later reset.
func TestSuccessResetNotDeferredByNewerInDoubt(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "AAA", "ord-1") // older: FILLED
	seedAcked(t, r.db, "i-2", "AAA", "ord-2") // newer: undetermined
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	r.api.fail("ord-2", errors.New("lookup exhausted"))

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !r.log.contains("report-order-success") {
		t.Fatal("a NEWER undetermined intent wrongly deferred an older fill's success reset")
	}
	assertResolution(t, r.path, "i-1", ResolutionFilled)
	assertUnresolved(t, r.path, "i-2")
}

// TestSettlingIntentDefersSuccessReset: an intent still inside the settle window
// has a POST possibly in flight right now, so it could still turn out to be a
// rejection. It participates in the in-doubt set (over-halt direction).
func TestSettlingIntentDefersSuccessReset(t *testing.T) {
	r := newRig(t, withSettleWindow(time.Hour))
	seedSubmitAttempted(t, r.db, "i-1", "AAA") // older, still settling
	seedAcked(t, r.db, "i-2", "AAA", "ord-2")  // newer, filled
	r.api.setOrder(order.Order{
		OrderID: "ord-2", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if r.log.contains("report-order-success") {
		t.Fatal("a fill reset the streak while an older intent was still inside the settle window")
	}
	assertUnresolved(t, r.path, "i-2")
}

// TestAmbiguousDoesNotDeferForever: a settled ambiguous intent can NEVER be
// established (it has no orderId handle), so counting it as in-doubt would defer
// every later success reset forever. Ambiguity has its own trigger — symbol floor
// plus backlog escalation — and is deliberately never reported to the
// order-failure counter (double counting is what ADR-0012 forbids).
func TestAmbiguousDoesNotDeferForever(t *testing.T) {
	r := newRig(t, withThreshold(99))
	seedSubmitAttempted(t, r.db, "i-1", "AAA") // older, permanently ambiguous
	seedAcked(t, r.db, "i-2", "BBB", "ord-2")  // newer, filled
	r.api.setOrder(order.Order{
		OrderID: "ord-2", Symbol: "BBB", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !r.log.contains("report-order-success") {
		t.Fatal("a permanently unresolvable ambiguous intent deferred a later fill forever")
	}
	assertResolution(t, r.path, "i-2", ResolutionFilled)
	if r.log.contains("report-order-failure") {
		t.Fatal("an ambiguous submit must never be reported to the order-failure counter (double counting)")
	}
	assertUnresolved(t, r.path, "i-1")
}

// --- resolution conflict (#28) ----------------------------------------------

// TestResolutionConflictIsEscalatedNotSwallowed: two components reaching
// contradictory verdicts about the same order is a journal-consistency bug, and
// the journal is the restart-recovery and audit ground truth — a silently dropped
// second verdict makes a real inconsistency undetectable.
func TestResolutionConflictIsEscalatedNotSwallowed(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-conflict", "AAA", "ord-1")
	r.api.set("ord-1", order.OrderStatusFilled, "AAA")

	// Something else closed it first, with a different verdict.
	if err := r.db.ResolveIntent(context.Background(), "i-conflict", ResolutionAbortedBeforeSubmit); err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	// It has left the unresolved set, so drive it explicitly through the same code
	// path the live scan uses.
	r.journal.Journal = conflictingJournal{Journal: r.journal.Journal, intentID: "i-conflict"}

	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	var found bool
	for _, ev := range r.sink.errorEvents() {
		if ev.ErrorClass == "resolution-conflict" && ev.IntentID == "i-conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("a resolution conflict was swallowed: no durable error record was written")
	}
	// The first recorded resolution is never overwritten.
	assertResolution(t, r.path, "i-conflict", ResolutionAbortedBeforeSubmit)
}

// conflictingJournal re-surfaces an already-resolved intent in the unresolved
// scan, which is how a journal-consistency bug would present itself to the
// reconciler.
type conflictingJournal struct {
	Journal
	intentID string
}

func (j conflictingJournal) LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error) {
	all, err := j.Journal.LoadNotFullyAuditedIntents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.Intent, 0, len(all))
	for _, in := range all {
		if in.IntentID == j.intentID {
			in.ResolvedAt = nil // present it as still open
			out = append(out, in)
		}
	}
	return out, nil
}

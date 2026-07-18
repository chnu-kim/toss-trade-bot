package reconciler

import (
	"context"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// This file tests the safety claim ADR-0014 Decision 9 rests on when it puts the
// audit re-emit pass AFTER the replay gate opens.
//
// The ordering is a deliberate ADR decision, not an oversight: gating the whole
// bot on audit RECOVERY would let a slow (or large) recovery pass hold new
// submissions shut long after the kill switch has been fully re-derived, and
// re-emitting audit records creates no new exposure of its own. The ADR's stated
// reason the residual window is safe is that pass 2's only blocking effect — an
// audit fail-closed escalation — is a durable global halt that re-blocks anyway.
//
// An adversarial review challenged exactly this: between gate-open and the pass-2
// halt, a submitter can pass CanSubmit, so (the argument goes) a dead audit sink
// could admit unaudited orders. The test below pins why that does not happen: the
// submit path audits every marker synchronously-durably, and the FIRST such record
// (prepared) precedes the POST. A dead sink therefore fail-closes the submit before
// anything irreversible, and trips the global halt itself.
//
// If this invariant ever changes — e.g. the submit path stops auditing before the
// POST — the ADR's ordering argument collapses and BootScan must move
// NotifyScanComplete after pass 2.

// postForbiddenAPI fails the test if a POST is ever attempted.
type postForbiddenAPI struct{ t *testing.T }

func (p postForbiddenAPI) SubmitOrder(context.Context, int64, order.OrderRequest) (order.OrderResponse, error) {
	p.t.Helper()
	p.t.Fatal("an order was POSTed through a dead audit sink — the audit fail-closed gate did not hold")
	return order.OrderResponse{}, nil
}

func TestGateOpenWindowCannotPostThroughADeadAuditSink(t *testing.T) {
	r := newRig(t)

	// The exact state of the window under discussion: the replay gate is open (pass
	// 1 finished and re-derived every block) and the audit sink is dead, but the
	// pass-2 escalation has not landed yet.
	r.sw.NotifyScanComplete()
	r.sink.failLifecycle = true
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("precondition: expected the gate to be open, got %q", reason)
	}

	submitter, err := order.NewSubmitter(order.SubmitterConfig{
		Journal:    r.db,
		Audit:      r.sink,
		Guard:      r.sw,
		API:        postForbiddenAPI{t: t},
		AccountSeq: testAccountSeq,
	})
	if err != nil {
		t.Fatalf("new submitter: %v", err)
	}

	outcome, err := submitter.SubmitIntent(context.Background(), order.Intent{
		IntentID: "raced-in",
		Request: order.OrderRequest{
			Symbol: "005930", Side: order.SideBuy, OrderType: order.OrderTypeLimit,
			Quantity: "1", Price: "1000",
		},
	})
	if err == nil {
		t.Fatal("a submit through a dead audit sink reported success")
	}
	if outcome.Status != order.StatusUnresolved {
		t.Fatalf("outcome = %v, want the submit to have stopped before the POST", outcome.Status)
	}
	// The submit path escalated on its own, so the window closes itself even before
	// the reconciler's pass 2 gets there.
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want the dead sink to have tripped the global halt", got)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("submissions are still allowed after the audit fail-closed escalation")
	}

	// And the intent never reached the "POST may have happened" marker.
	intents, err := r.db.LoadUnresolvedIntents(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, in := range intents {
		if in.IntentID != "raced-in" {
			continue
		}
		for _, m := range in.Markers {
			if m.Kind == store.MarkerSubmitAttempted {
				t.Fatal("a submit-attempted marker was written through a dead audit sink")
			}
		}
	}
}

// TestPass2FailClosedReBlocksAfterGateOpen is the second half of the ADR's
// argument: when the reconciler's own audit recovery finds a dead sink, the halt
// it trips is DURABLE, so it re-blocks the bot and survives a restart.
func TestPass2FailClosedReBlocksAfterGateOpen(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "005930", "ord-1")
	r.api.set("ord-1", order.OrderStatusPending, "005930") // open: pass 1 resolves nothing
	r.sink.failLifecycle = true

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if !r.log.contains("notify-scan-complete") {
		t.Fatal("the replay gate never opened")
	}
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want pass 2's fail-closed escalation to be durable", got)
	}
	if allowed, reason := r.canSubmit("005930"); allowed {
		t.Fatal("the bot was not re-blocked after the audit fail-closed escalation")
	} else if reason != "global-halt:halted" {
		t.Fatalf("blocked for %q, want the durable global halt", reason)
	}
}

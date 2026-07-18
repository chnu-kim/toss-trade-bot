package reconciler

import (
	"context"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// TestBacklogThresholdIsInclusive pins ADR-0014 Decision 1.2: threshold N means
// "halt on the Nth". Reading the comparison as a strict > would silently weaken a
// configured threshold by one, which is a fail-open — this is the exact off-by-one
// the ADR's own codex review caught in prose.
func TestBacklogThresholdIsInclusive(t *testing.T) {
	t.Run("one below threshold does not halt globally", func(t *testing.T) {
		r := newRig(t, withThreshold(3))
		seedSubmitAttempted(t, r.db, "i-1", "AAA")
		seedSubmitAttempted(t, r.db, "i-2", "BBB")
		r.pastSettle()

		if err := r.boot(); err != nil {
			t.Fatalf("boot: %v", err)
		}
		if haltPhase(t, r.db) != store.HaltNone {
			t.Fatal("a backlog of 2 tripped a global halt at threshold 3")
		}
		if allowed, _ := r.canSubmit("CCC"); !allowed {
			t.Fatal("an unrelated symbol was blocked below the backlog threshold")
		}
	})

	t.Run("exactly at threshold halts globally", func(t *testing.T) {
		r := newRig(t, withThreshold(3))
		seedSubmitAttempted(t, r.db, "i-1", "AAA")
		seedSubmitAttempted(t, r.db, "i-2", "BBB")
		seedSubmitAttempted(t, r.db, "i-3", "CCC")
		r.pastSettle()

		if err := r.boot(); err != nil {
			t.Fatalf("boot: %v", err)
		}
		if got := haltPhase(t, r.db); got != store.HaltHalted {
			t.Fatalf("durable halt phase = %q, want %q at the inclusive threshold", got, store.HaltHalted)
		}
		if allowed, reason := r.canSubmit("ZZZ"); allowed {
			t.Fatal("the global halt did not block a new submission")
		} else if reason != "global-halt:halted" {
			t.Fatalf("blocked for %q, want the durable global halt", reason)
		}
	})
}

// TestFloorPrecedesThresholdJudgment is ADR-0014 Decision 1.1: the per-symbol
// floor is unconditional and is applied BEFORE any threshold arithmetic, so it can
// never become reachable only through the escalation branch.
func TestFloorPrecedesThresholdJudgment(t *testing.T) {
	r := newRig(t, withThreshold(2))
	seedSubmitAttempted(t, r.db, "i-1", "AAA")
	seedSubmitAttempted(t, r.db, "i-2", "BBB")
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	firstSymbolTrip := r.log.indexOf("trip-symbol:AAA")
	globalTrip := r.log.indexOf("trip-global:" + reasonAmbiguousBacklog)
	if firstSymbolTrip < 0 || globalTrip < 0 {
		t.Fatalf("expected both a symbol floor and the backlog escalation, got %v", r.log.snapshot())
	}
	if firstSymbolTrip > globalTrip {
		t.Fatalf("the symbol floor was applied after the threshold judgment: %v", r.log.snapshot())
	}
}

// TestAllTripsPrecedeScanComplete is ADR-0014 Consequence (b) / ADR-0004 point 3:
// per-symbol blocks are deliberately NOT persisted, so if the replay gate opened
// before they were re-derived, a restart would itself be the bypass of the
// per-symbol protection.
func TestAllTripsPrecedeScanComplete(t *testing.T) {
	r := newRig(t, withThreshold(2))
	seedSubmitAttempted(t, r.db, "i-1", "AAA")
	seedSubmitAttempted(t, r.db, "i-2", "BBB")
	r.pastSettle()

	// Before the scan the gate is shut regardless of evidence.
	if allowed, reason := r.canSubmit("AAA"); allowed {
		t.Fatal("the replay gate was open before the restart scan ran")
	} else if reason != "replay-gate-closed" {
		t.Fatalf("pre-scan block reason = %q, want replay-gate-closed", reason)
	}

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	scanComplete := r.log.indexOf("notify-scan-complete")
	if scanComplete < 0 {
		t.Fatal("the replay gate never opened")
	}
	for _, call := range []string{"trip-symbol:AAA", "trip-symbol:BBB", "trip-global:" + reasonAmbiguousBacklog} {
		idx := r.log.indexOf(call)
		if idx < 0 {
			t.Fatalf("missing %q in %v", call, r.log.snapshot())
		}
		if idx > scanComplete {
			t.Fatalf("%q was injected AFTER the replay gate opened: %v", call, r.log.snapshot())
		}
	}
}

// TestScanFailureLeavesGateShut: a scan that could not run is "state unknown",
// which ADR-0004 point 3 requires to be treated as blocked — not as "no evidence
// found".
func TestScanFailureLeavesGateShut(t *testing.T) {
	r := newRig(t)
	r.journal.setLoadErr(context.DeadlineExceeded)

	if err := r.boot(); err == nil {
		t.Fatal("boot reported success despite a failed scan")
	}
	if r.log.contains("notify-scan-complete") {
		t.Fatal("the replay gate was opened after a failed restart scan")
	}
	if !r.log.contains("boot-halt") {
		t.Fatal("a failed restart scan must promote to a fail-closed halt")
	}
	if allowed, _ := r.canSubmit("AAA"); allowed {
		t.Fatal("submissions were allowed after a failed restart scan")
	}
}

// TestClearSymbolRequiresZeroResidual is ADR-0014 Consequence (e). ClearSymbol is
// a boolean delete, not a refcount: clearing on the first resolution would open a
// symbol that still carries other unresolved-ambiguous intents — a live
// duplicate-exposure fail-open.
func TestClearSymbolRequiresZeroResidual(t *testing.T) {
	r := newRig(t, withThreshold(5))
	seedSubmitAttempted(t, r.db, "i-a", "005930")
	seedSubmitAttempted(t, r.db, "i-b", "005930")
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("two ambiguous intents on one symbol did not block it")
	}

	// A human retires ONE of them (only a human can — the reconciler has no handle).
	if err := r.db.ResolveIntent(context.Background(), "i-a", "human-resolved"); err != nil {
		t.Fatalf("simulate human resolution: %v", err)
	}
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("the symbol was re-opened while another ambiguous intent was still unresolved")
	}
	if r.log.contains("clear-symbol:005930") {
		t.Fatal("ClearSymbol was called with residual ambiguous evidence still present")
	}

	// Once the residual count reaches zero, the block auto-clears.
	if err := r.db.ResolveIntent(context.Background(), "i-b", "human-resolved"); err != nil {
		t.Fatalf("simulate human resolution: %v", err)
	}
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("the symbol stayed blocked (%s) with zero residual evidence", reason)
	}
	if !r.log.contains("clear-symbol:005930") {
		t.Fatal("expected the residual-zero auto-clear")
	}
}

// TestGlobalHaltRefiresAfterOperatorClear is ADR-0014 Decision 6: the reconciler
// never clears a global halt, but it re-fires from live backlog evidence. Because
// the measure is the CURRENT unresolved backlog rather than a time-windowed rate,
// this stickiness is mechanical — the halt keeps coming back until a human
// actually removes the evidence.
func TestGlobalHaltRefiresAfterOperatorClear(t *testing.T) {
	r := newRig(t, withThreshold(2))
	seedSubmitAttempted(t, r.db, "i-1", "AAA")
	seedSubmitAttempted(t, r.db, "i-2", "BBB")
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if haltPhase(t, r.db) != store.HaltHalted {
		t.Fatal("the backlog did not trip the global halt")
	}

	// The operator clears it, but the backlog is untouched.
	if err := r.sw.ClearHalt(context.Background()); err != nil {
		t.Fatalf("operator clear: %v", err)
	}
	if haltPhase(t, r.db) != store.HaltNone {
		t.Fatal("the operator clear did not take effect")
	}

	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase after re-evaluation = %q, want the halt re-fired from live backlog evidence", got)
	}

	// And the reconciler itself never clears a global halt (Decision 5): the only
	// clear in this test was the operator's, issued directly on the kill switch.
	if r.log.contains("clear-halt") {
		t.Fatal("the reconciler must never clear a global halt")
	}
}

// TestAmbiguousWithUnrecoverableSymbolEscalatesGlobally: the per-symbol floor
// cannot be addressed without a symbol, and leaving the intent unblocked would be
// the one fail-open the policy exists to prevent.
func TestAmbiguousWithUnrecoverableSymbolEscalatesGlobally(t *testing.T) {
	r := newRig(t, withThreshold(99))
	if err := r.db.AppendIntent(context.Background(), store.Intent{
		IntentID:      "i-corrupt",
		ClientOrderID: "cid",
		Payload:       []byte("{not json"),
		CreatedAt:     baseTime,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := r.db.AppendMarker(context.Background(), "i-corrupt", store.MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("append marker: %v", err)
	}
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want a global halt when the local floor cannot be addressed", got)
	}
	assertUnresolved(t, r.path, "i-corrupt")
}

package reconciler

import (
	"reflect"
	"testing"
)

// TestSeamsStayNarrow is the enforcement twin of the invariants ADR-0014 argues
// from. Several of this package's safety claims are STRUCTURAL — they hold
// because the seam makes the unsafe call impossible to write, not because the
// current code happens not to make it. A widened seam silently invalidates the
// ADR's reasoning, so the widening has to fail a test rather than pass review.
//
// Each entry below names the decision it protects. If a future change genuinely
// needs one of these methods, ADR-0014 must be amended in the same change (its
// Decision 8 twin-artifact note spells out the one conditional exception, the
// loss-window-only query, and what must be true before it is allowed back).
func TestSeamsStayNarrow(t *testing.T) {
	cases := []struct {
		seam      reflect.Type
		forbidden map[string]string
	}{
		{
			seam: reflect.TypeOf((*OrderAPI)(nil)).Elem(),
			forbidden: map[string]string{
				"SubmitOrder": "ADR-0003 point 4: the reconciler must never submit or re-submit an order. Truth recovery must not be able to become a re-send.",
			},
		},
		{
			seam: reflect.TypeOf((*Journal)(nil)).Elem(),
			forbidden: map[string]string{
				"AppendIntent":    "ADR-0003 point 3: the reconciler must not be able to forge journal state.",
				"AppendMarker":    "ADR-0003 point 3: an acked marker binds an orderId to an intent; letting the reconciler write one would make payload-guess auto-ack writable.",
				"Atomically":      "ADR-0012 Decision 2 (TripTx-free): a kill-switch write must never be bound into a reconciler transaction.",
				"Halt":            "ADR-0014 Decision 8 (no-guard): resolve/finalize must not be gated on any halt carrier — that guard froze finalization behind human-clear-only halts and bootHalt.",
				"MarkHaltPending": "ADR-0012: killswitch owns every halt durable write.",
				"TripHalt":        "ADR-0012: killswitch owns every halt durable write.",
				"ClearHalt":       "ADR-0004 point 6: a global halt is cleared by a human only.",
			},
		},
		{
			seam: reflect.TypeOf((*Guard)(nil)).Elem(),
			forbidden: map[string]string{
				"ClearHalt":                 "ADR-0004 point 6 / ADR-0014 Decision 5: the reconciler re-fires from evidence, it never clears a global halt.",
				"FinalizePendingHalt":       "ADR-0014 Decision 5: pending-halt finalization belongs to the operator/#36 path.",
				"Halt":                      "ADR-0014 Decision 8 (no-guard).",
				"HasUnpersistedPendingHalt": "ADR-0014 Decision 8 / codex R3: this signal is true for bootHalt too, so gating on it re-introduces the finalization freeze.",
				"ReportTokenRefreshFailure": "ADR-0014 Decision 7: token-refresh escalation is killswitch's own; it has no journal counterpart the reconciler could re-derive.",
			},
		},
	}

	for _, tc := range cases {
		for i := 0; i < tc.seam.NumMethod(); i++ {
			name := tc.seam.Method(i).Name
			if why, banned := tc.forbidden[name]; banned {
				t.Errorf("seam %s exposes %s, which must stay out of reach.\n%s",
					tc.seam.Name(), name, why)
			}
		}
	}
}

// TestReconcilerNeverSubmits is the behavioural companion to the structural test
// above: across every branch, no code path reaches an order-placing call. The
// fake API records every call it receives, and it has no submit surface at all —
// if one were added to the seam, this test would need a new expectation, which is
// the point.
func TestReconcilerNeverSubmits(t *testing.T) {
	r := newRig(t, withThreshold(2))
	seedPrepared(t, r.db, "i-1", "AAA")        // prepared-only
	seedSubmitAttempted(t, r.db, "i-2", "BBB") // ambiguous
	seedSubmitAttempted(t, r.db, "i-3", "CCC") // ambiguous (crosses the threshold)
	seedAcked(t, r.db, "i-4", "DDD", "ord-4")  // acked, rejected
	r.api.set("ord-4", "REJECTED", "DDD")
	// Age everything past both windows so every branch actually fires.
	r.clock.Advance(2 * minPreparedAbandonWindow)
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := r.cycle(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	// The ONLY API traffic the reconciler is allowed to generate is the orderId
	// truth lookup for an acked intent.
	if got := r.api.callCount("ord-4"); got == 0 {
		t.Fatal("the acked intent was never looked up")
	}
	for _, id := range []string{"i-1", "i-2", "i-3"} {
		if got := r.api.callCount(id); got != 0 {
			t.Fatalf("intent %q generated %d API calls; only acked intents have a handle", id, got)
		}
	}
}

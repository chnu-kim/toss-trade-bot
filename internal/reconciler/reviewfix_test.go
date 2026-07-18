package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Regressions for two fail-open holes found in review of this PR. Both are the
// same class: "we did not learn anything this cycle" was being treated as "there
// is nothing to worry about".

// TestBlockSurvivesTransientLookupFailure — codex review P2.
//
// The per-symbol auto-clear reconciles against the evidence a cycle ESTABLISHED.
// If an intent was blocked for an unclassifiable status and the next cycle's
// lookup merely fails (a network blip), the symbol's evidence was not disproved —
// it was not observed at all. Clearing on that ignorance re-opens a symbol whose
// order is still in doubt, which is precisely the over-clear ADR-0014 Decision 4
// exists to prevent.
func TestBlockSurvivesTransientLookupFailure(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "005930", "ord-1")
	r.api.set("ord-1", "SOME_FUTURE_STATUS", "005930")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("an unclassifiable status did not block the symbol")
	}

	// The next cycle cannot reach the API at all.
	r.api.fail("ord-1", errors.New("transport cut"))
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("a transient lookup failure cleared a standing per-symbol block; ignorance is not evidence of resolution")
	}
	if r.log.contains("clear-symbol:005930") {
		t.Fatal("ClearSymbol was called while the blocking evidence was merely unobserved")
	}

	// A lookup that genuinely establishes a terminal state does release it.
	r.api.clearFail("ord-1")
	r.api.set("ord-1", order.OrderStatusCanceled, "005930")
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	assertResolution(t, r.path, "i-1", ResolutionCanceled)
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("the symbol stayed blocked (%s) after its evidence was resolved", reason)
	}
}

// TestTransientLookupFailureCreatesNoNewBlock is the other half of the same
// decision: a transient failure must not INVENT a block either (ADR-0014
// Decision 10 calls it a delay, not evidence). Only a previously established
// block is preserved through ignorance.
func TestTransientLookupFailureCreatesNoNewBlock(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-1", "005930", "ord-1")
	r.api.fail("ord-1", errors.New("transport cut"))

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("a transient lookup failure created a new symbol block (%s)", reason)
	}
}

// TestBootScanIsRetriedAfterTransientFailure — codex review P2.
//
// A failed boot scan correctly refuses to open the replay gate and promotes a
// fail-closed halt. But the gate is only ever opened by the boot scan, so if the
// loop moved on to ordinary live cycles the bot would stay blocked by
// replay-gate-closed FOREVER — even after the operator clears the halt and even
// though later scans succeed. The fail-closed state must be recoverable without a
// process restart.
func TestBootScanIsRetriedAfterTransientFailure(t *testing.T) {
	r := newRig(t)
	r.journal.setLoadErr(errors.New("transient store failure"))

	stop, _ := runLoop(t, r)
	defer stop()

	waitFor(t, "the failed boot scan to promote fail-closed", func() bool {
		return r.log.contains("boot-halt")
	})
	if r.log.contains("notify-scan-complete") {
		t.Fatal("the replay gate opened despite a failed restart scan")
	}

	// The medium recovers and the operator clears the promoted halt.
	r.journal.setLoadErr(nil)
	waitFor(t, "the promoted halt to be clearable", func() bool {
		return r.sw.ClearHalt(context.Background()) == nil
	})
	if got := haltPhase(t, r.db); got != store.HaltNone {
		t.Fatalf("halt phase = %q after the operator clear", got)
	}

	// The next cycle must re-run the RESTART scan (not a plain live cycle), so the
	// gate finally opens and the bot can trade again.
	r.tick()
	waitFor(t, "the retried boot scan to open the replay gate", func() bool {
		return r.log.contains("notify-scan-complete")
	})
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("submissions are still blocked (%s) after a successful retried restart scan", reason)
	}
}

// TestBootScanIsNotRepeatedOnceComplete: the retry is a recovery path, not a
// per-cycle re-open. Once the gate is open, live cycles must not keep re-issuing
// the scan-complete signal (it would mask a genuinely stuck gate and re-run the
// two-pass boot work every tick).
func TestBootScanIsNotRepeatedOnceComplete(t *testing.T) {
	r := newRig(t)
	stop, _ := runLoop(t, r)
	defer stop()
	r.awaitBoot()

	r.tick()
	r.tick()

	if got := r.log.count("notify-scan-complete"); got != 1 {
		t.Fatalf("the replay gate was signalled %d times, want exactly once", got)
	}
}

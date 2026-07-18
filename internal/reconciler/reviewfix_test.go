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

// TestSuccessResetIsNotReplayedAgainstNewerFailures — codex adversarial review
// [high].
//
// The success reset used to run BEFORE the fill's own resolve, and a failed
// resolve leaves the intent unresolved for a later cycle. That made the reset
// replayable: an older FILL resets the streak, its resolve fails, a NEWER
// rejection is durably counted, and the next cycle replays the same fill's reset
// — erasing a failure that happened after it. That is the permanent-undercount
// fail-open the count ordering exists to prevent, arriving through the success
// side.
//
// Two properties close it: the resolve commits before the reset (so a resolved
// fill can never be re-driven and replay its reset), and a fill whose resolve had
// to be retried abandons its reset for good (leaving the counter high is the
// over-halt direction, which ADR-0012 point 4 explicitly sanctions).
func TestSuccessResetIsNotReplayedAgainstNewerFailures(t *testing.T) {
	db, path := openStore(t)
	sw := newSwitch(t, db, killswitch.Config{
		OrderFailureThreshold: 3,
		TokenRefreshThreshold: 3,
		TokenRefreshWindow:    time.Minute,
	})
	r := newRigWith(t, db, path, sw)

	seedAcked(t, r.db, "i-1", "AAA", "ord-1") // older: FILLED
	seedAcked(t, r.db, "i-2", "AAA", "ord-2") // newer: REJECTED
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	r.api.set("ord-2", order.OrderStatusRejected, "AAA")

	// The fill's resolve does not land.
	r.journal.failResolve("i-1", errors.New("durable medium failure"))
	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertUnresolved(t, r.path, "i-1")
	if got := orderFailureCount(t, r.db); got != 1 {
		t.Fatalf("counter = %d after the newer rejection, want 1", got)
	}
	// A genuine journal durability failure is fail-closed, exactly as the submit
	// path treats a marker-write failure (ADR-0005 point 6). It also means any
	// window in which a stale reset could exist is a window where the bot is
	// already blocked.
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want a durable journal failure to fail closed", got)
	}

	// The medium recovers and the fill's resolve is retried.
	r.journal.clearResolveFailures()
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	assertResolution(t, r.path, "i-1", ResolutionFilled)
	if got := orderFailureCount(t, r.db); got != 1 {
		t.Fatalf("counter = %d, want the NEWER rejection to survive the retried fill's resolution", got)
	}
}

// TestSuccessResetFollowsItsOwnResolve pins the ordering the fix rests on: the
// fill is durably closed BEFORE its counter reset, so the reset can never be
// re-driven. (The reverse order is safe for the FAILURE path and required there —
// count-before-resolve — but on the success path it is what made the replay
// possible.)
func TestSuccessResetFollowsItsOwnResolve(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-fill", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	resolve := r.log.indexOf("resolve:i-fill:" + ResolutionFilled)
	reset := r.log.indexOf("report-order-success")
	if resolve < 0 || reset < 0 {
		t.Fatalf("expected both a resolve and a reset, got %v", r.log.snapshot())
	}
	if resolve > reset {
		t.Fatalf("the success reset ran before its own resolve, making it replayable: %v", r.log.snapshot())
	}
}

// TestBootScanCancellationDoesNotPromoteFailClosed — codex review round 2 [P2].
//
// A shutdown that lands while the boot scan is inside the journal scan surfaces
// as a context error. Promoting that to a fail-closed halt would latch bootHalt
// and write a durable global halt, so an ordinary Ctrl-C during startup would
// leave the NEXT run blocked until a human cleared it — a self-inflicted outage
// from a normal event (fail-closed-wrong-direction). The gate stays shut either
// way, which is the safe state; nothing needs to be escalated.
func TestBootScanCancellationDoesNotPromoteFailClosed(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.journal.setLoadErr(context.Canceled)

	if err := r.rec.BootScan(ctx); err == nil {
		t.Fatal("BootScan reported success despite a cancelled scan")
	}
	if r.log.contains("notify-scan-complete") {
		t.Fatal("the replay gate opened on a cancelled scan")
	}
	if r.log.contains("boot-halt") {
		t.Fatal("a cancelled shutdown was promoted to a fail-closed halt")
	}
	if got := haltPhase(t, r.db); got != store.HaltNone {
		t.Fatalf("halt phase = %q, want a clean shutdown to leave no durable halt behind", got)
	}
}

// TestAbandonedResetAlwaysPairsWithADurableHalt is the reachability argument that
// keeps the abandoned-reset bookkeeping safe even though it is in-memory only
// (codex review round 2 [P1]).
//
// The map cannot be made durable here — #35 does not modify store or killswitch,
// and there is no per-intent durable marker for "this reset was abandoned". What
// makes that acceptable is that the ONLY way to set it is a journal durability
// failure on a fill's resolve, and that same failure trips a DURABLE global halt.
// So a restart in that window inherits a standing, human-clear-only block: the
// stale reset cannot produce new exposure, and a human has to look at the bot
// before it trades again.
//
// This test pins that pairing. If a future change ever lets a reset be abandoned
// WITHOUT a durable halt standing, the in-memory map stops being sufficient and
// the residual becomes a genuine restart fail-open.
func TestAbandonedResetAlwaysPairsWithADurableHalt(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-fill", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	r.journal.failResolve("i-fill", errors.New("durable medium failure"))

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if !r.rec.resetAbandoned("i-fill") {
		t.Fatal("a fill whose resolve failed did not abandon its success reset")
	}
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q; an abandoned reset must always be paired with a durable halt, otherwise the in-memory bookkeeping is not restart-safe", got)
	}
	if allowed, _ := r.canSubmit("AAA"); allowed {
		t.Fatal("submissions were allowed in the window where a stale reset can exist")
	}
}

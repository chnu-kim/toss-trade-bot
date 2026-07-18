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

// TestSuccessResetSkippedWhenANewerFailureIsAlreadyCounted — codex adversarial
// review round 2 [high], generalized.
//
// The in-doubt guard is one-sided: it defers a fill's reset behind OLDER
// undetermined intents, but nothing stopped a fill's reset from landing after a
// NEWER intent's failure had already been counted. That needs no crash and no
// resolve failure at all — an older fill whose lookup was slow is simply
// established a cycle later than a newer rejection:
//
//	cycle 1: older A's lookup fails (undetermined); newer B is REJECTED → counted.
//	cycle 2: A is FILLED → its reset would zero the streak, erasing B.
//
// The streak's meaning is "failures since the last success in submit order", so a
// reset that is older than an already-counted failure must be dropped. Dropping it
// only leaves the counter high — the over-halt direction.
func TestSuccessResetSkippedWhenANewerFailureIsAlreadyCounted(t *testing.T) {
	db, path := openStore(t)
	sw := newSwitch(t, db, killswitch.Config{
		OrderFailureThreshold: 5,
		TokenRefreshThreshold: 5,
		TokenRefreshWindow:    time.Minute,
	})
	r := newRigWith(t, db, path, sw)

	seedAcked(t, r.db, "i-1", "AAA", "ord-1") // older, lookup fails first
	seedAcked(t, r.db, "i-2", "AAA", "ord-2") // newer, rejected
	r.api.fail("ord-1", errors.New("lookup exhausted"))
	r.api.set("ord-2", order.OrderStatusRejected, "AAA")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if got := orderFailureCount(t, r.db); got != 1 {
		t.Fatalf("counter = %d after the newer rejection, want 1", got)
	}

	// The older fill is only established now — after that newer failure was counted.
	r.api.clearFail("ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	assertResolution(t, r.path, "i-1", ResolutionFilled)
	if got := orderFailureCount(t, r.db); got != 1 {
		t.Fatalf("counter = %d — an older fill's reset erased an already-counted newer rejection", got)
	}
	if r.log.contains("report-order-success") {
		t.Fatal("the stale success reset was applied")
	}
}

// TestSuccessResetStillAppliesAfterOnlyOlderFailures is the other side: a fill
// that is genuinely the newest outcome MUST reset the streak (ADR-0012 point 4).
// The guard must not degrade into "never reset".
func TestSuccessResetStillAppliesAfterOlderFailures(t *testing.T) {
	db, path := openStore(t)
	sw := newSwitch(t, db, killswitch.Config{
		OrderFailureThreshold: 5,
		TokenRefreshThreshold: 5,
		TokenRefreshWindow:    time.Minute,
	})
	r := newRigWith(t, db, path, sw)

	seedAcked(t, r.db, "i-1", "AAA", "ord-1") // older, rejected
	seedAcked(t, r.db, "i-2", "AAA", "ord-2") // newer, filled
	r.api.set("ord-1", order.OrderStatusRejected, "AAA")
	r.api.setOrder(order.Order{
		OrderID: "ord-2", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !r.log.contains("report-order-success") {
		t.Fatal("a fill that is the newest outcome did not reset the streak")
	}
	if got := orderFailureCount(t, r.db); got != 0 {
		t.Fatalf("counter = %d, want the newest fill to have reset the streak", got)
	}
}

// TestPreexistingFillDoesNotResetTheStreak — codex adversarial review round 3
// [high].
//
// In-process ordering cannot survive a restart, and the consecutive-failure
// streak is DURABLE precisely so a restart cannot reset it (ADR-0004 point 7). A
// fill that predates this process may therefore be older than failures a previous
// process already counted, and this process has no way to prove otherwise —
// resolved intents are gone from the journal.
//
// So a fill whose submit predates this process's first scan does not reset the
// streak at all. Withholding only leaves the counter high (over-halt, the
// direction ADR-0012 point 4 sanctions) and it self-corrects on the next fill this
// process actually orders. The alternative — resetting on a guess — is a permanent
// undercount of a durable safety counter.
func TestPreexistingFillDoesNotResetTheStreak(t *testing.T) {
	r := newRig(t)
	// A previous process durably counted two failures.
	if err := r.db.SetCounter(context.Background(), store.Counter{Name: counterOrderFailureName, Value: 2}); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	seedAcked(t, r.db, "i-old", "AAA", "ord-old")
	r.api.setOrder(order.Order{
		OrderID: "ord-old", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	// The process starts AFTER those intents were written — i.e. they are the
	// crash-recovery population, not something this process submitted.
	r.clock.Advance(time.Minute)

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	assertResolution(t, r.path, "i-old", ResolutionFilled)
	if got := orderFailureCount(t, r.db); got != 2 {
		t.Fatalf("counter = %d — a fill that predates this process reset a durable streak it cannot order itself against", got)
	}
	if r.log.contains("report-order-success") {
		t.Fatal("a replayed (pre-existing) fill applied a success reset")
	}
}

// TestFillSubmittedInThisProcessResetsTheStreak is the self-correction half: once
// a fill's whole lifetime is inside this process, its ordering IS known, so the
// reset applies normally and the withheld state does not become permanent.
func TestFillSubmittedInThisProcessResetsTheStreak(t *testing.T) {
	r := newRig(t)
	if err := r.db.SetCounter(context.Background(), store.Counter{Name: counterOrderFailureName, Value: 2}); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	if err := r.boot(); err != nil { // nothing pre-existing
		t.Fatalf("boot: %v", err)
	}

	// A submit that happens while this process is running.
	seedAcked(t, r.db, "i-new", "AAA", "ord-new")
	r.api.setOrder(order.Order{
		OrderID: "ord-new", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	assertResolution(t, r.path, "i-new", ResolutionFilled)
	if !r.log.contains("report-order-success") {
		t.Fatal("a fill this process ordered end to end did not reset the streak")
	}
	if got := orderFailureCount(t, r.db); got != 0 {
		t.Fatalf("counter = %d, want the streak reset by a fully-ordered fill", got)
	}
}

// TestResolutionConflictDoesNotApplyTheAttemptedVerdictsSideEffects — codex review
// round 3 [P2].
//
// A resolution conflict means the journal recorded a DIFFERENT verdict and the
// first one is never overwritten — this reconciler lost the race. Treating the
// attempted verdict as if it won would then apply that verdict's durable counter
// side effect: a fill that the journal does not call a fill would still reset the
// consecutive-failure streak. The conflict must be reported and audited (it is),
// but its side effects must not fire.
//
// The rejection path is deliberately asymmetric: count-before-resolve requires the
// failure to be durable BEFORE the resolve is attempted, so a conflict discovered
// afterwards leaves an overcount. That direction over-halts, which is the safe one.
func TestResolutionConflictDoesNotApplyTheAttemptedVerdictsSideEffects(t *testing.T) {
	r := newRig(t)
	if err := r.db.SetCounter(context.Background(), store.Counter{Name: counterOrderFailureName, Value: 2}); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	seedAcked(t, r.db, "i-conflict", "AAA", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "AAA", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "1"},
	})

	// Something else closed it first, with a different verdict.
	if err := r.db.ResolveIntent(context.Background(), "i-conflict", ResolutionCanceled); err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	r.journal.Journal = conflictingJournal{Journal: r.journal.Journal, intentID: "i-conflict"}

	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	if r.log.contains("report-order-success") {
		t.Fatal("a lost resolution race still applied the fill's counter reset")
	}
	if got := orderFailureCount(t, r.db); got != 2 {
		t.Fatalf("counter = %d, want the durable streak untouched by a verdict that did not win", got)
	}
	// The conflict itself is still surfaced, never swallowed (#28).
	var audited bool
	for _, ev := range r.sink.errorEvents() {
		if ev.ErrorClass == "resolution-conflict" && ev.IntentID == "i-conflict" {
			audited = true
		}
	}
	if !audited {
		t.Fatal("the resolution conflict was not recorded on the durable audit medium")
	}
	assertResolution(t, r.path, "i-conflict", ResolutionCanceled)
}

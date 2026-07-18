package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// runLoop starts Run in the background and returns a stop function that cancels
// it and waits for the exit, plus a channel carrying Run's error.
func runLoop(t *testing.T, r *rig) (stop func(), errCh chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh = make(chan error, 1)
	go func() { errCh <- r.rec.Run(ctx) }()
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("Run did not exit after cancellation")
		}
	}, errCh
}

// TestTicker_QuietMarketFloorTransition is ADR-0014 Consequence (a) and the whole
// reason the ticker exists.
//
// The submit path's wake seam is best-effort AND only fires while something is
// being submitted. In a quiet market nothing wakes the reconciler, so without a
// cadence the per-symbol floor for a submit-attempted intent whose wake was lost
// would be deferred until the next reboot. The ticker closes exactly that gap —
// here with no wake at all.
func TestTicker_QuietMarketFloorTransition(t *testing.T) {
	r := newRig(t, withThreshold(99))
	seedSubmitAttempted(t, r.db, "i-amb", "005930")

	stop, _ := runLoop(t, r)
	defer stop()

	// The boot scan runs at a time when the intent is still inside the settle
	// window, so nothing is blocked yet.
	r.awaitBoot()
	if allowed, _ := r.canSubmit("005930"); !allowed {
		t.Fatal("the symbol was blocked before the settle window elapsed")
	}

	// Time passes with NO submissions at all — only the cadence fires.
	r.pastSettle()
	r.tick()

	allowed, reason := r.canSubmit("005930")
	if allowed {
		t.Fatal("the cadence did not apply the per-symbol floor in a quiet market")
	}
	if reason != "symbol-blocked" {
		t.Fatalf("blocked for %q, want the per-symbol ambiguous floor", reason)
	}
}

// TestTicker_GlobalRefireInQuietMarket: same gap for the global escalation. An
// operator clear followed by silence must not leave the bot running with an
// over-threshold backlog standing.
func TestTicker_GlobalRefireInQuietMarket(t *testing.T) {
	r := newRig(t, withThreshold(2))
	seedSubmitAttempted(t, r.db, "i-1", "AAA")
	seedSubmitAttempted(t, r.db, "i-2", "BBB")
	r.pastSettle()

	stop, _ := runLoop(t, r)
	defer stop()

	r.awaitBoot()
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q after the boot scan, want the backlog escalation", got)
	}

	if err := r.sw.ClearHalt(context.Background()); err != nil {
		t.Fatalf("operator clear: %v", err)
	}
	if got := haltPhase(t, r.db); got != store.HaltNone {
		t.Fatalf("halt phase = %q, want the operator clear to have landed", got)
	}

	r.tick()
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q after a quiet-market re-evaluation, want the backlog to have re-fired", got)
	}
}

// TestWakeDrivesImmediately: the submit path's wake seam converges a just-left
// unresolved intent without waiting for the next tick (ADR-0003 point 1).
func TestWakeDrivesImmediately(t *testing.T) {
	r := newRig(t, withThreshold(99))
	stop, _ := runLoop(t, r)
	defer stop()
	r.awaitBoot()

	seedSubmitAttempted(t, r.db, "i-amb", "005930")
	r.pastSettle()

	r.rec.Wake() // no tick is sent in this test

	waitFor(t, "the woken cycle to block the symbol", func() bool {
		allowed, _ := r.canSubmit("005930")
		return !allowed
	})
}

// TestWakeIsNonBlocking: the submit path calls this on its hot path and must
// never be parked by it, so a wake that arrives while one is already pending is
// collapsed rather than blocking the caller.
func TestWakeIsNonBlocking(t *testing.T) {
	r := newRig(t)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			r.rec.Wake() // nothing is consuming the channel
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wake blocked its caller")
	}
}

// TestTickerDeathPromotesFailClosed is ADR-0014 Consequence (h)/Decision 12.
// Every bounded-ness claim in ADR-0014 rests on this loop continuing to run, so a
// loop that cannot run must stop the bot from creating new exposure rather than
// quietly leaving those windows open.
func TestTickerDeathPromotesFailClosed(t *testing.T) {
	r := newRig(t)
	ticks := make(chan time.Time)
	r.rec.ticks = ticks

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- r.rec.Run(ctx) }()

	r.awaitBoot()
	close(ticks) // the cadence disappears

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrTickerStopped) {
			t.Fatalf("Run returned %v, want ErrTickerStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after the ticker stopped")
	}

	if !r.log.contains("boot-halt") {
		t.Fatal("a dead cadence must promote to an in-memory fail-closed halt")
	}
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want the promotion to be durable too", got)
	}
	if allowed, _ := r.canSubmit("AAA"); allowed {
		t.Fatal("submissions were still allowed after the reconciler loop died")
	}
}

// TestSustainedCycleFailurePromotesFailClosed: one bad cycle is not an emergency
// (the next tick retries), but a loop that keeps panicking is — it is no longer
// bounding anything.
func TestSustainedCycleFailurePromotesFailClosed(t *testing.T) {
	r := newRig(t)
	stop, _ := runLoop(t, r)
	defer stop()
	r.awaitBoot()

	r.journal.setLoadPanic(true)
	for i := 0; i < maxConsecutiveCycleFailures; i++ {
		r.tickExpectingFailure()
	}

	waitFor(t, "the fail-closed promotion", func() bool { return r.log.contains("boot-halt") })
	waitFor(t, "the durable promotion", func() bool { return haltPhase(t, r.db) == store.HaltHalted })
	if allowed, _ := r.canSubmit("AAA"); allowed {
		t.Fatal("submissions were still allowed after the reconciler stopped working")
	}
}

// TestSingleCycleFailureDoesNotPromote: the panic is contained (the loop survives
// it) and a single failure does not halt the bot — that would make a transient
// store hiccup an outage.
func TestSingleCycleFailureDoesNotPromote(t *testing.T) {
	r := newRig(t)
	stop, _ := runLoop(t, r)
	defer stop()
	r.awaitBoot()

	r.journal.setLoadPanic(true)
	r.tickExpectingFailure()

	// The loop survived the panic: it still services the next tick.
	r.journal.setLoadPanic(false)
	seedSubmitAttempted(t, r.db, "i-amb", "005930")
	r.pastSettle()
	r.tick()

	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("the loop stopped working after a contained panic")
	}
	if got := haltPhase(t, r.db); got != store.HaltNone {
		t.Fatalf("halt phase = %q, want no promotion after a single contained failure", got)
	}
}

// TestCancellationIsNotTreatedAsAnUnsustainableLoop: an ordinary shutdown must
// not leave a durable halt behind for the next boot to inherit.
func TestCancellationIsNotTreatedAsAnUnsustainableLoop(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- r.rec.Run(ctx) }()
	r.awaitBoot()

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit on cancellation")
	}

	if r.log.contains("boot-halt") {
		t.Fatal("a clean shutdown promoted a fail-closed halt")
	}
	if got := haltPhase(t, r.db); got != store.HaltNone {
		t.Fatalf("halt phase = %q, want none after a clean shutdown", got)
	}
}

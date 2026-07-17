package killswitch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// durable-before-visible (ADR-0012 Decision 1): a durable write error must not
// roll the mirror back to unhalted — it stays blocked. Both the MarkHaltPending
// arm (latch) and the TripHalt arm (durable pending) are blocked, never allowed.
func TestDurableBeforeVisibleWriteErrorStaysBlocked(t *testing.T) {
	ctx := context.Background()

	t.Run("TripHalt error leaves mirror pending, not unhalted", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		rec := &recordingNotifier{}
		k := newOpen(t, cs, WithNotifier(rec))
		cs.set(func(c *controlStore) { c.errTripHalt = errors.New("disk full") })

		if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err == nil {
			t.Fatalf("Trip did not surface the TripHalt error")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after failed TripHalt")
		} else if reason != "global-halt:pending" {
			t.Fatalf("blocked reason = %q, want global-halt:pending", reason)
		}
		// The durable pending phase (MarkHaltPending committed) backs the mirror.
		if p := haltPhase(t, cs.Store); p != store.HaltPending {
			t.Fatalf("durable phase = %s, want pending", p)
		}
		// A durable pending block was established → the operator must be notified,
		// consistent with the MarkHaltPending-error latch arm.
		if rec.count() == 0 {
			t.Fatalf("TripHalt-error pending halt did not notify")
		}
	})

	t.Run("MarkHaltPending error latches and blocks", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.errMarkPending = errors.New("store down") })

		if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err == nil {
			t.Fatalf("Trip did not surface the MarkHaltPending error")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after failed MarkHaltPending")
		} else if reason != "unpersisted-pending-halt" {
			t.Fatalf("blocked reason = %q, want unpersisted-pending-halt", reason)
		}
		if p := haltPhase(t, cs.Store); p != store.HaltNone {
			t.Fatalf("durable phase = %s, want none (write failed)", p)
		}
		if !k.HasUnpersistedPendingHalt() {
			t.Fatalf("latch not set after failed MarkHaltPending")
		}
	})
}

// 2-phase lifecycle: a killswitch trip interrupted between MarkHaltPending and
// TripHalt leaves a durable pending that a fresh boot treats as halted; a
// completed trip boots halted too.
func TestTwoPhaseLifecycleRestart(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/store.db"

	// Drive a trip whose TripHalt fails → durable pending (crash simulated).
	cs := newControlStore(openStoreAt(t, path))
	k := newOpen(t, cs)
	cs.set(func(c *controlStore) { c.errTripHalt = errors.New("crash before trip-halt") })
	_ = k.Trip(ctx, ScopeGlobal, "", "manual", time.Now())
	if p := haltPhase(t, cs.Store); p != store.HaltPending {
		t.Fatalf("durable phase after interrupted trip = %s, want pending", p)
	}
	_ = cs.Store.(*store.DB).Close()

	// Fresh boot reads pending → halted (persistence-wins). Cannot use newOpen
	// here — it asserts the guard opens, but a halted boot must stay blocked.
	st2 := openStoreAt(t, path)
	t.Cleanup(func() { _ = st2.Close() })
	k2, err := New(ctx, st2, testConfig())
	if err != nil {
		t.Fatalf("New (restart): %v", err)
	}
	k2.NotifyScanComplete()
	if ok, reason := k2.CanSubmit("AAPL"); ok {
		t.Fatalf("interrupted-trip restart booted unhalted")
	} else if reason != "global-halt:pending" {
		t.Fatalf("blocked reason = %q, want global-halt:pending", reason)
	}

	// Completing the trip (TripHalt succeeds) → durable halted, still blocked
	// after a further restart.
	if err := k2.Trip(ctx, ScopeGlobal, "", "manual-complete", time.Now()); err != nil {
		t.Fatalf("completing Trip: %v", err)
	}
	if p := haltPhase(t, st2); p != store.HaltHalted {
		t.Fatalf("durable phase after completion = %s, want halted", p)
	}
}

// count-before-resolve (ADR-0012 point 3): the counter increment (and the
// threshold TripHalt in the same tx) is durable before ReportOrderFailure
// returns; overcount is absorbed; a durable error rolls back with no latch.
func TestReportOrderFailureCountFirst(t *testing.T) {
	ctx := context.Background()

	t.Run("increments durably and trips at threshold", func(t *testing.T) {
		st := openStore(t)
		k := newOpen(t, st)
		for i := 1; i <= 2; i++ { // threshold is 3
			if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
				t.Fatalf("ReportOrderFailure #%d: %v", i, err)
			}
			if got := counterValue(t, st, counterOrderFailure); got != int64(i) {
				t.Fatalf("counter after #%d = %d, want %d", i, got, i)
			}
			if ok, _ := k.CanSubmit("AAPL"); !ok {
				t.Fatalf("blocked before threshold at #%d", i)
			}
		}
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure #3: %v", err)
		}
		if p := haltPhase(t, st); p != store.HaltHalted {
			t.Fatalf("durable phase at threshold = %s, want halted", p)
		}
		if ok, _ := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed at threshold")
		}

		// Overcount absorbed: a further failure still increments the evidence
		// counter (W-D) and stays halted (idempotent halt write).
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure overcount: %v", err)
		}
		if got := counterValue(t, st, counterOrderFailure); got != 4 {
			t.Fatalf("overcount counter = %d, want 4", got)
		}
		if p := haltPhase(t, st); p != store.HaltHalted {
			t.Fatalf("phase after overcount = %s, want halted", p)
		}
	})

	t.Run("durable error rolls back with no latch (reconciler re-count)", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.errAtomically = errors.New("tx failed") })
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err == nil {
			t.Fatalf("ReportOrderFailure did not surface the tx error")
		}
		cs.set(func(c *controlStore) { c.errAtomically = nil })

		if got := counterValue(t, cs.Store, counterOrderFailure); got != 0 {
			t.Fatalf("counter after rolled-back failure = %d, want 0", got)
		}
		if k.HasUnpersistedPendingHalt() {
			t.Fatalf("order-failure durable error set a latch (must rely on re-count)")
		}
		// order-failure is reconstructable: after the in-flight window, the guard
		// is not stuck — the reconciler re-count recovers the lost increment.
		if ok, reason := k.CanSubmit("AAPL"); !ok {
			t.Fatalf("guard stuck blocked after order-failure durable error: %s", reason)
		}
	})
}

// ReportOrderSuccess resets the streak durably without touching the halt state
// (I6): after a trip, a success resets the counter but the halt stands.
func TestReportOrderSuccessResetsWithoutTouchingHalt(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	k := newOpen(t, st)
	for i := 0; i < 3; i++ {
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	if p := haltPhase(t, st); p != store.HaltHalted {
		t.Fatalf("not halted after threshold: %s", p)
	}
	if err := k.ReportOrderSuccess(ctx); err != nil {
		t.Fatalf("ReportOrderSuccess: %v", err)
	}
	if got := counterValue(t, st, counterOrderFailure); got != 0 {
		t.Fatalf("counter after success = %d, want 0", got)
	}
	// I6: the mirror is untouched — the halt still stands.
	if p := haltPhase(t, st); p != store.HaltHalted {
		t.Fatalf("ReportOrderSuccess cleared the halt (I6 violated): %s", p)
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("ReportOrderSuccess reopened submits (I6 violated)")
	}
}

// Token-refresh escalation: windowed persist counter, same-tx TripHalt at
// threshold, durable counter survives restart, and a below-threshold persist
// failure latches (ADR-0013 latch scope (b)).
func TestReportTokenRefreshFailure(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now()

	t.Run("counts within window and trips at threshold", func(t *testing.T) {
		st := openStore(t)
		k := newOpen(t, st)
		if err := k.ReportTokenRefreshFailure(ctx, t0); err != nil {
			t.Fatalf("token failure #1: %v", err)
		}
		if ok, _ := k.CanSubmit("AAPL"); !ok {
			t.Fatalf("blocked below token threshold")
		}
		if err := k.ReportTokenRefreshFailure(ctx, t0.Add(30*time.Second)); err != nil {
			t.Fatalf("token failure #2: %v", err)
		}
		if p := haltPhase(t, st); p != store.HaltHalted {
			t.Fatalf("phase at token threshold = %s, want halted", p)
		}
	})

	t.Run("failure past window restarts the count", func(t *testing.T) {
		st := openStore(t)
		k := newOpen(t, st)
		if err := k.ReportTokenRefreshFailure(ctx, t0); err != nil {
			t.Fatalf("token failure: %v", err)
		}
		if err := k.ReportTokenRefreshFailure(ctx, t0.Add(2*time.Minute)); err != nil {
			t.Fatalf("token failure past window: %v", err)
		}
		if got := counterValue(t, st, counterTokenRefresh); got != 1 {
			t.Fatalf("counter after window reset = %d, want 1", got)
		}
		if ok, _ := k.CanSubmit("AAPL"); !ok {
			t.Fatalf("tripped despite window reset")
		}
	})

	t.Run("durable counter survives restart", func(t *testing.T) {
		path := t.TempDir() + "/store.db"
		st := openStoreAt(t, path)
		k := newOpen(t, st)
		if err := k.ReportTokenRefreshFailure(ctx, t0); err != nil {
			t.Fatalf("token failure: %v", err)
		}
		_ = st.Close()

		st2 := openStoreAt(t, path)
		t.Cleanup(func() { _ = st2.Close() })
		if got := counterValue(t, st2, counterTokenRefresh); got != 1 {
			t.Fatalf("counter after restart = %d, want 1 (must not reset to 0)", got)
		}
		k2 := newOpen(t, st2)
		if err := k2.ReportTokenRefreshFailure(ctx, t0.Add(30*time.Second)); err != nil {
			t.Fatalf("token failure after restart: %v", err)
		}
		if p := haltPhase(t, st2); p != store.HaltHalted {
			t.Fatalf("restart counter did not carry to threshold: phase %s", p)
		}
	})

	t.Run("below-threshold persist failure latches", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.errTxSetCount = errors.New("counter persist failed") })
		if err := k.ReportTokenRefreshFailure(ctx, t0); err == nil {
			t.Fatalf("token failure did not surface persist error")
		}
		if !k.HasUnpersistedPendingHalt() {
			t.Fatalf("below-threshold token persist failure did not latch")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after latched token failure")
		} else if reason != "unpersisted-pending-halt" {
			t.Fatalf("blocked reason = %q, want unpersisted-pending-halt", reason)
		}
	})
}

// graceful-shutdown affordance (ADR-0013): HasUnpersistedPendingHalt is true for
// a latch OR a bootHalt; FinalizePendingHalt durably promotes the latch, and on
// failure still reports the halt as unpersisted.
func TestGracefulShutdownAffordance(t *testing.T) {
	ctx := context.Background()

	t.Run("bootHalt is reported", func(t *testing.T) {
		k := newOpen(t, openStore(t))
		if k.HasUnpersistedPendingHalt() {
			t.Fatalf("clean switch reported unpersisted pending")
		}
		k.BootHalt()
		if !k.HasUnpersistedPendingHalt() {
			t.Fatalf("bootHalt not reported by HasUnpersistedPendingHalt")
		}
	})

	t.Run("FinalizePendingHalt promotes the latch durably", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.errMarkPending = errors.New("store down") })
		_ = k.Trip(ctx, ScopeGlobal, "", "manual", time.Now())
		if !k.HasUnpersistedPendingHalt() {
			t.Fatalf("expected latch before finalize")
		}
		// Store recovers; finalize promotes to durable halted.
		cs.set(func(c *controlStore) { c.errMarkPending = nil })
		if err := k.FinalizePendingHalt(ctx); err != nil {
			t.Fatalf("FinalizePendingHalt: %v", err)
		}
		if p := haltPhase(t, cs.Store); p != store.HaltHalted {
			t.Fatalf("durable phase after finalize = %s, want halted", p)
		}
		if k.HasUnpersistedPendingHalt() {
			t.Fatalf("latch still reported after successful finalize")
		}
	})

	t.Run("FinalizePendingHalt failure keeps the latch", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.errMarkPending = errors.New("store down") })
		_ = k.Trip(ctx, ScopeGlobal, "", "manual", time.Now())
		if err := k.FinalizePendingHalt(ctx); err == nil {
			t.Fatalf("FinalizePendingHalt did not surface the store error")
		}
		if !k.HasUnpersistedPendingHalt() {
			t.Fatalf("latch dropped after a failed finalize")
		}
	})
}

func TestNotifierFiresOnTrip(t *testing.T) {
	ctx := context.Background()
	rec := &recordingNotifier{}
	k := newOpen(t, openStore(t), WithNotifier(rec))
	if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if rec.count() == 0 {
		t.Fatalf("notifier not fired on global trip")
	}
}

package killswitch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// torn-read: while a trip is frozen after inflightTrips++ (I1) but before the
// durable write commits, durableHalt is still none yet inflightTrips is 1. The
// single mu snapshot reads both together, so CanSubmit is blocked — the torn read
// (durableHalt low seen apart from inflightTrips high) is structurally impossible.
func TestTornReadInflightBlocksBeforeDurableRises(t *testing.T) {
	ctx := context.Background()
	cs := newControlStore(openStore(t))
	k := newOpen(t, cs)

	entered := make(chan struct{})
	release := make(chan struct{})
	cs.set(func(c *controlStore) {
		c.beforeMarkPend = func() { close(entered); <-release }
	})

	done := make(chan error, 1)
	go func() { done <- k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()) }()

	<-entered
	// Snapshot the frozen state: durableHalt none, inflightTrips 1.
	k.mu.Lock()
	dh, inflight := k.durableHalt, k.inflightTrips
	k.mu.Unlock()
	if dh != store.HaltNone || inflight != 1 {
		t.Fatalf("frozen state durableHalt=%s inflight=%d, want none/1", dh, inflight)
	}
	// CanSubmit reads both in one snapshot and blocks on inflightTrips.
	if ok, reason := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed while a trip is in flight")
	} else if reason != "trip-in-flight" {
		t.Fatalf("blocked reason = %q, want trip-in-flight", reason)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("trip: %v", err)
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed after the trip completed to halted")
	}
}

// I7: the decrement is the trip's final step, strictly after the block-carrier is
// published. The afterTripHalt hook runs post-commit but pre-publish; there the
// counter is still held (inflightTrips==1) and the mirror is not yet halted, so
// the guard is blocked. After the trip returns, the counter is released.
func TestI7DecrementFollowsCarrierPublish(t *testing.T) {
	ctx := context.Background()
	cs := newControlStore(openStore(t))
	k := newOpen(t, cs)

	cs.set(func(c *controlStore) {
		c.afterTripHalt = func() {
			k.mu.Lock()
			n, dh := k.inflightTrips, k.durableHalt
			k.mu.Unlock()
			if n != 1 {
				t.Errorf("inflightTrips at post-commit hook = %d, want 1 (dec must follow publish)", n)
			}
			if dh != store.HaltPending {
				t.Errorf("mirror at post-commit hook = %s, want pending (halted not yet published)", dh)
			}
			if ok, _ := k.CanSubmit("AAPL"); ok {
				t.Errorf("submit allowed between commit and mirror publish")
			}
		}
	})

	if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	k.mu.Lock()
	n := k.inflightTrips
	k.mu.Unlock()
	if n != 0 {
		t.Fatalf("inflightTrips after trip = %d, want 0", n)
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed after global trip")
	}
}

// clobber (W3/W4/W7): a ClearHalt cannot clobber a trip that is publishing its
// carrier. While the trip holds haltMu, ClearHalt serializes behind it; when it
// finally runs it either defers (inflightTrips>0, the trip's halt stands) or
// clears a fully-completed halt (a legit operator clear). There is no window
// where the block is lost while the trip's carrier stands. ClearHalt never
// touches inflightTrips (I2).
func TestClearHaltCannotClobberInFlightTrip(t *testing.T) {
	ctx := context.Background()
	cs := newControlStore(openStore(t))
	k := newOpen(t, cs)

	entered := make(chan struct{})
	release := make(chan struct{})
	cs.set(func(c *controlStore) {
		c.beforeMarkPend = func() { close(entered); <-release }
	})

	tripErr := make(chan error, 1)
	go func() { tripErr <- k.Trip(ctx, ScopeGlobal, "", "order-escalation", time.Now()) }()
	<-entered

	// The in-flight trip already blocks submits via the inflightTrips carrier.
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("in-flight trip did not block submits")
	}

	clearErr := make(chan error, 1)
	go func() { clearErr <- k.ClearHalt(ctx) }()

	// Let the trip finish; it completes to durable halted while holding haltMu, so
	// ClearHalt only ever runs after the carrier is published.
	close(release)
	if err := <-tripErr; err != nil {
		t.Fatalf("trip: %v", err)
	}
	cerr := <-clearErr

	switch {
	case errors.Is(cerr, ErrClearDeferred):
		if ok, _ := k.CanSubmit("AAPL"); ok {
			t.Fatalf("clear deferred but submit allowed — trip's halt was clobbered")
		}
		if p := haltPhase(t, cs.Store); p != store.HaltHalted {
			t.Fatalf("clear deferred but durable phase = %s, want halted", p)
		}
	case cerr == nil:
		// Cleared only after the trip fully completed: must be fully consistent.
		if p := haltPhase(t, cs.Store); p != store.HaltNone {
			t.Fatalf("clear returned nil but durable phase = %s, want none", p)
		}
		if ok, _ := k.CanSubmit("AAPL"); !ok {
			t.Fatalf("clear returned nil but submit still blocked")
		}
	default:
		t.Fatalf("unexpected ClearHalt error: %v", cerr)
	}
}

// W-A: a manual/ambiguous MarkHaltPending failure latches — no live fail-open —
// and the latch is not lost across restart because graceful shutdown finalizes it
// durably (HasUnpersistedPendingHalt → FinalizePendingHalt → durable halted).
func TestWALatchSurvivesToDurable(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/store.db"
	cs := newControlStore(openStoreAt(t, path))
	k := newOpen(t, cs)

	cs.set(func(c *controlStore) { c.errMarkPending = errors.New("store down at decision") })
	_ = k.Trip(ctx, ScopeGlobal, "", "ambiguous-frequency", time.Now())

	// live: no fail-open.
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("live fail-open after failed MarkHaltPending")
	}
	if !k.HasUnpersistedPendingHalt() {
		t.Fatalf("latch not reported for graceful shutdown")
	}

	// graceful shutdown once the store recovers: finalize durably.
	cs.set(func(c *controlStore) { c.errMarkPending = nil })
	if err := k.FinalizePendingHalt(ctx); err != nil {
		t.Fatalf("FinalizePendingHalt: %v", err)
	}
	_ = cs.Store.(*store.DB).Close()

	// restart: the finalized halt is durable → boots halted (no restart loss).
	st2 := openStoreAt(t, path)
	t.Cleanup(func() { _ = st2.Close() })
	if p := haltPhase(t, st2); p != store.HaltHalted {
		t.Fatalf("durable phase after restart = %s, want halted (latch lost)", p)
	}
}

// W-B: the panic-span promotion follows the decision-locus — count-first
// order-failure promotes to in-memory bootHalt; every other trip latches.
func TestWBPanicPromotionByDecisionLocus(t *testing.T) {
	ctx := context.Background()

	t.Run("order-failure panic → bootHalt", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		// Reach the threshold so the tx runs TripHalt, then panic there.
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("failure #1: %v", err)
		}
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("failure #2: %v", err)
		}
		cs.set(func(c *controlStore) { c.panicTxTripHalt = true })
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err == nil {
			t.Fatalf("panicking ReportOrderFailure returned nil error")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after order-failure panic")
		} else if reason != "boot-halt" {
			t.Fatalf("blocked reason = %q, want boot-halt", reason)
		}
		k.mu.Lock()
		latched := k.unpersistedPending
		k.mu.Unlock()
		if latched {
			t.Fatalf("order-failure panic set a latch; W-B allows only bootHalt")
		}
	})

	t.Run("manual trip panic → latch", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		cs.set(func(c *controlStore) { c.panicMarkPending = true })
		if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err == nil {
			t.Fatalf("panicking Trip returned nil error")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after manual-trip panic")
		} else if reason != "unpersisted-pending-halt" {
			t.Fatalf("blocked reason = %q, want unpersisted-pending-halt", reason)
		}
		k.mu.Lock()
		boot := k.bootHalt
		k.mu.Unlock()
		if boot {
			t.Fatalf("manual-trip panic set bootHalt; W-B requires a latch")
		}
	})

	t.Run("token-failure panic → latch", func(t *testing.T) {
		cs := newControlStore(openStore(t))
		k := newOpen(t, cs)
		if err := k.ReportTokenRefreshFailure(ctx, time.Now()); err != nil {
			t.Fatalf("token failure #1: %v", err)
		}
		cs.set(func(c *controlStore) { c.panicTxTripHalt = true }) // trips at #2
		if err := k.ReportTokenRefreshFailure(ctx, time.Now()); err == nil {
			t.Fatalf("panicking token failure returned nil error")
		}
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("submit allowed after token panic")
		} else if reason != "unpersisted-pending-halt" {
			t.Fatalf("blocked reason = %q, want unpersisted-pending-halt", reason)
		}
	})
}

// W-C: while a trip is already halted, a fresh bare Trip(global) is an idempotent
// no-op — it does not re-write durable state and does not re-notify. The
// full-loss residual (an evidence-less bare one-shot Trip(global) that races a
// clear) is booked here honestly: killswitch cannot re-fire it, because a bare
// trip carries no persistent evidence (unlike count-first/ambiguous). That edge
// is out of this package's closure (ADR-0013 Consequences) and is why callers
// should trip via the evidence-bearing report paths where possible.
func TestWCIdempotentBareTripNoOp(t *testing.T) {
	ctx := context.Background()
	cs := newControlStore(openStore(t))
	rec := &recordingNotifier{}
	k := newOpen(t, cs, WithNotifier(rec))

	if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
		t.Fatalf("first Trip: %v", err)
	}
	firstNotify := rec.count()

	// A second trip while already halted must not re-write durable state; make any
	// durable halt write fail so a non-idempotent implementation would surface it.
	cs.set(func(c *controlStore) {
		c.errMarkPending = errors.New("must not be called")
		c.errTripHalt = errors.New("must not be called")
	})
	if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
		t.Fatalf("idempotent second Trip surfaced a durable write: %v", err)
	}
	if rec.count() != firstNotify {
		t.Fatalf("idempotent no-op re-notified: %d then %d", firstNotify, rec.count())
	}
	if p := haltPhase(t, cs.Store); p != store.HaltHalted {
		t.Fatalf("durable phase after no-op = %s, want halted", p)
	}
}

// W-D: evidence publication is independent of halt state. Even when already
// halted by an unrelated manual trip, a count-first ReportOrderFailure still
// increments its durable counter (only the halt durable write is idempotent), so
// the persistent evidence survives a later clear to re-fire.
func TestWDEvidencePublishedWhenAlreadyHalted(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	k := newOpen(t, st)

	if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
		t.Fatalf("manual Trip: %v", err)
	}
	if got := counterValue(t, st, counterOrderFailure); got != 0 {
		t.Fatalf("counter before any failure = %d, want 0", got)
	}
	// Already halted by the manual trip; the evidence counter must still advance.
	if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
		t.Fatalf("ReportOrderFailure while halted: %v", err)
	}
	if got := counterValue(t, st, counterOrderFailure); got != 1 {
		t.Fatalf("evidence counter while halted = %d, want 1 (W-D: evidence not skipped)", got)
	}
}

// W-E: CanSubmit never reads the evidence store. After a clear, the guard reopens
// immediately even though the durable failure counter is still at/over threshold;
// the delayed re-halt happens on the NEXT reporter event, not from CanSubmit.
func TestWEClearReopensThenNextEventReHalts(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	k := newOpen(t, st)

	// Drive to threshold (halted) and beyond, so the durable counter is >= threshold.
	for i := 0; i < 3; i++ {
		if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("not halted at threshold")
	}
	if got := counterValue(t, st, counterOrderFailure); got < 3 {
		t.Fatalf("counter = %d, want >= threshold", got)
	}

	// Operator clears. CanSubmit reopens immediately — it does NOT consult the
	// evidence counter (still >= threshold).
	if err := k.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if ok, reason := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("guard did not reopen after clear (CanSubmit read the evidence store?): %s", reason)
	}

	// The next reporter event re-halts (bounded delayed-halt).
	if err := k.ReportOrderFailure(ctx, "rejected", time.Now()); err != nil {
		t.Fatalf("post-clear ReportOrderFailure: %v", err)
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("next event did not re-halt after clear")
	}
}

// High-concurrency stress: a mix of every trip-triggering path, clears, and hot
// reads run together under -race. The regression guard is (a) no data race / no
// deadlock / no panic escapes, and (b) the system always converges to a clean,
// clearable state — draining ClearHalt (retrying past ErrClearDeferred) leaves
// the guard open and the durable halt none.
func TestConcurrentInterleavingStress(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	k := newOpen(t, st)

	const workers = 12
	const iters = 40
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch (id + i) % 7 {
				case 0:
					_ = k.Trip(ctx, ScopeGlobal, "", "manual", time.Now())
				case 1:
					_ = k.ReportOrderFailure(ctx, "rejected", time.Now())
				case 2:
					_ = k.ReportTokenRefreshFailure(ctx, time.Now())
				case 3:
					_ = k.ReportOrderSuccess(ctx)
				case 4:
					_ = k.Trip(ctx, ScopeSymbol, "AAPL", "ambiguous", time.Now())
					k.ClearSymbol("AAPL")
				case 5:
					_ = k.ClearHalt(ctx)
				case 6:
					r := k.Reserve("AAPL")
					if ok, _ := k.CanSubmit("AAPL"); ok {
						_, _ = k.Reconfirm(r)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	// Converge to clean: drain any in-flight trips, then clear.
	var lastErr error
	for attempt := 0; attempt < 100; attempt++ {
		lastErr = k.ClearHalt(ctx)
		if lastErr == nil {
			break
		}
		if !errors.Is(lastErr, ErrClearDeferred) {
			t.Fatalf("ClearHalt failed to converge: %v", lastErr)
		}
	}
	if lastErr != nil {
		t.Fatalf("ClearHalt never stopped deferring: %v", lastErr)
	}
	k.ClearSymbol("AAPL")
	if ok, reason := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("guard did not converge to open after drain: %s", reason)
	}
	if p := haltPhase(t, st); p != store.HaltNone {
		t.Fatalf("durable phase after convergence = %s, want none", p)
	}
}

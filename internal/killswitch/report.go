package killswitch

import (
	"context"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// ReportOrderFailure is the count-first order-failure escalation (ADR-0012
// point 3). The counter increment is the evidence and is published even when
// already halted (W-D); only the halt durable write is idempotent. It runs the
// increment and, at threshold, the TripHalt in the SAME transaction, so a crash
// rolls both back atomically and the reconciler re-count recovers (no latch —
// order-failure is reconstructable, ADR-0013). A panic promotes to bootHalt (the
// one path W-B allows an in-memory promotion for).
func (k *Switch) ReportOrderFailure(ctx context.Context, reason string, occurredAt time.Time) error {
	_ = occurredAt
	return k.withTripCarrier(
		k.BootHalt, // W-B: count-first order-failure panic → in-memory bootHalt
		func() error { return k.doReportOrderFailure(ctx, reason) },
	)
}

func (k *Switch) doReportOrderFailure(ctx context.Context, reason string) error {
	k.mu.Lock()
	already := k.durableHalt == store.HaltHalted
	k.mu.Unlock()

	var tripped bool
	err := k.store.Atomically(ctx, func(tx store.Tx) error {
		c, err := tx.Counter(ctx, counterOrderFailure)
		if err != nil {
			return err
		}
		c.Value++
		if err := tx.SetCounter(ctx, c); err != nil { // evidence (W-D)
			return err
		}
		// Halt durable write is idempotent: skip it when already halted, but the
		// counter increment above still lands (W-D).
		if c.Value >= int64(k.cfg.OrderFailureThreshold) && !already {
			if err := tx.TripHalt(ctx, reason); err != nil {
				return err
			}
			tripped = true
		}
		return nil
	})
	if err != nil {
		// Reconstructable: the atomic rollback leaves the intent unresolved and
		// the counter unchanged, so the reconciler re-count recovers — NO latch.
		// The in-flight window is fail-closed via inflightTrips until the deferred
		// decrement.
		return fmt.Errorf("killswitch: report order failure: %w", err)
	}
	if tripped {
		k.mu.Lock()
		k.durableHalt = store.HaltHalted // durable-before-visible (I3)
		k.mu.Unlock()
		k.notify(reason)
	}
	return nil
}

// ReportTokenRefreshFailure is the windowed token-refresh escalation. The
// counter is persisted (non-reconstructable — no journal equivalent), so ANY
// durable error latches, even below threshold: dropping the increment silently
// would let a store-error loop starve the threshold forever and bypass the
// escalation (ADR-0013 latch scope (b)). A panic latches (not order-failure).
func (k *Switch) ReportTokenRefreshFailure(ctx context.Context, occurredAt time.Time) error {
	return k.withTripCarrier(
		func() { k.latch(reasonTokenRefresh) },
		func() error { return k.doReportTokenRefresh(ctx, occurredAt) },
	)
}

func (k *Switch) doReportTokenRefresh(ctx context.Context, occurredAt time.Time) error {
	k.mu.Lock()
	already := k.durableHalt == store.HaltHalted
	k.mu.Unlock()

	var tripped bool
	err := k.store.Atomically(ctx, func(tx store.Tx) error {
		c, err := tx.Counter(ctx, counterTokenRefresh)
		if err != nil {
			return err
		}
		// Slide the window: a fresh window (or a failure past the window) restarts
		// the count at 1; otherwise increment within the current window.
		if c.WindowStart.IsZero() || occurredAt.Sub(c.WindowStart) > k.cfg.TokenRefreshWindow {
			c.WindowStart = occurredAt
			c.Value = 1
		} else {
			c.Value++
		}
		if err := tx.SetCounter(ctx, c); err != nil {
			return err
		}
		if c.Value >= int64(k.cfg.TokenRefreshThreshold) && !already {
			if err := tx.TripHalt(ctx, reasonTokenRefresh); err != nil {
				return err
			}
			tripped = true
		}
		return nil
	})
	if err != nil {
		// Non-reconstructable increment loss → latch even below threshold.
		k.latch(reasonTokenRefresh)
		k.notify(reasonTokenRefresh)
		return fmt.Errorf("killswitch: report token refresh failure: %w", err)
	}
	if tripped {
		k.mu.Lock()
		k.durableHalt = store.HaltHalted
		k.mu.Unlock()
		k.notify(reasonTokenRefresh)
	}
	return nil
}

// ReportOrderSuccess durably resets the consecutive order-failure counter. Per
// I6 it touches neither haltMu, nor inflightTrips, nor the mirror — only its own
// counter transaction. A reset failure is overcount-safe (the counter stays
// high, which is conservative — ADR-0012 point 4), so it is not part of the
// count-ordering contract.
func (k *Switch) ReportOrderSuccess(ctx context.Context) error {
	if err := k.store.SetCounter(ctx, store.Counter{Name: counterOrderFailure, Value: 0}); err != nil {
		return fmt.Errorf("killswitch: report order success (counter reset): %w", err)
	}
	return nil
}

package killswitch

import (
	"context"
	"errors"
	"fmt"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// ErrClearDeferred is returned by ClearHalt while a trip is in flight
// (inflightTrips > 0). The operator retries once the in-flight trips settle;
// this closes the ordering where a clear overlaps a trip's critical section
// (W-C). It is not a failure — the halt stays in effect.
var ErrClearDeferred = errors.New("killswitch: clear deferred — a trip is in flight, retry")

// ClearHalt is the manual global reset (ADR-0004 point 6). It is the ONLY path
// that lowers durableHalt to none and the ONLY runtime path that clears the
// latch and bootHalt. It never touches inflightTrips (only each owner decrements
// its own — I2).
//
// While a trip is in flight it defers (W-C): a concurrent trip has already
// incremented inflightTrips before any slow wait (I1), so ClearHalt sees it and
// refuses, rather than clearing a halt a trip is about to (re-)establish.
//
// On a durable ClearHalt error it keeps all three carriers (fail-closed).
func (k *Switch) ClearHalt(ctx context.Context) error {
	k.haltMu.Lock()
	defer k.haltMu.Unlock()

	k.mu.Lock()
	inflight := k.inflightTrips
	k.mu.Unlock()
	if inflight > 0 {
		return ErrClearDeferred
	}

	if err := k.store.ClearHalt(ctx); err != nil {
		return fmt.Errorf("killswitch: clear halt: %w", err)
	}

	k.mu.Lock()
	k.durableHalt = store.HaltNone
	k.unpersistedPending = false
	k.haltReason = ""
	k.bootHalt = false
	k.mu.Unlock()
	return nil
}

// FinalizePendingHalt durably promotes a latched (unpersisted) pending halt at
// graceful shutdown (ADR-0013). It re-commits MarkHaltPending→TripHalt with the
// latched reason; on success it clears the latch, on failure it keeps it. It is
// a no-op when nothing is latched. bootHalt is an in-memory contract and is NOT
// finalized here (it is re-derived on restart by its trigger).
func (k *Switch) FinalizePendingHalt(ctx context.Context) error {
	k.haltMu.Lock()
	defer k.haltMu.Unlock()

	k.mu.Lock()
	latched := k.unpersistedPending
	reason := k.haltReason
	k.mu.Unlock()
	if !latched {
		return nil
	}

	if err := k.store.MarkHaltPending(ctx, reason); err != nil {
		return fmt.Errorf("killswitch: finalize pending halt (mark pending): %w", err)
	}
	k.mu.Lock()
	k.durableHalt = store.HaltPending
	k.mu.Unlock()

	if err := k.store.TripHalt(ctx, reason); err != nil {
		// durable=pending is recorded; persistence-wins covers the restart. The
		// latch is retained so the in-memory guard stays blocked too.
		return fmt.Errorf("killswitch: finalize pending halt (trip halt): %w", err)
	}
	k.mu.Lock()
	k.durableHalt = store.HaltHalted
	k.unpersistedPending = false
	k.mu.Unlock()
	return nil
}

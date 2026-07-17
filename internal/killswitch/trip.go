package killswitch

import (
	"context"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Scope selects the blast radius of a Trip.
type Scope int

const (
	// ScopeGlobal trips the persisted global halt (manual-clear-only).
	ScopeGlobal Scope = iota
	// ScopeSymbol blocks a single symbol in memory (auto-clear, ADR-0004 point 4).
	ScopeSymbol
)

// Trip is the generic trigger sink (ADR-0004 point 7). A global trip runs the
// durable skeleton; a per-symbol trip is memory-only. occurredAt is accepted for
// caller symmetry and future audit wiring; the bare global trip does not persist
// it (the reconstruction evidence for ambiguous escalation lives in the caller's
// journal, out of this package's scope).
func (k *Switch) Trip(ctx context.Context, scope Scope, symbol, reason string, occurredAt time.Time) error {
	_ = occurredAt
	switch scope {
	case ScopeGlobal:
		return k.tripGlobal(ctx, reason)
	case ScopeSymbol:
		k.mu.Lock()
		k.perSymbolBlocked[symbol] = true
		k.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("killswitch: unknown scope %d", scope)
	}
}

// withTripCarrier runs a trip body under the disjoint in-flight carrier. It
// centralizes I1, I7 and the panic-span promotion (W-B) shared by every
// trip-triggering path (manual/ambiguous Trip, ReportOrderFailure,
// ReportTokenRefreshFailure):
//
//   - I1: inflightTrips++ happens before any slow wait (haltMu, store).
//   - I7: inflightTrips-- is a single deferred decrement, registered right after
//     the increment so it runs LAST — after body has published its own
//     block-carrier (durable write or latch) and after haltMu is released.
//   - W-B: a recover boundary inside the inc..dec span promotes to a conservative
//     halt (onPanic) BEFORE the decrement releases the counter.
//
// body runs while holding haltMu (I4 serialization).
func (k *Switch) withTripCarrier(onPanic func(), body func() error) (err error) {
	k.mu.Lock()
	k.inflightTrips++
	k.mu.Unlock()

	// Registered first => runs LAST (LIFO): the block-carrier is already
	// published by the time the counter is released (I7). Single deferred
	// decrement; no early explicit decrement anywhere.
	defer func() {
		k.mu.Lock()
		k.inflightTrips--
		k.mu.Unlock()
	}()

	// Recover boundary INSIDE the inflightTrips span (W-B): on panic, publish the
	// conservative carrier before the deferred decrement above runs.
	defer func() {
		if r := recover(); r != nil {
			onPanic()
			err = fmt.Errorf("killswitch: recovered panic during trip: %v", r)
		}
	}()

	k.haltMu.Lock()
	defer k.haltMu.Unlock()

	return body()
}

// tripGlobal is the bare/ambiguous global trip (no counter evidence). On a
// MarkHaltPending failure the durable state is none and non-reconstructable, so
// it latches; on a panic it latches (not order-failure, W-B).
func (k *Switch) tripGlobal(ctx context.Context, reason string) error {
	return k.withTripCarrier(
		func() { k.latch(reason) },
		func() error { return k.doGlobalHalt(ctx, reason) },
	)
}

// doGlobalHalt performs the two-phase durable halt write and publishes the mirror
// durable-before-visible (I3). Runs under haltMu, so durableHalt cannot change
// underneath it (every transition needs haltMu).
func (k *Switch) doGlobalHalt(ctx context.Context, reason string) error {
	k.mu.Lock()
	already := k.durableHalt == store.HaltHalted
	k.mu.Unlock()
	if already {
		// Idempotent no-op: the halt durable write is skipped, the mirror stays
		// halted (the existing durableHalt is the standing carrier). A bare trip
		// carries no evidence to publish (unlike the count-first paths, W-D).
		return nil
	}

	// MarkHaltPending is its own commit so an interrupted trip reloads as pending
	// (persistence-wins). A failure here leaves durable=none — non-reconstructable
	// for a manual/ambiguous trip, so latch (ADR-0013 latch scope (a)).
	if err := k.store.MarkHaltPending(ctx, reason); err != nil {
		k.latch(reason)
		k.notify(reason)
		return fmt.Errorf("killswitch: mark halt pending: %w", err)
	}
	// durable pending is now real — expose it (still blocked). durable-before-visible.
	k.mu.Lock()
	k.durableHalt = store.HaltPending
	k.mu.Unlock()

	if err := k.store.TripHalt(ctx, reason); err != nil {
		// durable=pending is already recorded — persistence-wins covers the
		// restart, so no latch is needed. The mirror stays pending (blocked); it
		// is NOT rolled back to none (that would fail open).
		return fmt.Errorf("killswitch: trip halt: %w", err)
	}
	k.mu.Lock()
	k.durableHalt = store.HaltHalted
	k.mu.Unlock()
	k.notify(reason)
	return nil
}

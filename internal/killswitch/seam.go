package killswitch

import (
	"context"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Store is the narrow slice of the store seam (issue #60) that killswitch
// consumes. killswitch never modifies internal/store; it depends only on this
// interface, so unit tests substitute a wrapper that injects durable-write
// errors and panics over a real temp-dir engine (ADR-0005 point 2). The
// concrete *store.DB satisfies it (a compile-time assertion lives in the tests).
//
// killswitch owns every halt durable write via its own Atomically transaction —
// there is no TripTx seam (ADR-0012 point 2). A count-first increment and its
// threshold TripHalt commit in the same transaction (ADR-0012 point 3).
type Store interface {
	// Atomically runs fn in one write transaction. killswitch uses it to commit
	// a counter increment and a threshold TripHalt as one durable event.
	Atomically(ctx context.Context, fn func(tx store.Tx) error) error

	// MarkHaltPending and TripHalt are the two legs of the durable trip. Run as
	// separate commits, an interrupted trip reloads as pending (persistence-wins,
	// ADR-0012 Decision 1(c)).
	MarkHaltPending(ctx context.Context, reason string) error
	TripHalt(ctx context.Context, reason string) error
	// ClearHalt resets the durable global halt (manual reset, ADR-0004 point 6).
	ClearHalt(ctx context.Context) error
	// Halt loads the persisted global halt for boot (persistence-wins).
	Halt(ctx context.Context) (store.HaltState, error)

	// SetCounter/Counter back the escalation counters. ReportOrderSuccess resets
	// via SetCounter; the count-first paths read+increment inside Atomically.
	SetCounter(ctx context.Context, c store.Counter) error
	Counter(ctx context.Context, name string) (store.Counter, error)
}

// Notifier is the halt-notification seam (ADR-0004 point 8). killswitch calls
// HaltTripped when a trip newly establishes a global block, inside a recover
// boundary so a panicking or misbehaving notifier can never break the guard.
//
// Contract: HaltTripped MUST be non-blocking — it is invoked while killswitch
// holds its transition lock, so an implementation should enqueue and return,
// never do slow I/O inline, and never call back into a mutating killswitch
// method (Trip/ClearHalt/Report*), which would deadlock. Reading CanSubmit is
// safe. The concrete channel (Slack/email/...) is out of scope here.
type Notifier interface {
	HaltTripped(reason string)
}

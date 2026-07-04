package audit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind classifies an audit record. The set is fixed by ADR-0006's per-class
// idempotency-key synthesis and must not drift silently.
type Kind string

const (
	// KindOrderLifecycle is an order-intent lifecycle transition (prepared,
	// submit-attempted, acked, terminal status). Its idempotency key reuses the
	// ADR-0002 identities: orderID once acquired, intentID before.
	KindOrderLifecycle Kind = "order_lifecycle"
	// KindFill is an observed cumulative execution snapshot. Toss exposes no
	// per-fill id (measured), so the key is versioned by a digest of the
	// financial fields (ADR-0006).
	KindFill Kind = "fill"
	// KindError is a reconstruction-resistant error occurrence. Its emit is
	// synchronous-durable and its key carries a durable append sequence so
	// distinct occurrences never collapse (ADR-0006).
	KindError Kind = "error"
)

// FillSnapshot is the cumulative execution snapshot observed from
// GET /api/v1/orders/{orderId} (Toss has no per-fill event stream — measured).
// Every field is carried as the raw API string so the financial digest is exact:
// re-encoding decimals as floats would make the digest lossy and defeat the
// "same quantity, corrected fee" detection ADR-0006 requires. audit is a leaf
// and never imports the toss client, so the caller passes these strings through.
type FillSnapshot struct {
	FilledQuantity     string
	AverageFilledPrice string
	FilledAmount       string
	Commission         string
	Tax                string
	SettlementDate     string
}

// OrderLifecycleEvent is one order-intent lifecycle transition to audit. OrderID
// is empty before it is acquired (ADR-0002); Marker is the marker/status token.
type OrderLifecycleEvent struct {
	IntentID   string
	OrderID    string
	Marker     string
	OccurredAt time.Time
	Detail     string
}

// FillEvent is one observed cumulative execution snapshot for an order.
type FillEvent struct {
	OrderID    string
	Snapshot   FillSnapshot
	OccurredAt time.Time
}

// ErrorEvent is one reconstruction-resistant error occurrence. Scope resolves to
// intentID, else orderID, else "global" (ADR-0006). Operation and ErrorClass
// give the key its structure; Message is free-form context.
type ErrorEvent struct {
	IntentID   string
	OrderID    string
	Operation  string
	ErrorClass string
	Message    string
	OccurredAt time.Time
}

// Ack is the durable acknowledgement returned once the segment-durability
// protocol has completed for a record. A returned Ack with a nil error IS the
// durable-ack signal ADR-0006 point 4 defines — content fsync plus, for a new or
// rotated segment, atomic rename and parent-directory fsync. IdempotencyKey is
// the record's merge handle; Sequence is its global durable append position
// (monotonic across rotation); Segment is the file it landed in.
type Ack struct {
	IdempotencyKey string
	Sequence       int64
	Segment        string
}

// Sink is the consumer-facing seam. order/reconciler/killswitch/token-manager
// depend on this interface and fake it in their own unit tests; the sink's own
// fsync/torn-tail/rotation/recovery behaviour is tested with a real writer in a
// temp dir (ADR-0006 point 2). Every Emit is synchronous-durable: it returns a
// nil error only after the record is fully committed to a durable segment.
type Sink interface {
	EmitOrderLifecycle(ctx context.Context, ev OrderLifecycleEvent) (Ack, error)
	EmitFill(ctx context.Context, ev FillEvent) (Ack, error)
	EmitError(ctx context.Context, ev ErrorEvent) (Ack, error)
	Close() error
}

// ErrClosed is returned when emitting to a closed sink.
var ErrClosed = errors.New("audit: sink closed")

// FailClosedError wraps any failure to make an audit record durable (a failed
// write, fsync, rename, or directory fsync — including disk-full). Its presence
// is the fail-closed SIGNAL of ADR-0006 point 6: the record is NOT durable, so
// the unattended bot must treat continued operation as unsafe and, once the
// killswitch exists, halt new orders. This issue only EXPOSES the signal — no
// killswitch trigger is wired here (explicitly out of scope). Callers detect it
// with IsFailClosed.
type FailClosedError struct {
	Op  string
	Err error
}

func (e *FailClosedError) Error() string {
	return fmt.Sprintf("audit: fail-closed at %s: %v", e.Op, e.Err)
}

func (e *FailClosedError) Unwrap() error { return e.Err }

// IsFailClosed reports whether err signals a non-durable audit write. A future
// killswitch matches on this to trip; this issue does not wire that trigger.
func IsFailClosed(err error) bool {
	var f *FailClosedError
	return errors.As(err, &f)
}

// failClosed wraps err as a fail-closed signal unless err is already one.
func failClosed(op string, err error) error {
	var f *FailClosedError
	if errors.As(err, &f) {
		return err
	}
	return &FailClosedError{Op: op, Err: err}
}

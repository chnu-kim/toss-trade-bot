package store

import (
	"errors"
	"time"
)

// ErrIntentNotFound is returned when a write targets an intent that was never
// appended. Surfacing it (rather than silently succeeding) helps callers catch
// journal-ordering bugs.
var ErrIntentNotFound = errors.New("store: intent not found")

// MarkerKind is a write-ahead journal transition. The set is fixed by ADR-0002
// and must not drift: prepared → submit-attempted → acked. store treats the
// value as opaque persistence; the domain owns its meaning.
type MarkerKind string

const (
	// MarkerPrepared is written when the intent payload is durably appended,
	// before POST is attempted. AppendIntent records this first marker.
	MarkerPrepared MarkerKind = "prepared"
	// MarkerSubmitAttempted is written immediately before calling POST /orders.
	// Its presence means "POST may have happened"; its absence means "POST
	// certainly did not happen" (ADR-0002).
	MarkerSubmitAttempted MarkerKind = "submit-attempted"
	// MarkerAcked is written when POST returns an orderId, which becomes the
	// handle to the truth via GET /orders/{orderId} (ADR-0002/0003).
	MarkerAcked MarkerKind = "acked"
)

// Intent is the persisted write-ahead journal record for one order intent — a
// DTO, not the domain's canonical intent object (ADR-0005 point 2). Payload is
// the opaque order request bytes; store never interprets them. ResolvedAt is
// nil while the intent is still live (LoadUnresolvedIntents returns exactly
// these); the domain sets it via ResolveIntent when it terminally closes the
// intent. Markers is populated on load so the reconciler can branch on the
// 2-marker state (ADR-0003).
type Intent struct {
	IntentID      string
	ClientOrderID string
	Payload       []byte
	CreatedAt     time.Time
	ResolvedAt    *time.Time
	Resolution    string
	Markers       []Marker
}

// Marker is one persisted journal transition for an intent. Seq is the durable
// append order (monotonic). OrderID is set only on the acked marker.
type Marker struct {
	Seq      int64
	IntentID string
	Kind     MarkerKind
	OrderID  string
	At       time.Time
}

// HaltState is the persisted global halt (ADR-0004). It survives restarts so a
// restart cannot become a safety-guard bypass. store persists and reads it;
// killswitch owns the decision to trip and the manual-reset policy. TrippedAt
// is zero when not halted.
type HaltState struct {
	Halted    bool
	Reason    string
	TrippedAt time.Time
}

// Counter is a reconstruction-resistant persistent counter (ADR-0004 point 7),
// e.g. token-refresh failures a restart must not reset to zero. WindowStart is
// optional (zero when unused) for time-windowed thresholds.
type Counter struct {
	Name        string
	Value       int64
	WindowStart time.Time
	UpdatedAt   time.Time
}

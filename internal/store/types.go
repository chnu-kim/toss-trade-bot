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
	// FullyAuditedAt is the prune-gate flag (ADR-0006 point 4): non-nil once every
	// lifecycle audit record for this intent has been durably acked, nil otherwise.
	// It is nil for any unresolved intent (the terminal record cannot yet exist),
	// so LoadUnresolvedIntents always leaves it nil; loadIntentByID surfaces it for
	// resolved intents. #14 reads it to gate prune; this issue sets it.
	FullyAuditedAt *time.Time
}

// LifecycleRecord is one order-lifecycle audit record reconstructed
// deterministically from journal state (markers + resolution) — a store-local DTO,
// NOT an audit event. store is a leaf and never imports audit (ADR-0005 point 2,
// ADR-0006 point 2): a consumer (the future reconciler) maps a LifecycleRecord to
// an audit.OrderLifecycleEvent, re-emits it, and on the durable ack records it back
// via RecordAuditAck(IntentID, Key). Fields:
//
//   - Key is the opaque store-local ack identity; round-trip it unchanged. It is
//     NOT the audit idempotency key (that is synthesized inside audit).
//   - Marker is the marker kind ("prepared"/"submit-attempted"/"acked") for a
//     transition record, or the terminal resolution string for the terminal record.
//   - OrderID is empty before it is acquired and the acquired orderId at/after the
//     acked marker (ADR-0002/0006 key reuse), so the consumer synthesizes the right
//     audit key.
//   - OccurredAt is the journal timestamp of the transition (or resolution).
//   - Terminal marks the final execution-snapshot record (exists only once resolved).
type LifecycleRecord struct {
	IntentID   string
	Key        string
	Marker     string
	OrderID    string
	OccurredAt time.Time
	Terminal   bool
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

// HaltPhase is the durable lifecycle of the global halt (ADR-0012 Decision 1(c)).
// It is a 2-phase trip so an unclean recovery can distinguish "a trip was durably
// initiated but not completed" (pending) from "no trip" (none) — store exposes
// the raw phase and the consumer (#32 killswitch / #36 cmd/bot) owns the judgment
// of whether pending should be treated as halted. store never interprets it.
type HaltPhase string

const (
	// HaltNone is the untripped state: no global halt is in effect.
	HaltNone HaltPhase = "none"
	// HaltPending means a trip has been durably initiated but TripHalt has not
	// yet completed. Because store is writable while pending is durable, an
	// unclean recovery reads pending and can treat it as halted (persistence-wins,
	// ADR-0012 Decision 1(c)) — closing the window where the reconciler resolves
	// away the reconstruction evidence before the halt is finalized.
	HaltPending HaltPhase = "pending"
	// HaltHalted is the completed global halt. It survives restarts and is cleared
	// only by an explicit human reset (ADR-0004 point 6).
	HaltHalted HaltPhase = "halted"
)

// HaltState is the persisted global halt (ADR-0004, ADR-0012). It survives
// restarts so a restart cannot become a safety-guard bypass. store persists and
// reads it; killswitch owns the decision to trip, the pending→halted judgment,
// and the manual-reset policy. TrippedAt records when the trip was first
// initiated (preserved across pending→halted) and is zero when Phase is none.
type HaltState struct {
	Phase     HaltPhase
	Reason    string
	TrippedAt time.Time
}

// LifecycleState is the clean-shutdown sentinel (ADR-0012 Decision 1(c)). It is a
// single durable lifecycle value, never two coexisting records, so a crash can
// never leave a stale clean beside a running (sentinel fail-open #1). store
// exposes an atomic set/get seam only; cmd/bot (#36) owns the eligibility rules
// ("when may a clean be written", "unclean ⇒ conservative halted boot").
type LifecycleState string

const (
	// LifecycleRunning is the conservative, not-known-clean value: set atomically
	// at boot before submissions open, and the default for a fresh or migrated DB
	// that has recorded no clean shutdown. An unclean exit leaves it running.
	LifecycleRunning LifecycleState = "running"
	// LifecycleClean is written only by a graceful shutdown that has no unresolved
	// non-durable halt (the eligibility rule lives in the consumer, not store).
	LifecycleClean LifecycleState = "clean"
)

// Counter is a reconstruction-resistant persistent counter (ADR-0004 point 7),
// e.g. token-refresh failures a restart must not reset to zero. WindowStart is
// optional (zero when unused) for time-windowed thresholds.
type Counter struct {
	Name        string
	Value       int64
	WindowStart time.Time
	UpdatedAt   time.Time
}

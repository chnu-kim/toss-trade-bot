package store

import (
	"errors"

	sqlitedrv "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Marker integrity (issue #29, ADR-0002). The 2-marker write-ahead protocol's
// invariants are enforced by the SCHEMA (V4) rather than by the discipline of the
// layers above it: the markers table carries a UNIQUE index on (intent_id, kind)
// and CHECK constraints on the marker shape, and appendMarker adds the one rule
// SQL cannot express (no marker after an intent is terminally resolved).
//
// Why the schema and not the order layer: submit-attempted is appended BEFORE the
// irreversible POST, so a duplicate-submit bug or race is caught at the durability
// layer while it is still cheap — before money moves. A store-layer-only check
// would be a check-then-act that a second writer could slip past, and a future
// writer could simply forget it. The engine cannot forget.
//
// The violations below were all accepted with err=nil before this change (audit
// finding M-7), which is why each has a named sentinel: a caller must be able to
// tell a protocol violation from a durability-medium failure. Conflating the two
// is the fail-closed-wrong-direction hazard — an over-broad "the store failed"
// classification turns a duplicate replay into a global halt of the whole bot.

// ErrDuplicateMarker is returned when a marker of that kind already exists for the
// intent. Each ADR-0002 transition happens at most once per intent: a second
// submit-attempted is the durable record of a SECOND POST attempt, and a second
// acked would bind a second orderId to one intent, making the post-hoc truth
// handle (ADR-0002 point 3) ambiguous for the reconciler.
var ErrDuplicateMarker = errors.New("store: a marker of this kind already exists for the intent")

// ErrInvalidMarker is returned when a marker's shape violates the protocol: an
// unknown kind, an acked marker without an orderId (it would claim the POST was
// acknowledged while destroying the only evidence needed to verify it), or a
// non-acked marker carrying one (ReconstructLifecycleRecords silently drops that
// orderId, so it would be invisible corruption rather than a loud error).
var ErrInvalidMarker = errors.New("store: marker violates the 2-marker protocol shape")

// ErrMarkerAfterTerminal is returned when a marker targets an intent that is
// already terminally resolved. A terminal resolution closes the journal entry
// (ADR-0003); a later marker would reopen a settled intent and could resurrect it
// into the ambiguous set that gates submissions. This is a cross-row rule, so it
// lives in appendMarker's conditional insert rather than in a CHECK.
var ErrMarkerAfterTerminal = errors.New("store: cannot append a marker to a terminally resolved intent")

// ErrMigrationDataViolation is returned by Open when a migration cannot be applied
// because rows ALREADY on disk violate an invariant it introduces — e.g. upgrading
// a journal that already contains two submit-attempted markers for one intent.
//
// It fails closed rather than repairing: migrations are additive and never rewrite
// existing rows (ADR-0005), and deleting or coercing the offending markers would
// destroy exactly the evidence of the duplicate-submit incident that produced them.
// Refusing to boot over a journal that contradicts the protocol is also the safer
// of the two failure modes for an unattended bot — the alternative is trading on
// top of a journal whose duplicate-submit accounting is already known to be wrong.
// The operator inspects the markers table and decides.
var ErrMigrationDataViolation = errors.New("store: existing rows violate an invariant this migration enforces")

// constraintCode reports the SQLite extended result code of err when it is a
// constraint violation from the driver, and whether it was one.
func constraintCode(err error) (int, bool) {
	var se *sqlitedrv.Error
	if !errors.As(err, &se) {
		return 0, false
	}
	// The primary code is the low 8 bits of the extended code; SQLITE_CONSTRAINT
	// covers UNIQUE/CHECK/FOREIGN KEY/NOT NULL and friends.
	if se.Code()&0xff != sqlite3.SQLITE_CONSTRAINT {
		return 0, false
	}
	return se.Code(), true
}

// classifyMarkerErr maps a driver error from the markers INSERT onto this
// package's sentinels. It is scoped deliberately narrowly — only appendMarker
// calls it, and the markers table has exactly one unique index
// (ux_markers_intent_kind) and one set of shape CHECKs — so a code cannot be
// misattributed. Anything else is returned unchanged: a genuine durability-medium
// failure must NOT be laundered into a protocol-violation sentinel, because the
// caller escalates the two differently (ADR-0005 point 6).
func classifyMarkerErr(err error) error {
	code, ok := constraintCode(err)
	if !ok {
		return err
	}
	switch code {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return ErrDuplicateMarker
	case sqlite3.SQLITE_CONSTRAINT_CHECK:
		return ErrInvalidMarker
	default:
		return err
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrHaltRowMissing is returned by the halt writers (MarkHaltPending, TripHalt,
// ClearHalt) when the halt singleton row (id = 1) is absent, so their UPDATE
// matches zero rows. Without this check the UPDATE returns no error and the caller
// gets a false durable-ack: killswitch believes a global halt is persisted when
// nothing was written, and a restart boots un-halted — the exact silent fail-open
// ADR-0004 forbids (and the PR#22 "false durable-ack" class). The row can only
// vanish via a botched migration rebuild, a backup restore, or manual tampering,
// so surfacing it (fail-closed) beats silently recreating it and masking the
// anomaly.
var ErrHaltRowMissing = errors.New("store: halt singleton row (id=1) missing")

// ErrResolutionConflict is returned by ResolveIntent when an already-terminally-
// resolved intent is re-resolved with a DIFFERENT resolution than the one durably
// recorded. Re-resolving with the SAME resolution is genuinely idempotent (nil);
// a conflicting one is a strong signal of a journal-consistency bug (e.g. the
// killswitch closed the intent aborted-before-submit while the reconciler tries to
// close it filled). store surfaces it as a sentinel so the consumer (#35
// reconciler) can log/alert on the contradiction instead of silently dropping the
// second verdict — the journal is the audit and restart-recovery ground truth, so
// a swallowed conflict makes a real inconsistency undetectable. The first-recorded
// resolution is never overwritten.
var ErrResolutionConflict = errors.New("store: intent already resolved with a different resolution")

// Tx is the atomic-write seam handed to an Atomically callback. Its methods run
// inside the enclosing transaction, so a logical event that must change several
// areas together (journal + halt/counter) either fully commits or fully rolls
// back. It exposes reads as well as writes because such events are often
// read-then-write (read a counter, increment, trip halt if over threshold).
type Tx interface {
	AppendIntent(ctx context.Context, in Intent) error
	AppendMarker(ctx context.Context, intentID string, kind MarkerKind, orderID string) error
	ResolveIntent(ctx context.Context, intentID, resolution string) error
	LoadUnresolvedIntents(ctx context.Context) ([]Intent, error)

	// MarkHaltPending transitions the global halt to pending (trip durably
	// initiated but not completed). TripHalt completes it (→ halted); ClearHalt
	// resets it (→ none). Halt loads the current phase. killswitch owns which
	// transition to run and when (ADR-0012 Decision 1(c)/2).
	MarkHaltPending(ctx context.Context, reason string) error
	TripHalt(ctx context.Context, reason string) error
	ClearHalt(ctx context.Context) error
	Halt(ctx context.Context) (HaltState, error)

	// SetLifecycle atomically sets the single-row clean-shutdown sentinel,
	// overwriting the previous value in the same write. Lifecycle loads it. store
	// is passive: it persists exactly what it is told and never auto-flips.
	SetLifecycle(ctx context.Context, s LifecycleState) error
	Lifecycle(ctx context.Context) (LifecycleState, error)

	SetCounter(ctx context.Context, c Counter) error
	Counter(ctx context.Context, name string) (Counter, error)

	// RecordAuditAck idempotently records that the lifecycle audit record
	// identified by recordKey (a store-local key from ReconstructLifecycleRecords)
	// is durably acked for intentID. It records the ack fact only
	// (boolean/timestamp, no audit content — ADR-0005 point 5) and does NOT set the
	// prune-gate flag. Call FinalizeFullyAudited after recording to gate the flag on
	// ALL lifecycle records (ADR-0006 point 4 — the terminal alone must not set it).
	RecordAuditAck(ctx context.Context, intentID, recordKey string) error
	// FinalizeFullyAudited sets the intent's fully-audited flag iff it is resolved
	// AND every lifecycle record reconstructed from its journal state has a recorded
	// ack; it returns whether the intent is (now or already) fully audited.
	// Idempotent and preservation-safe: until all records are acked the flag stays
	// unset, so prune (#14) preserves the intent.
	FinalizeFullyAudited(ctx context.Context, intentID string) (bool, error)
	// FullyAudited reads the prune-gate flag: the time it was set and whether it is
	// set.
	FullyAudited(ctx context.Context, intentID string) (time.Time, bool, error)
	// UnackedLifecycleRecords returns the lifecycle records reconstructed from
	// intentID's journal state that do NOT yet have a recorded ack — the set a
	// restart reconciler must re-emit (ADR-0006 point 4 recovery loop; the driver is
	// out of scope). Deterministic.
	UnackedLifecycleRecords(ctx context.Context, intentID string) ([]LifecycleRecord, error)
	// LoadNotFullyAuditedIntents returns every intent whose fully-audited flag is
	// unset (resolved-but-unfinalized crash orphans AND still-open intents), each
	// with its markers — the discoverable recovery-candidate set for the restart
	// loop, since resolved orphans leave LoadUnresolvedIntents. Discovery primitive
	// only; the re-emit driver is out of scope (ADR-0003).
	LoadNotFullyAuditedIntents(ctx context.Context) ([]Intent, error)
}

// querier is the read/write surface shared by *sql.Tx and *sql.DB, letting the
// query functions below back both the transactional Tx methods and the DB-level
// read wrappers.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// txn is the concrete Tx bound to one *sql.Tx.
type txn struct {
	q querier
}

var _ Tx = (*txn)(nil)

func (t *txn) AppendIntent(ctx context.Context, in Intent) error {
	return appendIntent(ctx, t.q, in)
}
func (t *txn) AppendMarker(ctx context.Context, intentID string, kind MarkerKind, orderID string) error {
	return appendMarker(ctx, t.q, intentID, kind, orderID)
}
func (t *txn) ResolveIntent(ctx context.Context, intentID, resolution string) error {
	return resolveIntent(ctx, t.q, intentID, resolution)
}
func (t *txn) LoadUnresolvedIntents(ctx context.Context) ([]Intent, error) {
	return loadUnresolvedIntents(ctx, t.q)
}
func (t *txn) MarkHaltPending(ctx context.Context, reason string) error {
	return markHaltPending(ctx, t.q, reason)
}
func (t *txn) TripHalt(ctx context.Context, reason string) error {
	return tripHalt(ctx, t.q, reason)
}
func (t *txn) ClearHalt(ctx context.Context) error         { return clearHalt(ctx, t.q) }
func (t *txn) Halt(ctx context.Context) (HaltState, error) { return readHalt(ctx, t.q) }
func (t *txn) SetLifecycle(ctx context.Context, s LifecycleState) error {
	return setLifecycle(ctx, t.q, s)
}
func (t *txn) Lifecycle(ctx context.Context) (LifecycleState, error) {
	return readLifecycle(ctx, t.q)
}
func (t *txn) SetCounter(ctx context.Context, c Counter) error {
	return setCounter(ctx, t.q, c)
}
func (t *txn) Counter(ctx context.Context, name string) (Counter, error) {
	return readCounter(ctx, t.q, name)
}
func (t *txn) RecordAuditAck(ctx context.Context, intentID, recordKey string) error {
	return recordAuditAck(ctx, t.q, intentID, recordKey)
}
func (t *txn) FinalizeFullyAudited(ctx context.Context, intentID string) (bool, error) {
	return finalizeFullyAudited(ctx, t.q, intentID)
}
func (t *txn) FullyAudited(ctx context.Context, intentID string) (time.Time, bool, error) {
	return readFullyAudited(ctx, t.q, intentID)
}
func (t *txn) UnackedLifecycleRecords(ctx context.Context, intentID string) ([]LifecycleRecord, error) {
	return unackedLifecycleRecords(ctx, t.q, intentID)
}
func (t *txn) LoadNotFullyAuditedIntents(ctx context.Context) ([]Intent, error) {
	return loadNotFullyAuditedIntents(ctx, t.q)
}

// --- shared query functions (work over any querier) ---

func appendIntent(ctx context.Context, q querier, in Intent) error {
	created := in.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO intents (intent_id, client_order_id, payload, created_at) VALUES (?, ?, ?, ?)`,
		in.IntentID, in.ClientOrderID, in.Payload, created.UnixNano(),
	); err != nil {
		return fmt.Errorf("store: append intent %q: %w", in.IntentID, err)
	}
	// Creating the intent IS the prepared event (ADR-0002): record it as the
	// first marker so the reconciler sees the 2-marker progression from the
	// start.
	return appendMarker(ctx, q, in.IntentID, MarkerPrepared, "")
}

func appendMarker(ctx context.Context, q querier, intentID string, kind MarkerKind, orderID string) error {
	if _, err := q.ExecContext(ctx,
		`INSERT INTO markers (intent_id, kind, order_id, at) VALUES (?, ?, ?, ?)`,
		intentID, string(kind), orderID, time.Now().UnixNano(),
	); err != nil {
		return fmt.Errorf("store: append marker %s for %q: %w", kind, intentID, err)
	}
	return nil
}

func resolveIntent(ctx context.Context, q querier, intentID, resolution string) error {
	res, err := q.ExecContext(ctx,
		`UPDATE intents SET resolved_at = ?, resolution = ? WHERE intent_id = ? AND resolved_at IS NULL`,
		time.Now().UnixNano(), resolution, intentID,
	)
	if err != nil {
		return fmt.Errorf("store: resolve intent %q: %w", intentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: resolve intent %q: %w", intentID, err)
	}
	if n == 0 {
		// Zero rows updated: the intent is either absent or already terminally
		// resolved (an existing UNRESOLVED intent would have matched the
		// resolved_at IS NULL clause, so n would be 1). Read the stored resolution
		// to distinguish the three cases: absent (ErrIntentNotFound), already
		// resolved with the SAME verdict (genuine idempotency → nil), or already
		// resolved with a DIFFERENT verdict (journal-consistency conflict →
		// ErrResolutionConflict). Both the UPDATE and this SELECT run on the same
		// connection/transaction, so no concurrent writer can interleave between
		// them.
		var stored string
		if err := q.QueryRowContext(ctx, `SELECT resolution FROM intents WHERE intent_id = ?`, intentID).Scan(&stored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %q", ErrIntentNotFound, intentID)
			}
			return fmt.Errorf("store: resolve intent %q: %w", intentID, err)
		}
		if stored != resolution {
			return fmt.Errorf("%w: %q resolved as %q, cannot re-resolve as %q", ErrResolutionConflict, intentID, stored, resolution)
		}
	}
	return nil
}

func loadUnresolvedIntents(ctx context.Context, q querier) ([]Intent, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT intent_id, client_order_id, payload, created_at, resolved_at, resolution
		 FROM intents WHERE resolved_at IS NULL ORDER BY created_at, intent_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: load unresolved intents: %w", err)
	}
	// Fully drain and close before issuing the per-intent marker queries: on a
	// single-connection transaction two live result sets cannot coexist.
	var out []Intent
	for rows.Next() {
		var (
			in         Intent
			createdAt  int64
			resolvedAt sql.NullInt64
		)
		if err := rows.Scan(&in.IntentID, &in.ClientOrderID, &in.Payload, &createdAt, &resolvedAt, &in.Resolution); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan intent: %w", err)
		}
		in.CreatedAt = time.Unix(0, createdAt)
		if resolvedAt.Valid {
			t := time.Unix(0, resolvedAt.Int64)
			in.ResolvedAt = &t
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate intents: %w", err)
	}
	rows.Close()

	for i := range out {
		markers, err := loadMarkers(ctx, q, out[i].IntentID)
		if err != nil {
			return nil, err
		}
		out[i].Markers = markers
	}
	return out, nil
}

func loadMarkers(ctx context.Context, q querier, intentID string) ([]Marker, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT seq, kind, order_id, at FROM markers WHERE intent_id = ? ORDER BY seq`,
		intentID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: load markers for %q: %w", intentID, err)
	}
	defer rows.Close()

	var out []Marker
	for rows.Next() {
		m := Marker{IntentID: intentID}
		var kind string
		var at int64
		if err := rows.Scan(&m.Seq, &kind, &m.OrderID, &at); err != nil {
			return nil, fmt.Errorf("store: scan marker: %w", err)
		}
		m.Kind = MarkerKind(kind)
		m.At = time.Unix(0, at)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate markers: %w", err)
	}
	return out, nil
}

// markHaltPending moves the global halt to the pending phase (ADR-0012 Decision
// 1(c)). tripped_at is set on the first transition out of none and preserved
// afterwards (COALESCE) so it records when the trip was initiated, not when it
// completed. A clear resets tripped_at to NULL, so the next trip stamps a fresh
// time.
func markHaltPending(ctx context.Context, q querier, reason string) error {
	return execHaltSingleton(ctx, q, "mark halt pending",
		`UPDATE halt SET state = 'pending', reason = ?, tripped_at = COALESCE(tripped_at, ?) WHERE id = 1`,
		reason, time.Now().UnixNano(),
	)
}

// tripHalt completes the trip (→ halted). It works both directly from none (the
// count-first single-tx path, ADR-0012 Decision 3) and from pending, preserving
// the trip-initiation tripped_at across pending→halted (COALESCE).
func tripHalt(ctx context.Context, q querier, reason string) error {
	return execHaltSingleton(ctx, q, "trip halt",
		`UPDATE halt SET state = 'halted', reason = ?, tripped_at = COALESCE(tripped_at, ?) WHERE id = 1`,
		reason, time.Now().UnixNano(),
	)
}

func clearHalt(ctx context.Context, q querier) error {
	return execHaltSingleton(ctx, q, "clear halt",
		`UPDATE halt SET state = 'none', reason = '', tripped_at = NULL WHERE id = 1`,
	)
}

// execHaltSingleton runs a halt-table UPDATE and enforces that it matched the
// singleton row exactly once. SQLite counts every WHERE-matched row for an UPDATE
// (a zero-change re-trip still reports 1), so RowsAffected != 1 means the id=1 row
// is absent and the write would otherwise be a false durable-ack (ErrHaltRowMissing,
// L-1). This closes the halt-write side of the fail-open the same way readHalt
// already fails closed on a missing row on the read side.
func execHaltSingleton(ctx context.Context, q querier, what, query string, args ...any) error {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	if n != 1 {
		return fmt.Errorf("store: %s: %w (rows affected = %d)", what, ErrHaltRowMissing, n)
	}
	return nil
}

func readHalt(ctx context.Context, q querier) (HaltState, error) {
	var (
		state     string
		reason    string
		trippedAt sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT state, reason, tripped_at FROM halt WHERE id = 1`).
		Scan(&state, &reason, &trippedAt)
	if err != nil {
		return HaltState{}, fmt.Errorf("store: read halt: %w", err)
	}
	hs := HaltState{Phase: HaltPhase(state), Reason: reason}
	if trippedAt.Valid {
		hs.TrippedAt = time.Unix(0, trippedAt.Int64)
	}
	return hs, nil
}

// setLifecycle overwrites the single-row clean-shutdown sentinel in one write, so
// the previous value cannot coexist with the new one (ADR-0012 Decision 1(c)).
// An unknown value is rejected by the table CHECK, which surfaces as an error —
// store returns truth or error, never a silently-coerced state.
func setLifecycle(ctx context.Context, q querier, s LifecycleState) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE lifecycle SET state = ? WHERE id = 1`, string(s),
	); err != nil {
		return fmt.Errorf("store: set lifecycle %q: %w", s, err)
	}
	return nil
}

func readLifecycle(ctx context.Context, q querier) (LifecycleState, error) {
	var state string
	if err := q.QueryRowContext(ctx, `SELECT state FROM lifecycle WHERE id = 1`).Scan(&state); err != nil {
		return "", fmt.Errorf("store: read lifecycle: %w", err)
	}
	return LifecycleState(state), nil
}

func setCounter(ctx context.Context, q querier, c Counter) error {
	var windowStart sql.NullInt64
	if !c.WindowStart.IsZero() {
		windowStart = sql.NullInt64{Int64: c.WindowStart.UnixNano(), Valid: true}
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO counters (name, value, window_start, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET value = excluded.value, window_start = excluded.window_start, updated_at = excluded.updated_at`,
		c.Name, c.Value, windowStart, time.Now().UnixNano(),
	); err != nil {
		return fmt.Errorf("store: set counter %q: %w", c.Name, err)
	}
	return nil
}

func readCounter(ctx context.Context, q querier, name string) (Counter, error) {
	var (
		value       int64
		windowStart sql.NullInt64
		updatedAt   sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT value, window_start, updated_at FROM counters WHERE name = ?`, name).
		Scan(&value, &windowStart, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// A never-written counter reads as zero — callers threshold against 0.
		return Counter{Name: name}, nil
	}
	if err != nil {
		return Counter{}, fmt.Errorf("store: read counter %q: %w", name, err)
	}
	c := Counter{Name: name, Value: value}
	if windowStart.Valid {
		c.WindowStart = time.Unix(0, windowStart.Int64)
	}
	if updatedAt.Valid {
		c.UpdatedAt = time.Unix(0, updatedAt.Int64)
	}
	return c, nil
}

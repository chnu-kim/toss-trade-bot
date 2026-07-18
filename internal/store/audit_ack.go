package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Audit-ack ↔ prune-gate wiring (issue #20, ADR-0006 point 4). This file is the
// store side of "each lifecycle audit record durably acked → record the ack →
// once ALL are acked, set the per-intent fully-audited flag that gates prune".
// The audit *content* stays in the sink (ADR-0005 point 5); store keeps only the
// ack facts (boolean/timestamp) and the coverage logic that forbids gating on the
// terminal record alone.

// terminalAckKey is the store-local ack key for an intent's terminal lifecycle
// record. Marker records key on their durable append seq (markerAckKey); the
// terminal has no marker seq, so it uses this fixed sentinel, which cannot collide
// with any "m:"-prefixed seq key.
const terminalAckKey = "terminal"

// markerAckKey derives a marker's store-local ack key from its durable append seq
// (stable across restarts, unique even if two markers share a kind).
func markerAckKey(seq int64) string {
	return "m:" + strconv.FormatInt(seq, 10)
}

// ReconstructLifecycleRecords deterministically derives an intent's full set of
// order-lifecycle audit records from its journal state — one per marker plus, once
// the intent is resolved, the terminal execution-snapshot record (ADR-0006 point
// 4). It is a pure function of the passed Intent (markers + resolution), so it is
// identical across restarts and re-emits: the recovery loop (ADR-0006 point 4 /
// ADR-0003 reconciler — driver out of scope for this issue) rebuilds exactly the
// records that must be durably acked before the intent is prune-eligible.
//
// OrderID follows ADR-0002/0006 key reuse: empty for prepared/submit-attempted
// (keyed on intentId), the acquired orderId at the acked marker and on the terminal
// record (keyed on orderId once acquired; empty for a prepared-only aborted intent
// that never acquired one).
func ReconstructLifecycleRecords(in Intent) []LifecycleRecord {
	var acquiredOrderID string
	for _, m := range in.Markers {
		if m.Kind == MarkerAcked && m.OrderID != "" {
			acquiredOrderID = m.OrderID
		}
	}

	records := make([]LifecycleRecord, 0, len(in.Markers)+1)
	for _, m := range in.Markers {
		orderID := ""
		if m.Kind == MarkerAcked {
			orderID = m.OrderID
		}
		records = append(records, LifecycleRecord{
			IntentID:   in.IntentID,
			Key:        markerAckKey(m.Seq),
			Marker:     string(m.Kind),
			OrderID:    orderID,
			OccurredAt: m.At,
		})
	}
	if in.ResolvedAt != nil {
		records = append(records, LifecycleRecord{
			IntentID:   in.IntentID,
			Key:        terminalAckKey,
			Marker:     in.Resolution,
			OrderID:    acquiredOrderID,
			OccurredAt: *in.ResolvedAt,
			Terminal:   true,
		})
	}
	return records
}

// recordAuditAck idempotently records that the lifecycle record identified by
// recordKey is durably acked for intentID. It first checks the intent exists so a
// wiring bug (ack for a never-appended intent) surfaces as ErrIntentNotFound rather
// than a raw FK error, then inserts the ack fact (record_key + time only — no audit
// content). Re-recording the same key is a no-op (at-least-once re-emit, ADR-0006
// point 3).
func recordAuditAck(ctx context.Context, q querier, intentID, recordKey string) error {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM intents WHERE intent_id = ?`, intentID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", ErrIntentNotFound, intentID)
	}
	if err != nil {
		return fmt.Errorf("store: record audit ack %q/%q: %w", intentID, recordKey, err)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO audit_acks (intent_id, record_key, acked_at) VALUES (?, ?, ?)
		 ON CONFLICT(intent_id, record_key) DO NOTHING`,
		intentID, recordKey, time.Now().UnixNano(),
	); err != nil {
		return fmt.Errorf("store: record audit ack %q/%q: %w", intentID, recordKey, err)
	}
	return nil
}

// finalizeFullyAudited sets the intent's fully-audited flag iff the intent is
// resolved AND every lifecycle record reconstructed from its journal state has a
// recorded ack. It returns whether the intent is (now or already) fully audited.
//
// Requiring resolution is load-bearing: an unresolved intent's terminal record does
// not exist, so it must never be flagged (prune preserves it). Requiring coverage
// of ALL records — not just the terminal — is the ADR-0006 point 4 invariant: a
// lost intermediate marker audit keeps the flag unset, so prune cannot delete the
// journal that is that marker's only durable outbox. Setting the flag is idempotent
// (the timestamp does not move on re-finalize).
func finalizeFullyAudited(ctx context.Context, q querier, intentID string) (bool, error) {
	in, err := loadIntentByID(ctx, q, intentID)
	if err != nil {
		return false, err
	}
	if in.ResolvedAt == nil {
		// No terminal record yet: cannot be fully audited. Fail-safe: flag unset.
		return false, nil
	}
	if in.FullyAuditedAt != nil {
		return true, nil // already flagged; idempotent.
	}

	acked, err := loadAckedKeys(ctx, q, intentID)
	if err != nil {
		return false, err
	}
	for _, rec := range ReconstructLifecycleRecords(in) {
		if _, ok := acked[rec.Key]; !ok {
			return false, nil // some lifecycle record still un-acked.
		}
	}

	if _, err := q.ExecContext(ctx,
		`UPDATE intents SET fully_audited_at = ? WHERE intent_id = ? AND fully_audited_at IS NULL`,
		time.Now().UnixNano(), intentID,
	); err != nil {
		return false, fmt.Errorf("store: set fully-audited %q: %w", intentID, err)
	}
	return true, nil
}

// readFullyAudited reads the prune-gate flag: the time it was set and whether it is
// set. A never-appended intent is ErrIntentNotFound (distinct from "appended but
// not yet fully audited", which is (zero, false, nil)).
func readFullyAudited(ctx context.Context, q querier, intentID string) (time.Time, bool, error) {
	var at sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT fully_audited_at FROM intents WHERE intent_id = ?`, intentID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, fmt.Errorf("%w: %q", ErrIntentNotFound, intentID)
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: read fully-audited %q: %w", intentID, err)
	}
	if !at.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(0, at.Int64), true, nil
}

// unackedLifecycleRecords returns the lifecycle records reconstructed from
// intentID's journal state that do NOT yet have a recorded ack — the deterministic
// set a restart reconciler must re-emit (ADR-0006 point 4 recovery loop; the driver
// itself is out of scope for this issue). The remainder preserves reconstruction
// order.
func unackedLifecycleRecords(ctx context.Context, q querier, intentID string) ([]LifecycleRecord, error) {
	in, err := loadIntentByID(ctx, q, intentID)
	if err != nil {
		return nil, err
	}
	acked, err := loadAckedKeys(ctx, q, intentID)
	if err != nil {
		return nil, err
	}
	all := ReconstructLifecycleRecords(in)
	out := make([]LifecycleRecord, 0, len(all))
	for _, rec := range all {
		if _, ok := acked[rec.Key]; !ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// loadAckedKeys loads the set of recorded ack keys for an intent.
func loadAckedKeys(ctx context.Context, q querier, intentID string) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT record_key FROM audit_acks WHERE intent_id = ?`, intentID)
	if err != nil {
		return nil, fmt.Errorf("store: load audit acks for %q: %w", intentID, err)
	}
	defer rows.Close()

	acked := make(map[string]struct{})
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("store: scan audit ack: %w", err)
		}
		acked[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit acks: %w", err)
	}
	return acked, nil
}

// loadIntentByID loads a single intent by id with its markers, regardless of
// resolution state (unlike loadUnresolvedIntents, which is the restart scan of
// still-open intents). It surfaces fully_audited_at so callers can branch on the
// prune-gate flag. A missing intent is ErrIntentNotFound.
func loadIntentByID(ctx context.Context, q querier, intentID string) (Intent, error) {
	var (
		in           Intent
		createdAt    int64
		resolvedAt   sql.NullInt64
		fullyAudited sql.NullInt64
	)
	err := q.QueryRowContext(ctx,
		`SELECT intent_id, client_order_id, payload, created_at, resolved_at, resolution, fully_audited_at
		 FROM intents WHERE intent_id = ?`, intentID,
	).Scan(&in.IntentID, &in.ClientOrderID, &in.Payload, &createdAt, &resolvedAt, &in.Resolution, &fullyAudited)
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, fmt.Errorf("%w: %q", ErrIntentNotFound, intentID)
	}
	if err != nil {
		return Intent{}, fmt.Errorf("store: load intent %q: %w", intentID, err)
	}
	in.CreatedAt = time.Unix(0, createdAt)
	if resolvedAt.Valid {
		t := time.Unix(0, resolvedAt.Int64)
		in.ResolvedAt = &t
	}
	if fullyAudited.Valid {
		t := time.Unix(0, fullyAudited.Int64)
		in.FullyAuditedAt = &t
	}

	markers, err := loadMarkers(ctx, q, intentID)
	if err != nil {
		return Intent{}, err
	}
	in.Markers = markers
	return in, nil
}

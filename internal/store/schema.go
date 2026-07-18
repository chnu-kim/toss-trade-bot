package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSchemaTooNew is returned by Open when the database was already migrated to
// a version newer than this binary knows. Opening it anyway would let old code
// write with obsolete assumptions and silently violate newer invariants, so the
// store fails closed instead (a rollback below a migration must not corrupt).
var ErrSchemaTooNew = errors.New("store: database schema is newer than this binary supports")

// Migration versions this package owns. V1 establishes the live-state tables;
// V2 (issue #60) extends the global halt to a 3-state lifecycle and adds the
// clean-shutdown sentinel (ADR-0012); V3 (issue #20) adds the per-intent
// fully-audited ack flag/timestamp plus the lifecycle-audit ack-tracking table
// that together gate prune on "every lifecycle audit record durably acked"
// (ADR-0006 point 4). This issue SETS that flag; the retention/prune loop (#14)
// READS it to decide eligibility and does not touch the schema — so V3 and #14
// do not collide on this file.
//
// The version number is the shared contract that keeps concurrently-open store
// issues from colliding: whichever merges first claims the next number and the
// others rebase onto it (issue #60 "공유 접점"). This issue was originally
// planned as V2 but rebased onto V3 after #60 merged first and took V2.
const (
	schemaVersionV1 = 1
	schemaVersionV2 = 2
	schemaVersionV3 = 3
)

// migrations is the ordered migration list. Index i migrates the schema from
// version i to version i+1; migrations[0] establishes V1. Append-only: never
// edit a shipped migration, add a new one.
var migrations = []string{
	schemaV1,
	schemaV2,
	schemaV3,
}

// schemaV1 creates the three areas of live state: the order journal (intents +
// append-only markers), the singleton global halt row, and persistent counters.
const schemaV1 = `
CREATE TABLE intents (
	intent_id       TEXT PRIMARY KEY,
	client_order_id TEXT NOT NULL,
	payload         BLOB,
	created_at      INTEGER NOT NULL,
	resolved_at     INTEGER,
	resolution      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE markers (
	seq       INTEGER PRIMARY KEY AUTOINCREMENT,
	intent_id TEXT NOT NULL REFERENCES intents(intent_id),
	kind      TEXT NOT NULL,
	order_id  TEXT NOT NULL DEFAULT '',
	at        INTEGER NOT NULL
);
CREATE INDEX idx_markers_intent ON markers(intent_id);
CREATE INDEX idx_intents_unresolved ON intents(resolved_at) WHERE resolved_at IS NULL;
CREATE TABLE halt (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	halted     INTEGER NOT NULL DEFAULT 0,
	reason     TEXT NOT NULL DEFAULT '',
	tripped_at INTEGER
);
INSERT INTO halt (id, halted, reason, tripped_at) VALUES (1, 0, '', NULL);
CREATE TABLE counters (
	name         TEXT PRIMARY KEY,
	value        INTEGER NOT NULL DEFAULT 0,
	window_start INTEGER,
	updated_at   INTEGER NOT NULL
);
`

// schemaV2 backs the kill-switch durability substrate (ADR-0012, issue #60). It
// does two things:
//
//   - Extends the global halt from a 2-value boolean to the 3-state lifecycle
//     none→pending→halted→none. The halt table is recreated (rather than an
//     ADD/DROP COLUMN dance) so the swap is portable across SQL engines and does
//     not depend on the engine's DROP COLUMN support. The singleton row's reason
//     and tripped_at are carried over, and the old boolean maps halted!=0 →
//     'halted', else → 'none', so an existing tripped halt is never lost on
//     upgrade (a mid-upgrade restart must not become a safety-guard bypass).
//   - Adds the clean-shutdown sentinel as a single-row lifecycle state. The
//     CHECK(id = 1) plus a single state column make two coexisting values
//     structurally impossible (sentinel fail-open #1, ADR-0012 Decision 1(c)).
//     A fresh/migrated DB defaults to the conservative 'running' — it has
//     recorded no clean shutdown.
//
// No table references halt via a foreign key, so recreating it under
// foreign_keys=ON is safe (nothing points at the old table to re-target).
const schemaV2 = `
CREATE TABLE halt_v2 (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	state      TEXT NOT NULL DEFAULT 'none' CHECK (state IN ('none', 'pending', 'halted')),
	reason     TEXT NOT NULL DEFAULT '',
	tripped_at INTEGER
);
INSERT INTO halt_v2 (id, state, reason, tripped_at)
	SELECT id, CASE WHEN halted != 0 THEN 'halted' ELSE 'none' END, reason, tripped_at
	FROM halt;
DROP TABLE halt;
ALTER TABLE halt_v2 RENAME TO halt;
CREATE TABLE lifecycle (
	id    INTEGER PRIMARY KEY CHECK (id = 1),
	state TEXT NOT NULL DEFAULT 'running' CHECK (state IN ('clean', 'running'))
);
INSERT INTO lifecycle (id, state) VALUES (1, 'running');
`

// schemaV3 backs the audit-ack ↔ prune-gate wiring (issue #20, ADR-0006 point 4).
// It is additive — no shipped migration (schemaV1/schemaV2) is edited — and adds:
//
//   - intents.fully_audited_at — the per-intent prune-gate flag/timestamp. NULL
//     means "not yet fully audited", the fail-safe default: prune (#14) preserves
//     an intent whose flag is unset (ADR-0005 point 6, ADR-0006 point 4). A
//     non-NULL timestamp means every lifecycle audit record for the intent has
//     been durably acked. It is a boolean/timestamp only — no audit CONTENT lands
//     in store (ADR-0005 point 5); the audit history lives in the sink.
//   - audit_acks — one row per (intent, lifecycle record) that has been durably
//     acked. record_key is the store-local lifecycle-record identity produced by
//     ReconstructLifecycleRecords (a marker's durable append seq, or the terminal
//     sentinel) — NOT audit content and NOT the audit idempotency key. This table
//     is what lets fully_audited_at gate on ALL lifecycle records rather than the
//     terminal alone (ADR-0006 point 4): the flag is set only once every record
//     reconstructed from journal state has a matching row here. It holds
//     boolean/timestamp bookkeeping only (the ack fact + time), so it stays within
//     ADR-0005 point 5's "live actionable state, not history" rule — it is the
//     prune-gate's coverage ledger, not an audit log.
//
// The FK to intents keeps an ack from ever referencing an intent the journal does
// not have; an orphan ack would silently corrupt prune-gating.
const schemaV3 = `
ALTER TABLE intents ADD COLUMN fully_audited_at INTEGER;
CREATE TABLE audit_acks (
	intent_id  TEXT NOT NULL REFERENCES intents(intent_id),
	record_key TEXT NOT NULL,
	acked_at   INTEGER NOT NULL,
	PRIMARY KEY (intent_id, record_key)
);
`

// migrate brings the database up to the latest migration, running each pending
// step in its own transaction and advancing PRAGMA user_version transactionally.
// It is idempotent: reopening an up-to-date database is a no-op.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("%w: on-disk version %d, supported up to %d", ErrSchemaTooNew, current, len(migrations))
	}
	for v := current; v < len(migrations); v++ {
		if err := applyMigration(ctx, db, v, migrations[v]); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, index int, ddl string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", index+1, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("store: apply migration %d: %w", index+1, err)
	}
	// PRAGMA user_version cannot be parameterized; index+1 is an int constant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", index+1)); err != nil {
		return fmt.Errorf("store: bump schema version to %d: %w", index+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %d: %w", index+1, err)
	}
	return nil
}

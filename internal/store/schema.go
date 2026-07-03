package store

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersionV1 is the migration version this package owns. issue #14 adds V2
// (the retention ack-flag column), so V1 is the ceiling here — the version
// number is the shared contract that keeps the two issues from colliding.
const schemaVersionV1 = 1

// migrations is the ordered migration list. Index i migrates the schema from
// version i to version i+1; migrations[0] establishes V1. Append-only: never
// edit a shipped migration, add a new one.
var migrations = []string{
	schemaV1,
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

// migrate brings the database up to the latest migration, running each pending
// step in its own transaction and advancing PRAGMA user_version transactionally.
// It is idempotent: reopening an up-to-date database is a no-op.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
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

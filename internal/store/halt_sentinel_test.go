package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

// TestGlobalHaltLifecycleDurable is the AC-1 guard for ADR-0012 Decision 1(c):
// the global halt is a durable none→pending→halted→none lifecycle. Each
// transition must commit durably and reload after a restart, and a trip
// interrupted at pending must reload as pending so the consumer (#32/#36) can
// treat it as halted (persistence-wins). Each leg reopens the store to prove the
// transition crossed a real fsync/WAL boundary, not just process memory.
func TestGlobalHaltLifecycleDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	// Fresh store: no halt.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if hs, err := db.Halt(ctx); err != nil || hs.Phase != HaltNone {
		t.Fatalf("fresh halt = %+v, err %v, want phase none", hs, err)
	}

	// none → pending (trip durably initiated but TripHalt not yet complete).
	if err := db.MarkHaltPending(ctx, "trip-initiated"); err != nil {
		t.Fatalf("MarkHaltPending: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: an interrupted trip reloads as pending, keeping reason + tripped_at.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after pending: %v", err)
	}
	hs, err := db2.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after pending reopen: %v", err)
	}
	if hs.Phase != HaltPending {
		t.Fatalf("interrupted trip reloaded as %q, want pending (consumer treats pending as halted)", hs.Phase)
	}
	if hs.Reason != "trip-initiated" || hs.TrippedAt.IsZero() {
		t.Fatalf("pending halt = %+v, want reason 'trip-initiated' and non-zero tripped_at", hs)
	}
	pendingTrippedAt := hs.TrippedAt

	// pending → halted (trip complete). tripped_at is preserved from the pending
	// phase so it records when the trip was first initiated.
	if err := db2.TripHalt(ctx, "trip-complete"); err != nil {
		t.Fatalf("TripHalt: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db3, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after halted: %v", err)
	}
	hs, err = db3.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after halted reopen: %v", err)
	}
	if hs.Phase != HaltHalted || hs.Reason != "trip-complete" {
		t.Fatalf("halted reload = %+v, want phase halted reason 'trip-complete'", hs)
	}
	if !hs.TrippedAt.Equal(pendingTrippedAt) {
		t.Fatalf("tripped_at after pending→halted = %v, want preserved %v", hs.TrippedAt, pendingTrippedAt)
	}

	// halted → none (manual clear per ADR-0004 point 6): reason and tripped_at reset.
	if err := db3.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if err := db3.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db4, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after clear: %v", err)
	}
	defer db4.Close()
	hs, err = db4.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after clear reopen: %v", err)
	}
	if hs.Phase != HaltNone || hs.Reason != "" || !hs.TrippedAt.IsZero() {
		t.Fatalf("cleared halt = %+v, want phase none, empty reason, zero tripped_at", hs)
	}
}

// TestSentinelSingleStateNoCoexistence is the AC-2 guard for ADR-0012 Decision
// 1(c) (표현): the clean-shutdown sentinel is a single lifecycle field/row, not
// two coexisting records. A running set must overwrite the previous clean in the
// same write, and the schema must structurally forbid a second sentinel record
// (so a crash can never leave a stale clean beside a running — sentinel fail-open
// #1). The conservative fresh default is running (= not known-clean).
func TestSentinelSingleStateNoCoexistence(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// Fresh default is running: a store that never recorded a clean shutdown must
	// not present itself as clean.
	s, err := db.Lifecycle(ctx)
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	if s != LifecycleRunning {
		t.Fatalf("fresh lifecycle = %q, want running (conservative default)", s)
	}

	// graceful shutdown writes clean...
	if err := db.SetLifecycle(ctx, LifecycleClean); err != nil {
		t.Fatalf("SetLifecycle(clean): %v", err)
	}
	if s, _ := db.Lifecycle(ctx); s != LifecycleClean {
		t.Fatalf("lifecycle after clean = %q, want clean", s)
	}

	// ...and the next boot overwrites clean with running in the SAME single-field
	// write — the two values can never coexist.
	if err := db.SetLifecycle(ctx, LifecycleRunning); err != nil {
		t.Fatalf("SetLifecycle(running): %v", err)
	}
	if s, _ := db.Lifecycle(ctx); s != LifecycleRunning {
		t.Fatalf("lifecycle after running = %q, want running (overwrote clean)", s)
	}

	// Coexistence is structurally impossible: exactly one sentinel row exists...
	var n int
	if err := db.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM lifecycle").Scan(&n); err != nil {
		t.Fatalf("count lifecycle rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("lifecycle rows = %d, want exactly 1 (single state, no coexistence)", n)
	}
	// ...and the schema forbids inserting a second sentinel record.
	if _, err := db.writeDB.ExecContext(ctx, "INSERT INTO lifecycle (id, state) VALUES (2, 'clean')"); err == nil {
		t.Fatal("inserting a second lifecycle row succeeded; CHECK(id=1) must forbid coexistence")
	}
	// And an out-of-range value is rejected by the CHECK (store returns error =
	// fail-closed truth-or-error contract).
	if err := db.SetLifecycle(ctx, LifecycleState("bogus")); err == nil {
		t.Fatal("SetLifecycle with bogus value succeeded; CHECK must reject unknown lifecycle states")
	}
}

// TestSentinelPersistsAcrossReopen guards that the sentinel is durable and that
// store is passive about it: store persists exactly what it is told and does NOT
// auto-flip to running on Open (the boot-write is the consumer's job — #36). So a
// clean marker survives reopen unchanged until a consumer overwrites it.
func TestSentinelPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.SetLifecycle(ctx, LifecycleClean); err != nil {
		t.Fatalf("SetLifecycle(clean): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	s, err := db2.Lifecycle(ctx)
	if err != nil {
		t.Fatalf("Lifecycle after reopen: %v", err)
	}
	if s != LifecycleClean {
		t.Fatalf("lifecycle after reopen = %q, want clean (store is passive, must not auto-flip)", s)
	}
}

// TestSentinelAndHaltComposeInOneTx is the AC-3 guard: both the sentinel and the
// halt-state transitions must be usable inside Atomically alongside other writes
// so a consumer (killswitch) can, e.g., set the boot sentinel + open a pending
// halt + append a journal marker as one all-or-nothing durable event
// (ADR-0012 Decision 1(c)/2, ADR-0005 point 3).
func TestSentinelAndHaltComposeInOneTx(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("seed AppendIntent: %v", err)
	}

	// Commit path: sentinel + halt-pending + marker land together.
	err := db.Atomically(ctx, func(tx Tx) error {
		if err := tx.SetLifecycle(ctx, LifecycleRunning); err != nil {
			return err
		}
		if err := tx.MarkHaltPending(ctx, "trip-underway"); err != nil {
			return err
		}
		return tx.AppendMarker(ctx, "i1", MarkerSubmitAttempted, "")
	})
	if err != nil {
		t.Fatalf("Atomically compose: %v", err)
	}
	if s, _ := db.Lifecycle(ctx); s != LifecycleRunning {
		t.Errorf("lifecycle = %q, want running (committed with tx)", s)
	}
	if hs, _ := db.Halt(ctx); hs.Phase != HaltPending {
		t.Errorf("halt = %+v, want pending (committed with tx)", hs)
	}
	got, _ := db.LoadUnresolvedIntents(ctx)
	if len(got) != 1 || len(got[0].Markers) != 2 {
		t.Errorf("markers = %+v, want prepared+submit-attempted committed with tx", got)
	}

	// Rollback path: a sentinel write inside a failing tx must roll back with the
	// rest — it must not leak past the transaction boundary.
	if err := db.SetLifecycle(ctx, LifecycleClean); err != nil {
		t.Fatalf("SetLifecycle(clean): %v", err)
	}
	err = db.Atomically(ctx, func(tx Tx) error {
		if err := tx.SetLifecycle(ctx, LifecycleRunning); err != nil {
			return err
		}
		if err := tx.TripHalt(ctx, "should-roll-back"); err != nil {
			return err
		}
		return errBoom
	})
	if err == nil {
		t.Fatal("Atomically returned nil, want errBoom")
	}
	if s, _ := db.Lifecycle(ctx); s != LifecycleClean {
		t.Errorf("lifecycle after rollback = %q, want clean (running write rolled back)", s)
	}
	if hs, _ := db.Halt(ctx); hs.Phase != HaltPending {
		t.Errorf("halt after rollback = %+v, want still pending (TripHalt rolled back)", hs)
	}
}

// TestMigrationV2PreservesExistingHalt is the AC-4 migration guard: a store still
// on the V1 schema (boolean halt) with a tripped halted record must migrate to
// the 3-state lifecycle without losing that record — the old halted row maps to
// phase halted and keeps its reason + tripped_at, so a mid-upgrade restart cannot
// become a safety-guard bypass.
func TestMigrationV2PreservesExistingHalt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()
	const trippedAt = int64(1_700_000_000_000_000_000)

	// Build a database that stopped at the V1 schema (only migrations[0] applied),
	// carrying a tripped halted record, exactly as an older binary left it.
	seedV1HaltedDB(t, path, trippedAt)

	// Open runs the pending V2 migration.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate V1→V2): %v", err)
	}
	defer db.Close()

	// Open now migrates a V1-seeded DB all the way to the latest migration; the V2
	// halt preservation asserted below is unaffected because every later migration
	// is additive and none of them touches the halt table.
	if v, err := db.schemaVersion(ctx); err != nil || v != len(migrations) {
		t.Fatalf("schema version after migrate = %d, err %v, want %d (the latest migration)", v, err, len(migrations))
	}
	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after migrate: %v", err)
	}
	if hs.Phase != HaltHalted {
		t.Fatalf("migrated halt phase = %q, want halted (existing halted record preserved)", hs.Phase)
	}
	if hs.Reason != "pre-migration-trip" {
		t.Fatalf("migrated halt reason = %q, want 'pre-migration-trip'", hs.Reason)
	}
	if !hs.TrippedAt.Equal(time.Unix(0, trippedAt)) {
		t.Fatalf("migrated tripped_at = %v, want %v", hs.TrippedAt, time.Unix(0, trippedAt))
	}

	// The sentinel table exists post-migration and defaults to the conservative
	// running (a migrated DB has recorded no clean shutdown).
	if s, err := db.Lifecycle(ctx); err != nil || s != LifecycleRunning {
		t.Fatalf("lifecycle after migrate = %q, err %v, want running", s, err)
	}

	// The migration recreates only the halt table; the journal it does not touch
	// must survive untouched — a pre-migration unresolved intent is exactly the
	// restart-recovery state a migration must never drop.
	intents, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents after migrate: %v", err)
	}
	if len(intents) != 1 || intents[0].IntentID != "pre-migration-intent" {
		t.Fatalf("intents after migrate = %+v, want the pre-migration unresolved intent preserved", intents)
	}
}

// TestMigrationV2PreservesUnhaltedRecord is the mirror: a V1 store that was not
// halted (halted=0) must migrate to phase none, not accidentally trip on upgrade.
func TestMigrationV2PreservesUnhaltedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	seedV1UnhaltedDB(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate V1→V2): %v", err)
	}
	defer db.Close()
	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after migrate: %v", err)
	}
	if hs.Phase != HaltNone {
		t.Fatalf("migrated unhalted phase = %q, want none (must not trip on upgrade)", hs.Phase)
	}
}

// seedV1HaltedDB writes a fresh database frozen at the V1 schema with the halt
// singleton tripped (halted=1), then leaves user_version at 1 so Open() sees a
// pending V2 migration.
func seedV1HaltedDB(t *testing.T, path string, trippedAt int64) {
	t.Helper()
	seedV1DB(t, path, func(raw *sql.DB) {
		if _, err := raw.Exec(
			"UPDATE halt SET halted = 1, reason = 'pre-migration-trip', tripped_at = ? WHERE id = 1",
			trippedAt,
		); err != nil {
			t.Fatalf("seed halted row: %v", err)
		}
		// Seed an unresolved intent + its prepared marker so the migration test
		// also proves the untouched journal survives V1→V2.
		if _, err := raw.Exec(
			"INSERT INTO intents (intent_id, client_order_id, payload, created_at) VALUES ('pre-migration-intent', 'coid', NULL, 1)",
		); err != nil {
			t.Fatalf("seed intent row: %v", err)
		}
		if _, err := raw.Exec(
			"INSERT INTO markers (intent_id, kind, at) VALUES ('pre-migration-intent', 'prepared', 1)",
		); err != nil {
			t.Fatalf("seed marker row: %v", err)
		}
	})
}

// seedV1UnhaltedDB writes a fresh V1-schema database with the halt singleton left
// untripped (the default halted=0 row).
func seedV1UnhaltedDB(t *testing.T, path string) {
	t.Helper()
	seedV1DB(t, path, func(*sql.DB) {})
}

// seedV1DB applies only the V1 migration DDL to a fresh file, runs mutate, and
// pins user_version=1 so the real Open() must run the V2 migration. It uses the
// same durability pragmas as the store so the seed lands on a real WAL database.
func seedV1DB(t *testing.T, path string, mutate func(*sql.DB)) {
	t.Helper()
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	raw, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		t.Fatalf("open raw V1 db: %v", err)
	}
	defer func() {
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw V1 db: %v", err)
		}
	}()
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("apply schemaV1: %v", err)
	}
	mutate(raw)
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("pin user_version=1: %v", err)
	}
}

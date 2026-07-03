package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// openTemp opens a store on a fresh file inside the test's temp dir. Using a
// real on-disk file (not :memory:) is deliberate: ADR-0005 requires crash,
// durability, partial-write, and atomicity to be verified against the real
// engine with real fsync/WAL semantics, which an in-memory DB cannot exercise.
func openTemp(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

// TestDurabilityPragmas is the load-bearing guard for ADR-0005 point 4: the
// durability contract (synchronous=FULL + WAL) must never be silently relaxed.
// It reads the pragmas back from the live write connection, so it fails both
// when the DSN pragma syntax does not apply and when someone weakens durability.
func TestDurabilityPragmas(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	var sync int
	if err := db.writeDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if sync != 2 { // 2 == FULL
		t.Errorf("synchronous = %d, want 2 (FULL); durability must not be relaxed (ADR-0005 point 4)", sync)
	}

	var journal string
	if err := db.writeDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal (ADR-0005 point 4)", journal)
	}

	// The read handle must also observe FULL: durability is per-connection.
	var rsync int
	if err := db.readDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&rsync); err != nil {
		t.Fatalf("read synchronous (read handle): %v", err)
	}
	if rsync != 2 {
		t.Errorf("read handle synchronous = %d, want 2 (FULL)", rsync)
	}
}

// TestMigrationVersion checks the migration framework: a fresh file lands at the
// V1 user_version, and reopening is a no-op that leaves the version unchanged.
func TestMigrationVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	v, err := db.schemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if v != schemaVersionV1 {
		t.Fatalf("schema version = %d, want %d", v, schemaVersionV1)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: migrations must be idempotent (no-op), version unchanged.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	v2, err := db2.schemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schemaVersion (reopen): %v", err)
	}
	if v2 != schemaVersionV1 {
		t.Fatalf("schema version after reopen = %d, want %d", v2, schemaVersionV1)
	}
}

// TestOpenRejectsFutureSchema guards against schema skew: a V1 binary must
// refuse to open (and write to) a store already migrated to a newer version
// (e.g. #14's V2), otherwise it would silently write with obsolete assumptions
// and corrupt/bypass newer invariants (audit-ack / prune gating).
func TestOpenRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Simulate a future migration having run against this file.
	if _, err := db.writeDB.ExecContext(context.Background(), "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = Open(path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open on future schema err = %v, want ErrSchemaTooNew", err)
	}
}

// TestConcurrentAtomically is the single-writer serialization guard (ADR-0005
// follow-up): many goroutines calling Atomically concurrently must serialize
// through the dedicated write connection without a spurious SQLITE_BUSY
// fail-closed. Run under -race.
func TestConcurrentAtomically(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	const goroutines = 16
	const perG = 25

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				err := db.Atomically(ctx, func(tx Tx) error {
					c, err := tx.Counter(ctx, "orders")
					if err != nil {
						return err
					}
					c.Value++
					return tx.SetCounter(ctx, c)
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Atomically under contention: %v", err)
	}

	// Read-modify-write serialized correctly: no lost updates.
	c, err := db.Counter(ctx, "orders")
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if want := int64(goroutines * perG); c.Value != want {
		t.Fatalf("counter = %d, want %d (lost updates ⇒ serialization broken)", c.Value, want)
	}
}

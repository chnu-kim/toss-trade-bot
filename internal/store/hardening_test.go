package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCreatesOwnerOnlyFiles is the M-2 guard: the .db and its WAL/SHM
// sidecars must be owner-only (0o600), independent of the process umask, so a
// co-tenant on a shared host cannot read the account's trading activity (order
// intents, client_order_ids, halt reasons). A durable write forces the WAL/SHM
// files into existence before they are stat'd.
func TestOpenCreatesOwnerOnlyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// A write materializes the -wal/-shm sidecars.
	if err := db.AppendIntent(context.Background(), Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v (expected to exist)", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %#o, want 0o600 (owner-only, no group/other access — M-2)", p, perm)
		}
	}
}

// TestOpenTightensGroupOtherReadableFile is the M-2 upgrade path: a pre-hardening
// database left group/other-readable (0o644) must be tightened in place on Open,
// not left leaking and not refused (refusing would stop an unattended bot from
// booting — the store repairs the mode fail-safe instead).
func TestOpenTightensGroupOtherReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	// Simulate a database an older (pre-M-2) binary left world-readable. A
	// zero-length file is a valid empty SQLite database, so Open migrates it.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed 0644 file: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open on pre-existing 0644 file: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("existing file mode after Open = %#o, want 0o600 (group/other bits tightened — M-2)", perm)
	}
}

// TestSecureDBFileTightensSidecars is the M-2 upgrade-path guard for the WAL/SHM
// sidecars: SQLite only sets a sidecar's mode when it CREATES it, so a
// pre-hardening database whose -wal/-shm an older binary left at 0644 (holding
// uncheckpointed journal pages — the same sensitive data as the .db) would keep
// leaking after upgrade if only the main file were tightened. secureDBFile must
// repair pre-existing sidecars in place — and must NOT create absent ones (an
// empty -wal/-shm invented here could corrupt SQLite's WAL recovery). Tested
// directly (rather than via Open) because SQLite discards and recreates empty
// sidecars on open, which would mask whether the repair actually fired.
func TestSecureDBFileTightensSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	// A pre-hardening WAL database left behind group/other-readable: the main file
	// AND its sidecars all at 0644.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, nil, 0o644); err != nil {
			t.Fatalf("seed %s: %v", path+suffix, err)
		}
	}

	if err := secureDBFile(path); err != nil {
		t.Fatalf("secureDBFile: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("stat %s: %v", path+suffix, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %#o, want 0o600 (M-2 sidecar tighten)", path+suffix, perm)
		}
	}

	// On a fresh database the sidecars are absent; secureDBFile must only repair,
	// never create them (SQLite creates the real sidecars itself, 0o600-inherited).
	fresh := filepath.Join(t.TempDir(), "store.db")
	if err := secureDBFile(fresh); err != nil {
		t.Fatalf("secureDBFile fresh: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(fresh + suffix); !os.IsNotExist(err) {
			t.Errorf("secureDBFile created sidecar %s (err=%v); it must repair, not create", fresh+suffix, err)
		}
	}
}

// TestOpenRejectsNonRegularPathWithoutDamage guards the permission-repair path
// against collateral damage: if the configured DB path (or a sidecar path) is
// accidentally a directory or other non-regular entry, Open must fail closed
// WITHOUT chmodding it — chmod 0o600 on a 0o755 directory makes it non-traversable
// even for the owner, damaging a filesystem entry outside the database-file case.
func TestOpenRejectsNonRegularPathWithoutDamage(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "notafile")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := Open(target); err == nil {
		t.Fatal("Open on a directory path succeeded, want error")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("directory mode after failed Open = %#o, want unchanged 0o755 (repair must not damage non-regular entries)", perm)
	}
}

// TestHaltWritersFailWhenSingletonRowMissing is the L-1 guard: if the halt
// singleton (id=1) is absent, the UPDATE matches zero rows. The write must fail
// closed (ErrHaltRowMissing) rather than return a false durable-ack — otherwise
// killswitch believes a halt is persisted when nothing was written and a restart
// boots un-halted (ADR-0004 silent fail-open; the PR#22 false-durable-ack class).
func TestHaltWritersFailWhenSingletonRowMissing(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// Simulate a botched migration rebuild / backup restore / manual tampering.
	if _, err := db.writeDB.ExecContext(ctx, "DELETE FROM halt WHERE id = 1"); err != nil {
		t.Fatalf("delete halt singleton: %v", err)
	}

	if err := db.TripHalt(ctx, "boom"); !errors.Is(err, ErrHaltRowMissing) {
		t.Fatalf("TripHalt with missing singleton err = %v, want ErrHaltRowMissing (no false durable-ack)", err)
	}
	if err := db.ClearHalt(ctx); !errors.Is(err, ErrHaltRowMissing) {
		t.Fatalf("ClearHalt with missing singleton err = %v, want ErrHaltRowMissing", err)
	}
	if err := db.MarkHaltPending(ctx, "boom"); !errors.Is(err, ErrHaltRowMissing) {
		t.Fatalf("MarkHaltPending with missing singleton err = %v, want ErrHaltRowMissing", err)
	}
}

// TestHaltAckImpliesRecorded fixes the L-1 invariant from the happy-path side:
// when a halt write returns nil, the halt must be durably readable back — "what
// was acked is recorded". A repeated trip with identical values stays idempotent
// (RowsAffected on a matched singleton row is 1, not 0).
func TestHaltAckImpliesRecorded(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.TripHalt(ctx, "threshold"); err != nil {
		t.Fatalf("TripHalt: %v", err)
	}
	// Re-trip with the same values must remain a clean ack (no spurious
	// ErrHaltRowMissing from a zero-change UPDATE).
	if err := db.TripHalt(ctx, "threshold"); err != nil {
		t.Fatalf("re-TripHalt: %v", err)
	}

	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if hs.Phase != HaltHalted || hs.Reason != "threshold" {
		t.Fatalf("after acked TripHalt, halt = %+v, want halted/threshold", hs)
	}
}

// TestResolveIntentIdempotentSameResolution is one half of L-7: re-resolving a
// terminally-closed intent with the SAME resolution is genuinely idempotent (nil).
func TestResolveIntentIdempotentSameResolution(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.ResolveIntent(ctx, "i1", "filled"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if err := db.ResolveIntent(ctx, "i1", "filled"); err != nil {
		t.Fatalf("idempotent re-resolve err = %v, want nil", err)
	}
}

// TestResolveIntentConflictingResolution is the other half of L-7: re-resolving a
// terminally-closed intent with a DIFFERENT resolution is a journal-consistency
// conflict and must surface as ErrResolutionConflict without overwriting the
// durably-recorded first verdict (so the #35 reconciler can log/alert instead of
// silently losing the contradiction).
func TestResolveIntentConflictingResolution(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.ResolveIntent(ctx, "i1", "filled"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	err := db.ResolveIntent(ctx, "i1", "aborted-before-submit")
	if !errors.Is(err, ErrResolutionConflict) {
		t.Fatalf("conflicting re-resolve err = %v, want ErrResolutionConflict", err)
	}

	// The first verdict must remain durably recorded — a conflict never overwrites.
	var stored string
	if err := db.readDB.QueryRowContext(ctx, "SELECT resolution FROM intents WHERE intent_id = 'i1'").Scan(&stored); err != nil {
		t.Fatalf("read stored resolution: %v", err)
	}
	if stored != "filled" {
		t.Fatalf("stored resolution = %q, want unchanged 'filled' (conflict must not overwrite)", stored)
	}
}

// TestOpenRejectsUnsafePath is the L-8 guard: a database path containing a
// character that corrupts the "file:PATH?query" URI DSN (#, ?, or %) must be
// refused at Open. Without the guard a '#' path silently opens a *different*
// (truncated) file — verified: ".../data#prod/store.db" opens ".../data" — which
// breaks the "reopen the same journal" premise restart recovery depends on
// (ADR-0003/0005), and a '?' can drop the durability pragmas.
func TestOpenRejectsUnsafePath(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"data#prod", "data?x", "data%2f"} {
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		path := filepath.Join(sub, "store.db")

		_, err := Open(path)
		if !errors.Is(err, ErrUnsafeDBPath) {
			t.Fatalf("Open(%q) err = %v, want ErrUnsafeDBPath (silent wrong-file / pragma loss must be blocked)", path, err)
		}
	}
}

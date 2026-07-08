package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

// TestAtomicallyRecoversConnectionAfterCallbackPanic is the regression guard for
// issue #24 (security audit finding H-1): a write callback that panics must not
// permanently leak the sole write connection. Before the fix, BeginTx was not
// followed by a deferred Rollback, so a panicking fn left the *sql.Tx neither
// committed nor rolled back. Because writeDB is the dedicated single connection
// (SetMaxOpenConns(1)), that leaked connection blocked every subsequent write
// (TripHalt, AppendIntent, ...) forever — a "zombie" store that still serves
// reads (readDB) but can never write again, even after the caller (e.g.
// runtime.Supervisor) recovers the panic and keeps running.
func TestAtomicallyRecoversConnectionAfterCallbackPanic(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("callback did not panic; test setup broken")
			}
		}()
		_ = db.Atomically(ctx, func(tx Tx) error {
			panic("simulated callback panic")
		})
	}()

	// The next write must succeed promptly, even on a context with no deadline
	// (Acceptance Criteria): if the write connection leaked, BeginTx would block
	// forever waiting for the pool to free a connection (MaxOpenConns=1), since
	// context.Background() never cancels. Race the call against a generous
	// timeout so a regression fails the test instead of hanging the suite.
	done := make(chan error, 1)
	go func() { done <- db.TripHalt(context.Background(), "post-panic recovery check") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TripHalt after recovered panic: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TripHalt after recovered panic blocked >5s: write connection leaked (issue #24)")
	}

	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !hs.Halted || hs.Reason != "post-panic recovery check" {
		t.Fatalf("halt = %+v, want halted with reason after recovery TripHalt", hs)
	}
}

// TestAtomicallyRollsBackAllWrites is the atomicity guard for ADR-0005 point 3:
// when a single logical event touches the journal AND halt/counter, a failure
// mid-event must roll back everything — they live or die together.
func TestAtomicallyRollsBackAllWrites(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	err := db.Atomically(ctx, func(tx Tx) error {
		if err := tx.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
			return err
		}
		if err := tx.TripHalt(ctx, "over-threshold"); err != nil {
			return err
		}
		if err := tx.SetCounter(ctx, Counter{Name: "fails", Value: 5}); err != nil {
			return err
		}
		return errBoom // abort after several writes
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Atomically err = %v, want errBoom", err)
	}

	// None of the three writes may have persisted.
	intents, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("intents = %+v, want none (rolled back)", intents)
	}
	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if hs.Halted {
		t.Errorf("halt = %+v, want not halted (rolled back)", hs)
	}
	c, err := db.Counter(ctx, "fails")
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 0 {
		t.Errorf("counter = %d, want 0 (rolled back)", c.Value)
	}
}

// TestAtomicallyCommitsAllWrites is the mirror: on success, the journal marker
// and the halt trip commit together — the atomic coupling ADR-0004 requires.
func TestAtomicallyCommitsAllWrites(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("seed AppendIntent: %v", err)
	}

	err := db.Atomically(ctx, func(tx Tx) error {
		if err := tx.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
			return err
		}
		return tx.TripHalt(ctx, "ambiguous-frequent")
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}

	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !hs.Halted || hs.Reason != "ambiguous-frequent" {
		t.Errorf("halt = %+v, want halted with reason", hs)
	}
	got, _ := db.LoadUnresolvedIntents(ctx)
	if len(got[0].Markers) != 2 {
		t.Errorf("markers = %+v, want prepared+submit-attempted committed", got[0].Markers)
	}
}

// TestReadThenWriteInOneTx exercises the read-then-write pattern Atomically
// exists for: read a counter, increment, trip halt over threshold — atomically.
func TestReadThenWriteInOneTx(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	err := db.Atomically(ctx, func(tx Tx) error {
		c, err := tx.Counter(ctx, "token-refresh-failures")
		if err != nil {
			return err
		}
		c.Value++
		if err := tx.SetCounter(ctx, c); err != nil {
			return err
		}
		if c.Value >= 1 {
			return tx.TripHalt(ctx, "token refresh failing")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}
	c, _ := db.Counter(ctx, "token-refresh-failures")
	if c.Value != 1 {
		t.Errorf("counter = %d, want 1", c.Value)
	}
	hs, _ := db.Halt(ctx)
	if !hs.Halted {
		t.Errorf("halt = %+v, want halted", hs)
	}
}

// TestHaltPersistsAcrossReopen is the restart-safety guard for ADR-0004: a
// tripped global halt must survive process death so a restart boots halted
// instead of bypassing the safety guard.
func TestHaltPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.TripHalt(ctx, "manual-e2e"); err != nil {
		t.Fatalf("TripHalt: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	hs, err := db2.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt after reopen: %v", err)
	}
	if !hs.Halted || hs.Reason != "manual-e2e" || hs.TrippedAt.IsZero() {
		t.Fatalf("halt after reopen = %+v, want halted with reason and tripped time", hs)
	}

	// Manual clear (ADR-0004 point 6) then reopen: stays cleared.
	if err := db2.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	hs2, _ := db2.Halt(ctx)
	if hs2.Halted || !hs2.TrippedAt.IsZero() {
		t.Fatalf("halt after clear = %+v, want cleared", hs2)
	}
}

// TestCounterPersistsAcrossReopen guards ADR-0004 point 7: reconstruction-
// resistant counters must not reset to zero on restart.
func TestCounterPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()
	window := time.Unix(1_700_000_000, 0)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.SetCounter(ctx, Counter{Name: "fails", Value: 3, WindowStart: window}); err != nil {
		t.Fatalf("SetCounter: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	c, err := db2.Counter(ctx, "fails")
	if err != nil {
		t.Fatalf("Counter after reopen: %v", err)
	}
	if c.Value != 3 {
		t.Errorf("counter value = %d, want 3 (must not reset on restart)", c.Value)
	}
	if !c.WindowStart.Equal(window) {
		t.Errorf("counter window = %v, want %v", c.WindowStart, window)
	}
}

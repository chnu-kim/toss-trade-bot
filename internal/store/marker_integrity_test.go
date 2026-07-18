package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// This file covers the V4 markers integrity constraints (issue #29): the
// 2-marker write-ahead protocol's invariants (ADR-0002) enforced by the SCHEMA
// plus the store-layer terminal guard, so a protocol violation is rejected at the
// durability layer instead of being silently accepted. Every test runs against a
// real SQLite engine in a temp dir (ADR-0005 point 2).

// --- the four violations the audit found silently accepted (M-7) ---

// TestSecondSubmitAttemptedRejected: a second submit-attempted marker is the
// durable record of a SECOND POST attempt for one intent. Because the marker is
// appended BEFORE the irreversible POST (ADR-0002 write-ahead), rejecting it here
// stops the duplicate submit before money can move.
func TestSecondSubmitAttemptedRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("first submit-attempted must be accepted: %v", err)
	}

	err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, "")
	if !errors.Is(err, ErrDuplicateMarker) {
		t.Fatalf("second submit-attempted err = %v, want ErrDuplicateMarker", err)
	}
	assertMarkerKinds(t, db, "i1", MarkerPrepared, MarkerSubmitAttempted)
}

// TestSecondPreparedRejected: prepared is written by AppendIntent, so a second one
// means the journal is being re-opened for an intent that already exists.
func TestSecondPreparedRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerPrepared, ""); !errors.Is(err, ErrDuplicateMarker) {
		t.Fatalf("second prepared err = %v, want ErrDuplicateMarker", err)
	}
}

// TestAckedWithEmptyOrderIDRejected: an acked marker exists to persist the
// orderId, which is the ONLY post-hoc truth handle (ADR-0002 point 3 —
// clientOrderId is not queryable). An acked with no orderId claims "the POST was
// acknowledged" while destroying the evidence needed to verify it, which would
// make a live order unreconcilable.
func TestAckedWithEmptyOrderIDRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	// Seed the legal predecessor so the ONLY violation under test is the empty
	// orderId — otherwise the ordering guard would reject it first and this test
	// would pass without ever exercising the shape constraint.
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, ""); !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("acked with empty order_id err = %v, want ErrInvalidMarker", err)
	}
	assertMarkerKinds(t, db, "i1", MarkerPrepared, MarkerSubmitAttempted)
}

// TestMarkerOutOfOrderRejected: the 2-marker model's whole discriminating power
// is that submit-attempted's ABSENCE proves the POST certainly did not happen
// (ADR-0002). An acked marker with no submit-attempted before it claims an
// acknowledged order while the journal still says no POST was attempted, which is
// precisely the "단일 마커" degenerate case ADR-0002 rejected.
func TestMarkerOutOfOrderRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-1")
	if !errors.Is(err, ErrMarkerOutOfOrder) {
		t.Fatalf("acked without a preceding submit-attempted err = %v, want ErrMarkerOutOfOrder", err)
	}
	// Not misreported as one of the neighbouring failure modes.
	if errors.Is(err, ErrMarkerAfterTerminal) || errors.Is(err, ErrIntentNotFound) {
		t.Fatalf("out-of-order marker misclassified: %v", err)
	}
	assertMarkerKinds(t, db, "i1", MarkerPrepared)

	// Once the predecessor is there, the same append is accepted — the guard gates
	// on the progression, not on the kind.
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-1"); err != nil {
		t.Fatalf("acked after its predecessor must be accepted: %v", err)
	}
}

// TestSecondAckedWithDifferentOrderIDRejected: two acked markers with different
// orderIds make the truth handle ambiguous — the reconciler could verify the wrong
// order. One intent binds at most one orderId (ADR-0002/0003).
func TestSecondAckedWithDifferentOrderIDRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-1"); err != nil {
		t.Fatalf("first acked: %v", err)
	}

	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-2"); !errors.Is(err, ErrDuplicateMarker) {
		t.Fatalf("second acked with a different orderId err = %v, want ErrDuplicateMarker", err)
	}
	// The originally bound orderId must survive untouched.
	in := loadOne(t, db, "i1")
	last := in.Markers[len(in.Markers)-1]
	if last.Kind != MarkerAcked || last.OrderID != "ord-1" {
		t.Fatalf("acked marker = %+v, want the first-bound ord-1 preserved", last)
	}
}

// TestMarkerAfterTerminalRejected: once the reconciler terminally resolves an
// intent (ADR-0003) its journal entry is closed. A later marker would reopen a
// settled intent and could resurrect it into the ambiguous set. SQL CHECK cannot
// express this cross-row rule, so the store layer enforces it as a conditional
// insert.
func TestMarkerAfterTerminalRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.ResolveIntent(ctx, "i1", "aborted-before-submit"); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	for _, tc := range []struct {
		kind    MarkerKind
		orderID string
	}{
		{MarkerSubmitAttempted, ""},
		{MarkerAcked, "ord-late"},
	} {
		err := db.AppendMarker(ctx, "i1", tc.kind, tc.orderID)
		if !errors.Is(err, ErrMarkerAfterTerminal) {
			t.Fatalf("append %s after terminal err = %v, want ErrMarkerAfterTerminal", tc.kind, err)
		}
	}
	if got := markerCount(t, db, "i1"); got != 1 {
		t.Fatalf("marker count = %d, want 1 (only the prepared marker; nothing appended after terminal)", got)
	}
}

// --- shape constraints ---

// TestUnknownMarkerKindRejected keeps the marker vocabulary fixed at the ADR-0002
// set. A typo'd kind would otherwise persist and silently fall through the
// reconciler's branch (ADR-0003), leaving an intent unclassified forever.
func TestUnknownMarkerKindRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	for _, kind := range []MarkerKind{"", "submitted", "ACKED", "resolved"} {
		if err := db.AppendMarker(ctx, "i1", kind, ""); !errors.Is(err, ErrInvalidMarker) {
			t.Fatalf("append kind %q err = %v, want ErrInvalidMarker", kind, err)
		}
	}
}

// TestNonAckedMarkerWithOrderIDRejected: "OrderID is set only on the acked
// marker" is the documented Marker contract, and ReconstructLifecycleRecords
// silently DROPS an orderId carried by any other kind — so a stray one would be
// invisible corruption rather than a loud error.
func TestNonAckedMarkerWithOrderIDRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, "ord-stray"); !errors.Is(err, ErrInvalidMarker) {
		t.Fatalf("submit-attempted carrying an orderId err = %v, want ErrInvalidMarker", err)
	}
}

// TestMarkerForMissingIntentRejected: the conditional insert must distinguish
// "no such intent" from "intent already terminal" — collapsing them would send a
// caller chasing the wrong bug.
func TestMarkerForMissingIntentRejected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	err := db.AppendMarker(ctx, "ghost", MarkerSubmitAttempted, "")
	if !errors.Is(err, ErrIntentNotFound) {
		t.Fatalf("append for a missing intent err = %v, want ErrIntentNotFound", err)
	}
	if errors.Is(err, ErrMarkerAfterTerminal) {
		t.Fatal("a missing intent must not be reported as a terminal violation")
	}
}

// --- the legal path must still pass (the constraints must not over-reject) ---

// TestLegalTwoMarkerFlowStillAccepted is the other half of the bidirectional
// proof: the full ADR-0002 progression, and a prepared-only intent that is
// aborted before submit, are both still accepted end to end.
func TestLegalTwoMarkerFlowStillAccepted(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "full", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "full", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, "full", MarkerAcked, "ord-1"); err != nil {
		t.Fatalf("acked: %v", err)
	}
	assertMarkerKinds(t, db, "full", MarkerPrepared, MarkerSubmitAttempted, MarkerAcked)
	if err := db.ResolveIntent(ctx, "full", "filled"); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	// Prepared-only: killswitch blocked the submit, so no further marker is ever
	// written and the reconciler closes it aborted-before-submit.
	if err := db.AppendIntent(ctx, Intent{IntentID: "aborted", ClientOrderID: "c2"}); err != nil {
		t.Fatalf("AppendIntent(aborted): %v", err)
	}
	if err := db.ResolveIntent(ctx, "aborted", "aborted-before-submit"); err != nil {
		t.Fatalf("ResolveIntent(aborted): %v", err)
	}

	// Two DIFFERENT intents may of course each carry the same marker kinds — the
	// uniqueness is per (intent, kind), not per kind.
	if err := db.AppendIntent(ctx, Intent{IntentID: "other", ClientOrderID: "c3"}); err != nil {
		t.Fatalf("AppendIntent(other): %v", err)
	}
	if err := db.AppendMarker(ctx, "other", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("other intent submit-attempted must be accepted: %v", err)
	}
	// Two intents carrying the SAME orderId is deliberately left unconstrained,
	// and this assertion pins that decision rather than merely observing it.
	// Uniqueness on acked order_id was considered and rejected: the acked marker is
	// written AFTER the irreversible POST, so a rejection there lands on the one
	// path where money has already moved, and order's acked-append failure branch
	// trips the GLOBAL halt. The only ways two intents can share an orderId are a
	// clientOrderId hash collision or the server replaying a duplicate
	// clientOrderId — behaviour ADR-0002 point 4 explicitly records as UNDEFINED.
	// Halting the whole bot on undefined server behaviour, to prevent a
	// double-counted audit record, is the wrong side of the trade
	// (fail-closed-wrong-direction). What order should do when the exchange hands
	// back an already-bound orderId is a policy question for ADR-0003's owner, not
	// for this schema-integrity change.
	if err := db.AppendMarker(ctx, "other", MarkerAcked, "ord-1"); err != nil {
		t.Fatalf("two intents sharing an orderId must not be rejected here: %v", err)
	}
}

// TestRejectedMarkerDoesNotPoisonTransaction: a constraint violation must abort
// only the offending STATEMENT, so a caller that handles the error can still
// commit the rest of its transaction. If it aborted the whole transaction, one
// bad marker would silently roll back an unrelated halt trip composed with it.
func TestRejectedMarkerDoesNotPoisonTransaction(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	err := db.Atomically(ctx, func(tx Tx) error {
		if err := tx.AppendMarker(ctx, "i1", MarkerPrepared, ""); !errors.Is(err, ErrDuplicateMarker) {
			return fmt.Errorf("duplicate prepared err = %v, want ErrDuplicateMarker", err)
		}
		// Same transaction still usable: the legal marker and a halt trip commit.
		if err := tx.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
			return fmt.Errorf("submit-attempted after a rejected marker: %w", err)
		}
		return tx.TripHalt(ctx, "unrelated")
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}
	assertMarkerKinds(t, db, "i1", MarkerPrepared, MarkerSubmitAttempted)
	if hs, _ := db.Halt(ctx); hs.Phase != HaltHalted {
		t.Fatalf("halt = %+v, want the composed trip to have committed", hs)
	}
}

// TestConcurrentDuplicateMarkerRacesToOne is the -race proof that the constraint
// is a real serialization backstop, not a check-then-act: many goroutines append
// the SAME (intent, kind) and exactly one may win. This is the shape a duplicate
// submit race would take, and it must be decided by the engine.
func TestConcurrentDuplicateMarkerRacesToOne(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}

	const racers = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
		bad  []error
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrDuplicateMarker):
			default:
				bad = append(bad, err)
			}
		}()
	}
	wg.Wait()

	if len(bad) > 0 {
		t.Fatalf("losers must fail with ErrDuplicateMarker, got: %v", bad)
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1 (the engine must serialize the duplicate race)", wins)
	}
	assertMarkerKinds(t, db, "i1", MarkerPrepared, MarkerSubmitAttempted)
}

// --- the DDL itself is the backstop, independent of the Go guard ---

// TestSchemaRejectsViolationsBelowTheGoLayer writes raw SQL straight at the
// engine, bypassing appendMarker entirely. This is what makes these constraints a
// STRUCTURAL backstop rather than store-layer discipline: a future writer that
// forgets the guard still cannot persist a protocol violation (the whole point of
// moving enforcement from the order layer into the schema — issue #29).
//
// It covers the three rules a table constraint can express. The two row-spanning
// rules (no marker after terminal, no transition before its predecessor) are NOT
// expressible as a CHECK and are enforced in appendMarker instead, so they bind
// every caller that goes through the store API — the only supported way in
// (ADR-0005) — but not raw SQL against the file. The migration pre-check in
// schemaV4 is what keeps a journal violating those two from being imported.
func TestSchemaRejectsViolationsBelowTheGoLayer(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	const raw = `INSERT INTO markers (intent_id, kind, order_id, at) VALUES (?, ?, ?, 0)`
	for _, tc := range []struct {
		name             string
		kind, orderID    string
		wantConstraintOf string
	}{
		{"duplicate kind", "prepared", "", "UNIQUE"},
		{"acked without orderId", "acked", "", "CHECK"},
		{"non-acked with orderId", "submit-attempted", "ord-x", "CHECK"},
		{"unknown kind", "bogus", "", "CHECK"},
	} {
		if _, err := db.writeDB.ExecContext(ctx, raw, "i1", tc.kind, tc.orderID); err == nil {
			t.Fatalf("%s: raw INSERT succeeded; the schema must reject it (%s constraint missing)", tc.name, tc.wantConstraintOf)
		}
	}
	// The foreign key to intents also still holds after the V4 table rebuild.
	if _, err := db.writeDB.ExecContext(ctx, raw, "ghost", "prepared", ""); err == nil {
		t.Fatal("raw INSERT for a nonexistent intent succeeded; the intents foreign key was lost in the rebuild")
	}
}

// --- migration: V3 → V4 over EXISTING data ---

// preIntegrityVersion is the schema version immediately before the markers
// integrity constraints landed. Pinning it by name (not by len(migrations)) keeps
// these upgrade tests exercising the V3→V4 step even after later migrations are
// appended.
const preIntegrityVersion = schemaVersionV3

// seedLegacyDB builds a database migrated to exactly `upto` migrations, runs seed
// against it, and closes it — so the caller can then Open() it and exercise the
// real upgrade path on pre-existing rows.
func seedLegacyDB(t *testing.T, path string, upto int, seed func(*sql.DB)) {
	t.Helper()
	db, err := openConn(path, true)
	if err != nil {
		t.Fatalf("open legacy conn: %v", err)
	}
	db.SetMaxOpenConns(1)
	for v := 0; v < upto; v++ {
		if err := applyMigration(context.Background(), db, v, migrations[v]); err != nil {
			t.Fatalf("apply legacy migration %d: %v", v+1, err)
		}
	}
	if seed != nil {
		seed(db)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy conn: %v", err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("seed exec %q: %v", query, err)
	}
}

// TestUpgradeOverExistingLegalDataPreservesJournal: upgrading a populated V3
// database must carry every marker across the table rebuild UNCHANGED — including
// its seq, which is load-bearing: audit_acks.record_key is "m:<seq>", so a
// renumbering would dangle every recorded ack and silently break the prune gate
// (ADR-0006 point 4).
func TestUpgradeOverExistingLegalDataPreservesJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	seedLegacyDB(t, path, preIntegrityVersion, func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO intents (intent_id, client_order_id, payload, created_at) VALUES ('i1', 'c1', NULL, 100)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (7, 'i1', 'prepared', '', 101)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (8, 'i1', 'submit-attempted', '', 102)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (9, 'i1', 'acked', 'ord-1', 103)`)
		mustExec(t, db, `INSERT INTO audit_acks (intent_id, record_key, acked_at) VALUES ('i1', 'm:8', 104)`)
		// A RESOLVED intent whose markers all precede its resolution — the shape the
		// post-terminal pre-check must NOT mistake for a violation. Without this the
		// upgrade tests would only ever see unresolved intents and an over-rejecting
		// pre-check would go unnoticed until it refused to boot on real data.
		mustExec(t, db, `INSERT INTO intents (intent_id, client_order_id, payload, created_at, resolved_at, resolution) VALUES ('i0', 'c0', NULL, 10, 50, 'filled')`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (1, 'i0', 'prepared', '', 11)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (2, 'i0', 'submit-attempted', '', 12)`)
		// A marker written in the same instant as the resolve is a tie, and ties read
		// as legal (the pre-check uses a strict >).
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (3, 'i0', 'acked', 'ord-0', 50)`)
	})

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (V3→V4 upgrade over legal data) must succeed: %v", err)
	}
	defer db.Close()

	if v, err := db.schemaVersion(ctx); err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d (err %v), want %d", v, err, len(migrations))
	}

	in := loadOne(t, db, "i1")
	if len(in.Markers) != 3 {
		t.Fatalf("markers after upgrade = %+v, want the 3 seeded rows preserved", in.Markers)
	}
	for i, want := range []struct {
		seq     int64
		kind    MarkerKind
		orderID string
	}{
		{7, MarkerPrepared, ""},
		{8, MarkerSubmitAttempted, ""},
		{9, MarkerAcked, "ord-1"},
	} {
		got := in.Markers[i]
		if got.Seq != want.seq || got.Kind != want.kind || got.OrderID != want.orderID {
			t.Fatalf("marker[%d] = {seq %d, kind %s, order %q}, want {seq %d, kind %s, order %q} — seq must survive the rebuild (audit ack keys reference it)",
				i, got.Seq, got.Kind, got.OrderID, want.seq, want.kind, want.orderID)
		}
	}

	// The pre-existing ack still resolves to its record after the rebuild.
	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	for _, r := range recs {
		if r.Key == "m:8" {
			t.Fatal("the pre-existing ack for m:8 no longer matches its record — seq/ack correspondence broke in the upgrade")
		}
	}

	// A new marker on the upgraded table follows the new AUTOINCREMENT sequence
	// (never reusing a seq an audit ack already refers to).
	if err := db.AppendIntent(ctx, Intent{IntentID: "i2", ClientOrderID: "c2"}); err != nil {
		t.Fatalf("AppendIntent after upgrade: %v", err)
	}
	if seq := loadOne(t, db, "i2").Markers[0].Seq; seq <= 9 {
		t.Fatalf("post-upgrade marker seq = %d, want > 9 (AUTOINCREMENT high-water mark lost in the rebuild)", seq)
	}
	// And the new constraints are live on the upgraded table.
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-2"); !errors.Is(err, ErrDuplicateMarker) {
		t.Fatalf("duplicate acked on upgraded table err = %v, want ErrDuplicateMarker", err)
	}
}

// TestUpgradeOverViolatingDataFailsClosed: a V3 database whose markers ALREADY
// violate the new invariants cannot be silently repaired — migrations are additive
// and never rewrite existing rows (ADR-0005). Deleting or coercing the offending
// rows would destroy exactly the evidence of a duplicate-submit incident. So Open
// fails closed with a diagnosable sentinel and the operator inspects the journal.
func TestUpgradeOverViolatingDataFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, db *sql.DB)
	}{
		{
			name: "duplicate submit-attempted",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'submit-attempted', '', 102)`)
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'submit-attempted', '', 103)`)
			},
		},
		{
			name: "acked without orderId",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'acked', '', 102)`)
			},
		},
		{
			name: "unknown kind",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'submitted', '', 102)`)
			},
		},
		{
			// Row-spanning rule: appendMarker refuses this going forward, so an
			// upgrade that admitted it would leave the database in a state the running
			// code declares impossible.
			name: "marker appended after the intent was terminally resolved",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `UPDATE intents SET resolved_at = 150, resolution = 'filled' WHERE intent_id = 'i1'`)
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'submit-attempted', '', 200)`)
			},
		},
		{
			// The other row-spanning rule: an acked marker with no submit-attempted
			// before it asserts an acknowledged order while the journal still says no
			// POST was ever attempted (ADR-0002).
			name: "acked with no preceding submit-attempted",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'acked', 'ord-1', 102)`)
			},
		},
		{
			// The predecessor EXISTS but was appended AFTER the marker it is supposed
			// to precede. seq is the durable append order that reconstruction replays
			// in, so this is still a progression appendMarker can never produce — a
			// mere existence check would import it unchallenged.
			name: "predecessor appended after the marker it must precede",
			seed: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (50, 'i1', 'acked', 'ord-1', 102)`)
				mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (51, 'i1', 'submit-attempted', '', 103)`)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			seedLegacyDB(t, path, preIntegrityVersion, func(db *sql.DB) {
				mustExec(t, db, `INSERT INTO intents (intent_id, client_order_id, payload, created_at) VALUES ('i1', 'c1', NULL, 100)`)
				mustExec(t, db, `INSERT INTO markers (intent_id, kind, order_id, at) VALUES ('i1', 'prepared', '', 101)`)
				tc.seed(t, db)
			})

			db, err := Open(path)
			if err == nil {
				db.Close()
				t.Fatal("Open succeeded over a journal that violates the new invariants; it must fail closed")
			}
			if !errors.Is(err, ErrMigrationDataViolation) {
				t.Fatalf("Open err = %v, want ErrMigrationDataViolation (an opaque driver error is not diagnosable)", err)
			}

			// The offending rows must still be there for the operator to inspect: the
			// failed migration rolled back and rewrote nothing.
			raw, oerr := openConn(path, false)
			if oerr != nil {
				t.Fatalf("reopen raw: %v", oerr)
			}
			defer raw.Close()
			var n int
			if err := raw.QueryRowContext(context.Background(), `SELECT count(*) FROM markers`).Scan(&n); err != nil {
				t.Fatalf("count markers: %v", err)
			}
			if n < 2 {
				t.Fatalf("markers left = %d, want the offending rows preserved for diagnosis (migrations never rewrite rows)", n)
			}
			var v int
			if err := raw.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v); err != nil {
				t.Fatalf("read user_version: %v", err)
			}
			if v != preIntegrityVersion {
				t.Fatalf("user_version = %d, want %d (a failed migration must not advance the version)", v, preIntegrityVersion)
			}
		})
	}
}

// TestUpgradeImportsPostTerminalMarkerWithNonLaterTimestamp pins a KNOWN,
// deliberate limitation rather than asserting desired behaviour: the
// post-terminal migration probe is best-effort, so a genuinely post-terminal
// marker whose `at` ties or predates the resolution (a backwards clock step, or
// two writes landing in the same nanosecond) is imported silently.
//
// It cannot be fixed by adding a durable resolution sequence: legacy rows carry no
// such value to reconstruct, so the evidence simply is not there. The blast radius
// is bounded and fail-safe, which is what this test actually verifies — the intent
// stays terminally resolved (it cannot re-enter the live set that gates
// submissions), and the extra reconstructed lifecycle record leaves the intent
// un-finalized so prune preserves it (#14).
//
// If a future change ever does make this provable, this test will fail and should
// be replaced by one asserting rejection.
func TestUpgradeImportsPostTerminalMarkerWithNonLaterTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	seedLegacyDB(t, path, preIntegrityVersion, func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO intents (intent_id, client_order_id, payload, created_at, resolved_at, resolution) VALUES ('i1', 'c1', NULL, 100, 150, 'filled')`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (1, 'i1', 'prepared', '', 101)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (2, 'i1', 'submit-attempted', '', 102)`)
		// Appended AFTER the resolution in reality (seq 3 > 2), but stamped at the
		// same instant as it — the probe's blind spot.
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (3, 'i1', 'acked', 'ord-1', 150)`)
	})

	db, err := Open(path)
	if err != nil {
		t.Fatalf("known limitation: this journal is expected to upgrade silently, got %v", err)
	}
	defer db.Close()

	// Bounded blast radius #1: the intent stays terminally resolved. A post-terminal
	// marker must never put it back in the live set that gates new submissions.
	live, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	for _, in := range live {
		if in.IntentID == "i1" {
			t.Fatal("an imported post-terminal marker resurrected a resolved intent into the live set")
		}
	}

	// Bounded blast radius #2: the stray marker adds a lifecycle record that is not
	// acked, so the intent never becomes fully audited and prune preserves it.
	if _, set, err := db.FullyAudited(ctx, "i1"); err != nil || set {
		t.Fatalf("FullyAudited = %v (err %v), want unset so prune preserves the intent", set, err)
	}
	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("expected the imported marker to leave un-acked lifecycle records (prune stays blocked)")
	}

	// And going forward the exact guard applies: appendMarker refuses to extend the
	// resolved intent further, timestamps irrelevant.
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); !errors.Is(err, ErrMarkerAfterTerminal) {
		t.Fatalf("post-upgrade append on the resolved intent err = %v, want ErrMarkerAfterTerminal", err)
	}
}

// TestUpgradeIsIdempotent: reopening an already-upgraded database is a no-op —
// the rebuild must not run twice (which would either fail or silently re-key seq).
func TestUpgradeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	seedLegacyDB(t, path, preIntegrityVersion, func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO intents (intent_id, client_order_id, payload, created_at) VALUES ('i1', 'c1', NULL, 100)`)
		mustExec(t, db, `INSERT INTO markers (seq, intent_id, kind, order_id, at) VALUES (3, 'i1', 'prepared', '', 101)`)
	})

	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		v, err := db.schemaVersion(ctx)
		if err != nil {
			t.Fatalf("schemaVersion: %v", err)
		}
		if v != len(migrations) {
			t.Fatalf("version after Open #%d = %d, want %d", i+1, v, len(migrations))
		}
		in := loadOne(t, db, "i1")
		if len(in.Markers) != 1 || in.Markers[0].Seq != 3 {
			t.Fatalf("markers after Open #%d = %+v, want the single seeded marker at seq 3", i+1, in.Markers)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

// --- helpers ---

func loadOne(t *testing.T, db *DB, intentID string) Intent {
	t.Helper()
	var in Intent
	if err := db.Atomically(context.Background(), func(tx Tx) error {
		var err error
		in, err = loadIntentByID(context.Background(), tx.(*txn).q, intentID)
		return err
	}); err != nil {
		t.Fatalf("load intent %q: %v", intentID, err)
	}
	return in
}

func assertMarkerKinds(t *testing.T, db *DB, intentID string, want ...MarkerKind) {
	t.Helper()
	in := loadOne(t, db, intentID)
	got := make([]MarkerKind, 0, len(in.Markers))
	for _, m := range in.Markers {
		got = append(got, m.Kind)
	}
	if len(got) != len(want) {
		t.Fatalf("markers for %q = %v, want %v", intentID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("markers for %q = %v, want %v", intentID, got, want)
		}
	}
}

func markerCount(t *testing.T, db *DB, intentID string) int {
	t.Helper()
	return len(loadOne(t, db, intentID).Markers)
}

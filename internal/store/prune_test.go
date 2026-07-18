package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The prune tests run against the real engine in a temp dir (openTemp), never an
// in-memory fake: prune is the ONLY deletion path in the repo, so the thing that
// must be verified is what the actual SQL deletes and leaves behind, including
// foreign-key behaviour and the V4 marker constraints (#29).

// --- seeding helpers -------------------------------------------------------

// seedUnresolvedIntent appends an intent that is still in flight: prepared plus
// (optionally) further markers, never resolved. This is the state ADR-0003's
// restart recovery depends on and that prune must never touch.
func seedUnresolvedIntent(t *testing.T, db *DB, id string, kinds ...MarkerKind) {
	t.Helper()
	ctx := context.Background()
	if err := db.AppendIntent(ctx, Intent{IntentID: id, ClientOrderID: "cli-" + id}); err != nil {
		t.Fatalf("AppendIntent %q: %v", id, err)
	}
	for _, k := range kinds {
		orderID := ""
		if k == MarkerAcked {
			orderID = "ord-" + id
		}
		if err := db.AppendMarker(ctx, id, k, orderID); err != nil {
			t.Fatalf("AppendMarker %s for %q: %v", k, id, err)
		}
	}
}

// seedFullyAuditedIntent drives one intent through the real happy path — markers,
// resolution, a durable ack for EVERY reconstructed lifecycle record, then
// FinalizeFullyAudited — and finally backdates both durable timestamps to at.
//
// It deliberately uses the production write paths rather than raw INSERTs so the
// rows it produces are exactly what the running bot produces (including the
// audit_acks ledger and the V4 marker constraints); only the two clock values are
// rewritten afterwards, because both are stamped with time.Now() inside store and
// a retention-window test cannot otherwise reach a boundary.
func seedFullyAuditedIntent(t *testing.T, db *DB, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	seedUnresolvedIntent(t, db, id, MarkerSubmitAttempted, MarkerAcked)
	if err := db.ResolveIntent(ctx, id, "filled"); err != nil {
		t.Fatalf("ResolveIntent %q: %v", id, err)
	}
	recs, err := db.UnackedLifecycleRecords(ctx, id)
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords %q: %v", id, err)
	}
	for _, rec := range recs {
		if err := db.RecordAuditAck(ctx, id, rec.Key); err != nil {
			t.Fatalf("RecordAuditAck %q/%q: %v", id, rec.Key, err)
		}
	}
	done, err := db.FinalizeFullyAudited(ctx, id)
	if err != nil {
		t.Fatalf("FinalizeFullyAudited %q: %v", id, err)
	}
	if !done {
		t.Fatalf("FinalizeFullyAudited %q = false, want true (seed did not reach the fully-audited state)", id)
	}
	setIntentTimes(t, db, id, &at, &at)
}

// setIntentTimes rewrites an intent's resolved_at / fully_audited_at directly. A
// nil value writes NULL. Tests use it to place a row on a chosen side of the
// retention boundary, and to FORCE states the production code refuses to create
// (e.g. a fully-audited flag on an unresolved intent) so that each guard in the
// prune predicate can be shown to be load-bearing on its own.
func setIntentTimes(t *testing.T, db *DB, id string, resolvedAt, fullyAuditedAt *time.Time) {
	t.Helper()
	var r, f any
	if resolvedAt != nil {
		r = resolvedAt.UnixNano()
	}
	if fullyAuditedAt != nil {
		f = fullyAuditedAt.UnixNano()
	}
	res, err := db.writeDB.ExecContext(context.Background(),
		`UPDATE intents SET resolved_at = ?, fully_audited_at = ? WHERE intent_id = ?`, r, f, id)
	if err != nil {
		t.Fatalf("backdate intent %q: %v", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("backdate intent %q rows: %v", id, err)
	}
	if n != 1 {
		t.Fatalf("backdate intent %q affected %d rows, want 1", id, n)
	}
}

// --- assertion helpers -----------------------------------------------------

func intentExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	var n int
	if err := db.readDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM intents WHERE intent_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count intent %q: %v", id, err)
	}
	return n > 0
}

func countRows(t *testing.T, db *DB, table, intentID string) int {
	t.Helper()
	var n int
	// table is a test-local literal, never user input.
	if err := db.readDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+table+` WHERE intent_id = ?`, intentID).Scan(&n); err != nil {
		t.Fatalf("count %s for %q: %v", table, intentID, err)
	}
	return n
}

// assertReferentialIntegrity fails if the database holds a dangling foreign key.
// A prune that deleted an intent while leaving its markers behind would show up
// here — and would also make a future migration's cross-row pre-check (V4, #29)
// refuse to upgrade.
func assertReferentialIntegrity(t *testing.T, db *DB) {
	t.Helper()
	rows, err := db.readDB.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		t.Fatalf("foreign_key_check reported a dangling reference after prune")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check iterate: %v", err)
	}
}

const testBatch = 100

// --- the load-bearing guards ----------------------------------------------

// TestPruneNeverDeletesUnresolvedIntent is the single most important test in this
// package: losing an unresolved intent breaks ADR-0003 restart recovery, and a bot
// that cannot rebuild an in-flight order can submit it a second time (irreversible).
//
// Each sub-case is deliberately given EVERY other prune qualification — an ancient
// timestamp, and even a forced fully-audited flag that FinalizeFullyAudited would
// never set on an unresolved intent — so the only thing standing between the row
// and deletion is the "terminal only" guard.
func TestPruneNeverDeletesUnresolvedIntent(t *testing.T) {
	ancient := time.Now().Add(-365 * 24 * time.Hour)

	cases := []struct {
		name    string
		markers []MarkerKind
	}{
		{"prepared-only", nil},
		{"submit-attempted (POST may have happened)", []MarkerKind{MarkerSubmitAttempted}},
		{"acked but not yet resolved", []MarkerKind{MarkerSubmitAttempted, MarkerAcked}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTemp(t)
			seedUnresolvedIntent(t, db, "live", tc.markers...)
			// Force the prune-gate flag on despite the intent being unresolved. This
			// state is unreachable through FinalizeFullyAudited; it exists here only to
			// prove the resolved_at guard protects the row all by itself.
			setIntentTimes(t, db, "live", nil, &ancient)

			stats, err := db.PruneTerminalIntents(context.Background(), time.Now(), testBatch)
			if err != nil {
				t.Fatalf("PruneTerminalIntents: %v", err)
			}
			if stats.Intents != 0 {
				t.Fatalf("pruned %d intents, want 0 — an unresolved intent must NEVER be pruned (ADR-0005 point 6)", stats.Intents)
			}
			if !intentExists(t, db, "live") {
				t.Fatal("unresolved intent was deleted — restart recovery (ADR-0003) is broken")
			}
			if got := countRows(t, db, "markers", "live"); got != len(tc.markers)+1 {
				t.Fatalf("markers for the unresolved intent = %d, want %d", got, len(tc.markers)+1)
			}

			// The reconciler's restart scan must still see it.
			live, err := db.LoadUnresolvedIntents(context.Background())
			if err != nil {
				t.Fatalf("LoadUnresolvedIntents: %v", err)
			}
			if len(live) != 1 || live[0].IntentID != "live" {
				t.Fatalf("LoadUnresolvedIntents after prune = %v, want the still-open intent", live)
			}
		})
	}
}

// TestPruneKeepsTerminalWithoutFullyAuditedFlag is the fail-safe direction of
// ADR-0005 point 6 / ADR-0006 point 4: an intent that is terminal and long past
// the retention window is STILL preserved while its prune-gate flag is unset,
// because that flag is the only evidence that its lifecycle audit records reached
// durable storage. Deleting it would destroy the sole durable outbox for a
// money-moving action.
func TestPruneKeepsTerminalWithoutFullyAuditedFlag(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	ancient := time.Now().Add(-365 * 24 * time.Hour)

	seedFullyAuditedIntent(t, db, "orphan", ancient)
	// Clear the flag: terminal, ancient, but its audit was never durably acked —
	// exactly the crash tail ADR-0006 point 4 describes.
	setIntentTimes(t, db, "orphan", &ancient, nil)

	stats, err := db.PruneTerminalIntents(ctx, time.Now(), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 0 {
		t.Fatalf("pruned %d intents, want 0 — a terminal intent with no fully-audited flag must be PRESERVED", stats.Intents)
	}
	if !intentExists(t, db, "orphan") {
		t.Fatal("terminal intent without the fully-audited flag was deleted (audit evidence lost)")
	}

	// It must also remain discoverable so the reconciler can re-emit its audit.
	cands, err := db.LoadNotFullyAuditedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadNotFullyAuditedIntents: %v", err)
	}
	if len(cands) != 1 || cands[0].IntentID != "orphan" {
		t.Fatalf("LoadNotFullyAuditedIntents after prune = %v, want the preserved orphan", cands)
	}
}

// TestPruneDeletesFullyAuditedTerminalPastWindow is the happy path: only when an
// intent is terminal AND fully audited AND past the retention window does it go,
// and then it goes completely (intent + markers + ack ledger), leaving no dangling
// reference behind.
func TestPruneDeletesFullyAuditedTerminalPastWindow(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	seedFullyAuditedIntent(t, db, "done", old)
	if got := countRows(t, db, "markers", "done"); got != 3 {
		t.Fatalf("seeded markers = %d, want 3", got)
	}
	if got := countRows(t, db, "audit_acks", "done"); got != 4 {
		t.Fatalf("seeded audit acks = %d, want 4 (3 markers + terminal)", got)
	}

	stats, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 1 || stats.Markers != 3 || stats.AuditAcks != 4 {
		t.Fatalf("stats = %+v, want {Intents:1 Markers:3 AuditAcks:4}", stats)
	}
	if intentExists(t, db, "done") {
		t.Fatal("eligible intent was not pruned")
	}
	if got := countRows(t, db, "markers", "done"); got != 0 {
		t.Fatalf("markers left behind after prune = %d, want 0", got)
	}
	if got := countRows(t, db, "audit_acks", "done"); got != 0 {
		t.Fatalf("audit acks left behind after prune = %d, want 0", got)
	}
	assertReferentialIntegrity(t, db)
}

// TestPruneRetentionWindowBoundary pins both sides of the boundary. The window is
// inclusive at the cutoff ("the window has elapsed") and anything newer by even a
// nanosecond is preserved.
func TestPruneRetentionWindowBoundary(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour).Truncate(time.Microsecond)

	t.Run("exactly at the cutoff is pruned", func(t *testing.T) {
		db := openTemp(t)
		seedFullyAuditedIntent(t, db, "at-cutoff", cutoff)
		stats, err := db.PruneTerminalIntents(context.Background(), cutoff, testBatch)
		if err != nil {
			t.Fatalf("PruneTerminalIntents: %v", err)
		}
		if stats.Intents != 1 || intentExists(t, db, "at-cutoff") {
			t.Fatalf("intent resolved exactly at the cutoff was not pruned (stats %+v)", stats)
		}
	})

	t.Run("one nanosecond inside the window is preserved", func(t *testing.T) {
		db := openTemp(t)
		seedFullyAuditedIntent(t, db, "just-inside", cutoff.Add(time.Nanosecond))
		stats, err := db.PruneTerminalIntents(context.Background(), cutoff, testBatch)
		if err != nil {
			t.Fatalf("PruneTerminalIntents: %v", err)
		}
		if stats.Intents != 0 || !intentExists(t, db, "just-inside") {
			t.Fatalf("intent still inside the retention window was pruned (stats %+v)", stats)
		}
	})
}

// TestPruneWindowAppliesToTheFullyAuditedTimestampToo covers the concurrency-shaped
// half of the window. An intent can be resolved long ago and only become fully
// audited now — a restart reconciler re-emitting a crash tail does exactly that.
// If the window were measured on resolved_at alone, prune could delete such a row
// the instant the reconciler flagged it, while that reconciler is still working
// through the same intent. Requiring BOTH timestamps to be older than the cutoff
// gives every freshly finalized intent the full window before it becomes eligible.
func TestPruneWindowAppliesToTheFullyAuditedTimestampToo(t *testing.T) {
	db := openTemp(t)
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	justNow := time.Now()

	seedFullyAuditedIntent(t, db, "late-finalize", ancient)
	setIntentTimes(t, db, "late-finalize", &ancient, &justNow)

	stats, err := db.PruneTerminalIntents(context.Background(), time.Now().Add(-time.Hour), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 0 || !intentExists(t, db, "late-finalize") {
		t.Fatalf("a just-finalized intent was pruned before its retention window elapsed (stats %+v)", stats)
	}
}

// TestPruneWindowAppliesToTheResolvedTimestampToo covers the mirror image, and it
// is the one case the rest of the suite cannot reach on its own.
//
// In every state store can produce, fully_audited_at >= resolved_at (finalize
// refuses to run before resolution), so the window on fully_audited_at normally
// implies the window on resolved_at and the latter looks redundant. It stops being
// redundant the moment the wall clock steps BACKWARDS between the two writes — an
// NTP correction between ResolveIntent and FinalizeFullyAudited is enough — because
// then a row can carry an old fully_audited_at beside a resolution that is still
// well inside the retention window. Checking only the flag timestamp would delete
// it early; checking both keeps the window honest under a clock this code does not
// control. A hand-edited or restored row can produce the same shape.
//
// (Mutation-checked: removing the resolved_at window conjunct turns this red.)
func TestPruneWindowAppliesToTheResolvedTimestampToo(t *testing.T) {
	db := openTemp(t)
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	justNow := time.Now()

	seedFullyAuditedIntent(t, db, "clock-stepped-back", ancient)
	// resolved just now, but flagged with a pre-step (older) clock reading.
	setIntentTimes(t, db, "clock-stepped-back", &justNow, &ancient)

	stats, err := db.PruneTerminalIntents(context.Background(), time.Now().Add(-time.Hour), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 0 || !intentExists(t, db, "clock-stepped-back") {
		t.Fatalf("an intent resolved inside the retention window was pruned because its flag timestamp predated it (stats %+v)", stats)
	}
}

// TestPruneLeavesHaltCountersAndLifecycleAlone is the "these are never prune
// targets" guard from ADR-0005 point 6. The halt state and the persistent counters
// are reconstruction-resistant: deleting them would turn a restart into a
// safety-guard bypass. They live in tables prune never names, and this test pins
// that structurally by pruning with an eligible intent present.
func TestPruneLeavesHaltCountersAndLifecycleAlone(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	if err := db.TripHalt(ctx, "test-halt"); err != nil {
		t.Fatalf("TripHalt: %v", err)
	}
	if err := db.SetCounter(ctx, Counter{Name: "order-failures", Value: 7}); err != nil {
		t.Fatalf("SetCounter: %v", err)
	}
	if err := db.SetLifecycle(ctx, LifecycleClean); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	seedFullyAuditedIntent(t, db, "done", old)

	stats, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 1 {
		t.Fatalf("pruned %d intents, want 1 (the eligible one)", stats.Intents)
	}

	halt, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if halt.Phase != HaltHalted || halt.Reason != "test-halt" {
		t.Fatalf("halt after prune = %+v, want the tripped halt intact (ADR-0005 point 6)", halt)
	}
	c, err := db.Counter(ctx, "order-failures")
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 7 {
		t.Fatalf("counter after prune = %d, want 7 (persistent counters are never pruned)", c.Value)
	}
	lc, err := db.Lifecycle(ctx)
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	if lc != LifecycleClean {
		t.Fatalf("lifecycle sentinel after prune = %q, want %q", lc, LifecycleClean)
	}
}

// TestPruneTouchesOnlyTheEligibleIntentsRows guards against collateral damage: a
// prune pass must not remove one marker or one ack belonging to an intent it is
// preserving. A partially-deleted marker set would both destroy journal evidence
// and leave a state the V4 cross-row pre-check (#29) refuses to migrate.
func TestPruneTouchesOnlyTheEligibleIntentsRows(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	seedFullyAuditedIntent(t, db, "eligible", old)
	seedFullyAuditedIntent(t, db, "recent", time.Now())
	seedUnresolvedIntent(t, db, "live", MarkerSubmitAttempted)

	if _, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), testBatch); err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}

	if intentExists(t, db, "eligible") {
		t.Fatal("eligible intent survived")
	}
	for _, id := range []string{"recent", "live"} {
		if !intentExists(t, db, id) {
			t.Fatalf("preserved intent %q was deleted", id)
		}
	}
	if got := countRows(t, db, "markers", "recent"); got != 3 {
		t.Fatalf("markers for the preserved intent %q = %d, want 3 (no partial deletion)", "recent", got)
	}
	if got := countRows(t, db, "audit_acks", "recent"); got != 4 {
		t.Fatalf("audit acks for the preserved intent %q = %d, want 4", "recent", got)
	}
	if got := countRows(t, db, "markers", "live"); got != 2 {
		t.Fatalf("markers for the unresolved intent = %d, want 2", got)
	}
	assertReferentialIntegrity(t, db)
}

// TestPruneBatchLimitBoundsOnePass keeps a single pass bounded so a large backlog
// cannot hold the sole write connection for an unbounded time. The oldest
// resolutions go first, and the remainder is picked up by the next pass.
func TestPruneBatchLimitBoundsOnePass(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	base := time.Now().Add(-72 * time.Hour)

	seedFullyAuditedIntent(t, db, "a", base)
	seedFullyAuditedIntent(t, db, "b", base.Add(time.Minute))
	seedFullyAuditedIntent(t, db, "c", base.Add(2*time.Minute))

	stats, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), 2)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 2 {
		t.Fatalf("pruned %d intents in one pass, want 2 (the batch limit)", stats.Intents)
	}
	if intentExists(t, db, "a") || intentExists(t, db, "b") {
		t.Fatal("the two oldest intents should have been pruned first")
	}
	if !intentExists(t, db, "c") {
		t.Fatal("the newest intent should have been left for the next pass")
	}

	stats, err = db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), 2)
	if err != nil {
		t.Fatalf("PruneTerminalIntents (second pass): %v", err)
	}
	if stats.Intents != 1 || intentExists(t, db, "c") {
		t.Fatalf("second pass stats = %+v, want the remaining intent pruned", stats)
	}
}

// TestPruneRejectsNonPositiveBatch is fail-closed configuration: a zero or negative
// batch would turn a prune pass into a silent no-op (unbounded growth → disk full →
// the very ambiguous-submit hazard ADR-0005 point 6 exists to prevent), so it is an
// error rather than an accepted degenerate value.
func TestPruneRejectsNonPositiveBatch(t *testing.T) {
	db := openTemp(t)
	for _, limit := range []int{0, -1} {
		if _, err := db.PruneTerminalIntents(context.Background(), time.Now(), limit); err == nil {
			t.Fatalf("PruneTerminalIntents(limit=%d) err = nil, want a fail-closed error", limit)
		}
	}
}

// TestPruneCandidateSelectionUsesTheIndex is an availability guard, and it is the
// reason schemaV5 exists.
//
// The prune pass opens the single write transaction and only THEN selects its
// candidates. Without an index matching the predicate and the ordering, that
// selection is a full scan of intents plus a temp-B-tree sort — while holding the
// one connection every writer shares. The batch limit bounds how many rows are
// deleted, but it does not bound the scan, so on a large journal a retention pass
// would stall AppendMarker, and a stalled submit-attempted append is exactly the
// hazard the retention loop exists to prevent. Growing backlogs are also precisely
// when prune runs hardest, so the failure mode compounds itself.
//
// Reading the planner's output is the only way to assert this: a functional test
// passes just as happily against a full scan.
func TestPruneCandidateSelectionUsesTheIndex(t *testing.T) {
	db := openTemp(t)
	cutoff := time.Now().UnixNano()

	rows, err := db.readDB.QueryContext(context.Background(),
		`EXPLAIN QUERY PLAN `+pruneCandidateQuery, cutoff, testBatch)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plan string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}

	if !strings.Contains(plan, pruneIndexName) {
		t.Fatalf("prune candidate selection does not use %s — it scans the journal under the write lock.\nplan:\n%s",
			pruneIndexName, plan)
	}
	// Using the index is NOT enough, and this is the subtle half. If the index led on
	// plain resolved_at, the planner would still report "USING INDEX" while bounding
	// on resolved_at alone and then walking every older row to test the second cutoff
	// — so a backlog of old-resolved-but-recently-finalized rows (what a reconciler
	// draining a crash tail produces) would be an unbounded prefix examined on every
	// pass. SQLite reports a real range constraint as a bound in parentheses, e.g.
	// "(<expr><?)"; its absence means the traversal is not bounded by the cutoff.
	if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, "<?") {
		t.Fatalf("prune candidate selection is not range-bounded by the retention cutoff; "+
			"it walks the index until LIMIT is satisfied, under the write lock.\nplan:\n%s", plan)
	}
	// A temp B-tree means the planner sorted the rows itself, i.e. it had to visit
	// every candidate before the LIMIT could apply.
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("prune candidate selection sorts in a temp B-tree; the index no longer serves the ORDER BY.\nplan:\n%s", plan)
	}
}

// TestPruneSkipsRecentlyFinalizedBacklogCorrectly is the functional companion to the
// query-plan test, in the shape the adversarial review called out: a large backlog of
// intents resolved long ago but finalized moments ago (what a reconciler draining a
// crash tail produces), sitting ahead of a few genuinely eligible rows in resolution
// order. The pass must return only the eligible rows.
func TestPruneSkipsRecentlyFinalizedBacklogCorrectly(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	justFinalized := time.Now()

	// Ancient resolutions, finalized just now ⇒ NOT eligible, and they sort FIRST by
	// resolved_at — the ordering a naive index would traverse.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("backlog-%02d", i)
		seedFullyAuditedIntent(t, db, id, ancient)
		setIntentTimes(t, db, id, &ancient, &justFinalized)
	}
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 3; i++ {
		seedFullyAuditedIntent(t, db, fmt.Sprintf("eligible-%d", i), old)
	}

	stats, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), testBatch)
	if err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}
	if stats.Intents != 3 {
		t.Fatalf("pruned %d intents, want exactly the 3 past-window rows", stats.Intents)
	}
	for i := 0; i < 50; i++ {
		if !intentExists(t, db, fmt.Sprintf("backlog-%02d", i)) {
			t.Fatal("a recently-finalized intent was pruned inside its retention window")
		}
	}
	assertReferentialIntegrity(t, db)
}

// TestPruneKeepsMarkerSeqMonotonic pins an interaction with #29/#20 that is easy to
// break: marker seq is AUTOINCREMENT and audit_acks.record_key embeds it ("m:<seq>").
// If deleting markers let SQLite reuse a seq, a later intent's marker could collide
// with an ack key. AUTOINCREMENT's high-water mark prevents that, and this test
// keeps it from silently regressing (e.g. if someone ever rebuilds the table).
func TestPruneKeepsMarkerSeqMonotonic(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	seedFullyAuditedIntent(t, db, "old", time.Now().Add(-48*time.Hour))
	var maxSeq int64
	if err := db.readDB.QueryRowContext(ctx, `SELECT MAX(seq) FROM markers`).Scan(&maxSeq); err != nil {
		t.Fatalf("read max seq: %v", err)
	}

	if _, err := db.PruneTerminalIntents(ctx, time.Now().Add(-time.Hour), testBatch); err != nil {
		t.Fatalf("PruneTerminalIntents: %v", err)
	}

	seedUnresolvedIntent(t, db, "fresh")
	markers, err := loadMarkers(ctx, db.readDB, "fresh")
	if err != nil {
		t.Fatalf("loadMarkers: %v", err)
	}
	if len(markers) != 1 {
		t.Fatalf("markers for the fresh intent = %d, want 1", len(markers))
	}
	if markers[0].Seq <= maxSeq {
		t.Fatalf("marker seq %d reused a pruned seq (max was %d) — ack keys would collide", markers[0].Seq, maxSeq)
	}
}

// TestPruneUnderConcurrentWrites runs prune against the live write path under -race.
// prune must serialize through the same single write connection as every other
// writer (ADR-0005), and no in-flight intent may be lost while it does.
func TestPruneUnderConcurrentWrites(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, 3*n)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			id := "live-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			if err := db.AppendIntent(ctx, Intent{IntentID: id, ClientOrderID: "cli"}); err != nil {
				errs <- err
				return
			}
			if err := db.AppendMarker(ctx, id, MarkerSubmitAttempted, ""); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := db.PruneTerminalIntents(ctx, time.Now(), testBatch); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent prune/write: %v", err)
	}

	live, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	if len(live) != n {
		t.Fatalf("unresolved intents after concurrent prune = %d, want %d (prune ate an in-flight intent)", len(live), n)
	}
	assertReferentialIntegrity(t, db)
}

// --- the loop --------------------------------------------------------------

// fakePruneJournal drives the loop's own behaviour (cadence, recover boundary,
// error accounting) without the engine. The DELETE semantics are covered above
// against the real engine; this is about the goroutine that calls it.
type fakePruneJournal struct {
	mu     sync.Mutex
	calls  []time.Time
	err    error
	panics bool
}

func (f *fakePruneJournal) PruneTerminalIntents(_ context.Context, before time.Time, _ int) (PruneStats, error) {
	f.mu.Lock()
	f.calls = append(f.calls, before)
	shouldPanic, err := f.panics, f.err
	f.mu.Unlock()
	if shouldPanic {
		panic("boom")
	}
	return PruneStats{}, err
}

func (f *fakePruneJournal) snapshot() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls...)
}

func TestNewPrunerValidatesFailClosed(t *testing.T) {
	base := PruneConfig{
		Journal:         &fakePruneJournal{},
		RetentionWindow: time.Hour,
		Interval:        time.Minute,
		MaxBatch:        10,
	}
	cases := []struct {
		name  string
		mutit func(*PruneConfig)
	}{
		{"nil journal", func(c *PruneConfig) { c.Journal = nil }},
		// A zero retention window would make an intent eligible the moment it is
		// finalized, erasing the grace period that protects a concurrently-working
		// reconciler. It is a fail-open default, so it is refused.
		{"zero retention window", func(c *PruneConfig) { c.RetentionWindow = 0 }},
		{"negative retention window", func(c *PruneConfig) { c.RetentionWindow = -time.Hour }},
		{"zero interval", func(c *PruneConfig) { c.Interval = 0 }},
		{"zero batch", func(c *PruneConfig) { c.MaxBatch = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutit(&cfg)
			if _, err := NewPruner(cfg); err == nil {
				t.Fatal("NewPruner err = nil, want a fail-closed validation error")
			}
		})
	}
	if _, err := NewPruner(base); err != nil {
		t.Fatalf("NewPruner on a valid config: %v", err)
	}
}

// TestPrunerPassesTheRetentionCutoff checks the one arithmetic the loop owns:
// the cutoff handed to the journal is now minus the retention window.
func TestPrunerPassesTheRetentionCutoff(t *testing.T) {
	fake := &fakePruneJournal{}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p, err := NewPruner(PruneConfig{
		Journal:         fake,
		RetentionWindow: 24 * time.Hour,
		Interval:        time.Minute,
		MaxBatch:        10,
		Now:             func() time.Time { return now },
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewPruner: %v", err)
	}
	if _, err := p.PruneOnce(context.Background()); err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("journal calls = %d, want 1", len(calls))
	}
	if want := now.Add(-24 * time.Hour); !calls[0].Equal(want) {
		t.Fatalf("cutoff = %s, want %s (now - retention window)", calls[0], want)
	}
}

// TestPrunerRunPrunesOnEveryTick pins the cadence: the loop prunes once at start
// and once per tick, and returns when the context is cancelled.
func TestPrunerRunPrunesOnEveryTick(t *testing.T) {
	fake := &fakePruneJournal{}
	ticks := make(chan time.Time)
	p, err := NewPruner(PruneConfig{
		Journal:         fake,
		RetentionWindow: time.Hour,
		Interval:        time.Minute,
		MaxBatch:        10,
		Ticks:           ticks,
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewPruner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	ticks <- time.Now()
	ticks <- time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if n := len(fake.snapshot()); n < 3 {
		t.Fatalf("prune passes = %d, want at least 3 (one at start + one per tick)", n)
	}
}

// TestPrunerRunSurvivesFailureAndPanic is the unattended-operation guard: prune is
// housekeeping, so neither an error nor a panic inside a pass may kill the loop or
// the process. The safe direction of a failed prune is simply "nothing was
// deleted", so the loop logs and keeps its cadence.
func TestPrunerRunSurvivesFailureAndPanic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakePruneJournal)
	}{
		{"error", func(f *fakePruneJournal) { f.err = errors.New("disk on fire") }},
		{"panic", func(f *fakePruneJournal) { f.panics = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePruneJournal{}
			tc.setup(fake)
			ticks := make(chan time.Time)
			p, err := NewPruner(PruneConfig{
				Journal:         fake,
				RetentionWindow: time.Hour,
				Interval:        time.Minute,
				MaxBatch:        10,
				Ticks:           ticks,
				Logger:          slog.New(slog.DiscardHandler),
			})
			if err != nil {
				t.Fatalf("NewPruner: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- p.Run(ctx) }()

			ticks <- time.Now()
			ticks <- time.Now()
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run returned %v, want context.Canceled (the loop must survive %s)", err, tc.name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("Run died on a pass that returned a %s", tc.name)
			}
			if n := len(fake.snapshot()); n < 3 {
				t.Fatalf("prune passes = %d, want at least 3 — the loop stopped retrying after a %s", n, tc.name)
			}
		})
	}
}

// TestPrunerRunEndToEnd wires the real loop to the real engine once, so the
// composition (cutoff arithmetic → SQL predicate → deletion) is verified together
// and not only in its halves.
func TestPrunerRunEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seedFullyAuditedIntent(t, db, "old", time.Now().Add(-48*time.Hour))
	seedFullyAuditedIntent(t, db, "new", time.Now())
	seedUnresolvedIntent(t, db, "live")

	p, err := NewPruner(PruneConfig{
		Journal:         db,
		RetentionWindow: 24 * time.Hour,
		Interval:        time.Minute,
		MaxBatch:        testBatch,
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewPruner: %v", err)
	}
	stats, err := p.PruneOnce(context.Background())
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if stats.Intents != 1 {
		t.Fatalf("pruned %d intents, want 1", stats.Intents)
	}
	if intentExists(t, db, "old") {
		t.Fatal("the aged, fully-audited intent was not pruned")
	}
	if !intentExists(t, db, "new") || !intentExists(t, db, "live") {
		t.Fatal("prune deleted a protected intent")
	}
	assertReferentialIntegrity(t, db)
}

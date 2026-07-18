package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// seedResolvedIntent builds a fully-progressed, resolved intent: prepared (from
// AppendIntent) → submit-attempted → acked(orderID) → resolved(resolution). Its
// lifecycle audit set is therefore 3 markers + 1 terminal = 4 records.
func seedResolvedIntent(t *testing.T, db *DB, ctx context.Context, id, orderID, resolution string) {
	t.Helper()
	if err := db.AppendIntent(ctx, Intent{IntentID: id, ClientOrderID: "c-" + id}); err != nil {
		t.Fatalf("AppendIntent %s: %v", id, err)
	}
	if err := db.AppendMarker(ctx, id, MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("AppendMarker submit-attempted %s: %v", id, err)
	}
	if err := db.AppendMarker(ctx, id, MarkerAcked, orderID); err != nil {
		t.Fatalf("AppendMarker acked %s: %v", id, err)
	}
	if err := db.ResolveIntent(ctx, id, resolution); err != nil {
		t.Fatalf("ResolveIntent %s: %v", id, err)
	}
}

func keyByMarker(recs []LifecycleRecord, marker string) (string, bool) {
	for _, r := range recs {
		if r.Marker == marker {
			return r.Key, true
		}
	}
	return "", false
}

func terminalKey(recs []LifecycleRecord) (string, bool) {
	for _, r := range recs {
		if r.Terminal {
			return r.Key, true
		}
	}
	return "", false
}

// TestReconstructLifecycleRecordsDeterministic pins the pure reconstruction: a
// fully-progressed intent yields 4 lifecycle records in journal order with the
// ADR-0006 orderId key-reuse (empty before acked, the acquired orderId at/after
// acked), distinct keys, and a stable result across repeated calls.
func TestReconstructLifecycleRecordsDeterministic(t *testing.T) {
	now := time.Unix(0, 1_700_000_000_000_000_000)
	resolved := now.Add(3 * time.Second)
	in := Intent{
		IntentID:   "i1",
		Resolution: "FILLED",
		ResolvedAt: &resolved,
		Markers: []Marker{
			{Seq: 1, Kind: MarkerPrepared, At: now},
			{Seq: 2, Kind: MarkerSubmitAttempted, At: now.Add(time.Second)},
			{Seq: 3, Kind: MarkerAcked, OrderID: "ord-1", At: now.Add(2 * time.Second)},
		},
	}

	recs := ReconstructLifecycleRecords(in)
	if len(recs) != 4 {
		t.Fatalf("got %d lifecycle records, want 4: %+v", len(recs), recs)
	}

	want := []struct {
		marker   string
		orderID  string
		terminal bool
	}{
		{"prepared", "", false},
		{"submit-attempted", "", false},
		{"acked", "ord-1", false},
		{"FILLED", "ord-1", true},
	}
	seenKeys := map[string]bool{}
	for i, w := range want {
		if recs[i].Marker != w.marker {
			t.Errorf("rec[%d].Marker = %q, want %q", i, recs[i].Marker, w.marker)
		}
		if recs[i].OrderID != w.orderID {
			t.Errorf("rec[%d].OrderID = %q, want %q (ADR-0006 orderId key reuse)", i, recs[i].OrderID, w.orderID)
		}
		if recs[i].Terminal != w.terminal {
			t.Errorf("rec[%d].Terminal = %v, want %v", i, recs[i].Terminal, w.terminal)
		}
		if recs[i].IntentID != "i1" {
			t.Errorf("rec[%d].IntentID = %q, want i1", i, recs[i].IntentID)
		}
		if recs[i].Key == "" {
			t.Errorf("rec[%d] has empty Key", i)
		}
		if seenKeys[recs[i].Key] {
			t.Errorf("rec[%d] duplicate Key %q", i, recs[i].Key)
		}
		seenKeys[recs[i].Key] = true
	}

	// Deterministic: a second call yields an identical shape (same keys/order).
	again := ReconstructLifecycleRecords(in)
	if len(again) != len(recs) {
		t.Fatalf("second call len = %d, want %d", len(again), len(recs))
	}
	for i := range recs {
		if again[i].Key != recs[i].Key || again[i].Marker != recs[i].Marker {
			t.Fatalf("non-deterministic reconstruction at %d: %+v vs %+v", i, again[i], recs[i])
		}
	}
}

// TestReconstructPreparedOnlyAborted covers an aborted-before-submit intent: only
// the prepared marker exists, no orderId was ever acquired, so the terminal record
// keys on the intent (empty orderId).
func TestReconstructPreparedOnlyAborted(t *testing.T) {
	resolved := time.Unix(0, 2_000_000_000_000_000_000)
	in := Intent{
		IntentID:   "i2",
		Resolution: "aborted-before-submit",
		ResolvedAt: &resolved,
		Markers:    []Marker{{Seq: 5, Kind: MarkerPrepared, At: resolved.Add(-time.Second)}},
	}
	recs := ReconstructLifecycleRecords(in)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (prepared + terminal): %+v", len(recs), recs)
	}
	if recs[0].Marker != "prepared" || recs[0].OrderID != "" {
		t.Errorf("rec[0] = %+v, want prepared/empty-order", recs[0])
	}
	if !recs[1].Terminal || recs[1].Marker != "aborted-before-submit" || recs[1].OrderID != "" {
		t.Errorf("rec[1] = %+v, want terminal aborted-before-submit/empty-order", recs[1])
	}
}

// TestReconstructUnresolvedHasNoTerminal: an unresolved intent has no terminal
// lifecycle record yet, so reconstruction yields only its marker records.
func TestReconstructUnresolvedHasNoTerminal(t *testing.T) {
	in := Intent{
		IntentID: "i3",
		Markers: []Marker{
			{Seq: 1, Kind: MarkerPrepared, At: time.Unix(0, 1)},
			{Seq: 2, Kind: MarkerSubmitAttempted, At: time.Unix(0, 2)},
		},
	}
	recs := ReconstructLifecycleRecords(in)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (no terminal for unresolved): %+v", len(recs), recs)
	}
	for _, r := range recs {
		if r.Terminal {
			t.Errorf("unresolved intent produced a terminal record: %+v", r)
		}
	}
}

// TestFlagNotSetUntilAllLifecycleRecordsAcked is AC-1: the fully-audited flag is
// not set until EVERY lifecycle audit record is durably acked; it flips exactly on
// the last one.
func TestFlagNotSetUntilAllLifecycleRecordsAcked(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "FILLED")

	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("un-acked records = %d, want 4 (nothing acked yet)", len(recs))
	}

	for i, rec := range recs {
		if err := db.RecordAuditAck(ctx, "i1", rec.Key); err != nil {
			t.Fatalf("RecordAuditAck %q: %v", rec.Key, err)
		}
		done, err := db.FinalizeFullyAudited(ctx, "i1")
		if err != nil {
			t.Fatalf("FinalizeFullyAudited: %v", err)
		}
		last := i == len(recs)-1
		if done != last {
			t.Fatalf("after acking %d/%d records, fully-audited = %v, want %v", i+1, len(recs), done, last)
		}
		_, set, err := db.FullyAudited(ctx, "i1")
		if err != nil {
			t.Fatalf("FullyAudited: %v", err)
		}
		if set != last {
			t.Fatalf("after acking %d/%d records, flag set = %v, want %v", i+1, len(recs), set, last)
		}
	}
}

// TestTerminalAckAloneDoesNotSetFlag is AC-2 / ADR-0006 point 4's core: if an
// intermediate marker audit was lost, the terminal ack alone must NOT set the
// flag — otherwise prune could delete the journal that is the only outbox for the
// lost intermediate lifecycle audit.
func TestTerminalAckAloneDoesNotSetFlag(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "FILLED")

	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	tKey, ok := terminalKey(recs)
	if !ok {
		t.Fatalf("no terminal record among %+v", recs)
	}

	// Only the terminal is acked; the three marker records are "lost".
	if err := db.RecordAuditAck(ctx, "i1", tKey); err != nil {
		t.Fatalf("RecordAuditAck terminal: %v", err)
	}
	done, err := db.FinalizeFullyAudited(ctx, "i1")
	if err != nil {
		t.Fatalf("FinalizeFullyAudited: %v", err)
	}
	if done {
		t.Fatal("terminal-only ack set the flag — ADR-0006 point 4 forbids terminal-alone gating")
	}
	if _, set, _ := db.FullyAudited(ctx, "i1"); set {
		t.Fatal("flag set with only terminal acked")
	}

	// Now ack every record EXCEPT the prepared marker: still not fully audited.
	preparedKey, _ := keyByMarker(recs, "prepared")
	for _, rec := range recs {
		if rec.Key == preparedKey {
			continue
		}
		if err := db.RecordAuditAck(ctx, "i1", rec.Key); err != nil {
			t.Fatalf("RecordAuditAck %q: %v", rec.Key, err)
		}
	}
	done, err = db.FinalizeFullyAudited(ctx, "i1")
	if err != nil {
		t.Fatalf("FinalizeFullyAudited: %v", err)
	}
	if done {
		t.Fatal("flag set while the prepared-marker audit is still un-acked (lost intermediate marker)")
	}
}

// TestUnresolvedIntentNeverFullyAudited: even when every existing marker record is
// acked, an unresolved intent cannot be fully audited — its terminal lifecycle
// record does not exist yet, so prune must preserve it.
func TestUnresolvedIntentNeverFullyAudited(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("AppendMarker: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-1"); err != nil {
		t.Fatalf("AppendMarker: %v", err)
	}

	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("un-acked = %d, want 3 markers (no terminal while unresolved)", len(recs))
	}
	for _, rec := range recs {
		if rec.Terminal {
			t.Fatalf("unresolved intent produced terminal record %+v", rec)
		}
		if err := db.RecordAuditAck(ctx, "i1", rec.Key); err != nil {
			t.Fatalf("RecordAuditAck %q: %v", rec.Key, err)
		}
	}
	done, err := db.FinalizeFullyAudited(ctx, "i1")
	if err != nil {
		t.Fatalf("FinalizeFullyAudited: %v", err)
	}
	if done {
		t.Fatal("unresolved intent became fully audited — terminal record must gate it")
	}
	if _, set, _ := db.FullyAudited(ctx, "i1"); set {
		t.Fatal("flag set on unresolved intent")
	}
}

// TestUnackedLifecycleRecordsShrinksAsAcked is AC-4: the reconstruction of un-acked
// records is deterministic and shrinks precisely as acks are recorded.
func TestUnackedLifecycleRecordsShrinksAsAcked(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "CANCELED")

	all, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("initial un-acked = %d, want 4", len(all))
	}

	// Ack the first two; the un-acked set must be exactly the remaining two, in the
	// same deterministic order.
	if err := db.RecordAuditAck(ctx, "i1", all[0].Key); err != nil {
		t.Fatalf("RecordAuditAck: %v", err)
	}
	if err := db.RecordAuditAck(ctx, "i1", all[1].Key); err != nil {
		t.Fatalf("RecordAuditAck: %v", err)
	}
	rest, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(rest) != 2 || rest[0].Key != all[2].Key || rest[1].Key != all[3].Key {
		t.Fatalf("un-acked after 2 acks = %+v, want [%q %q]", rest, all[2].Key, all[3].Key)
	}

	for _, rec := range rest {
		if err := db.RecordAuditAck(ctx, "i1", rec.Key); err != nil {
			t.Fatalf("RecordAuditAck: %v", err)
		}
	}
	none, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("un-acked after all acked = %+v, want empty", none)
	}
}

// TestFullyAuditedIdempotentAndDurable: finalizing twice keeps the same timestamp,
// and the flag survives a close/reopen (real fsync durability, ADR-0005 point 4).
func TestFullyAuditedIdempotentAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "FILLED")
	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	for _, rec := range recs {
		if err := db.RecordAuditAck(ctx, "i1", rec.Key); err != nil {
			t.Fatalf("RecordAuditAck: %v", err)
		}
	}
	if done, err := db.FinalizeFullyAudited(ctx, "i1"); err != nil || !done {
		t.Fatalf("FinalizeFullyAudited = %v, %v; want true, nil", done, err)
	}
	at1, set, err := db.FullyAudited(ctx, "i1")
	if err != nil || !set {
		t.Fatalf("FullyAudited = %v, %v; want set", set, err)
	}

	// Idempotent: a second finalize does not move the timestamp.
	if done, err := db.FinalizeFullyAudited(ctx, "i1"); err != nil || !done {
		t.Fatalf("second FinalizeFullyAudited = %v, %v; want true, nil", done, err)
	}
	at2, _, err := db.FullyAudited(ctx, "i1")
	if err != nil {
		t.Fatalf("FullyAudited: %v", err)
	}
	if !at2.Equal(at1) {
		t.Fatalf("fully-audited timestamp moved on re-finalize: %v -> %v", at1, at2)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Durable across restart.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	at3, set, err := db2.FullyAudited(ctx, "i1")
	if err != nil || !set {
		t.Fatalf("FullyAudited after reopen = %v, %v; want set (durable)", set, err)
	}
	if !at3.Equal(at1) {
		t.Fatalf("fully-audited timestamp not durable: %v vs %v", at3, at1)
	}
}

// TestRecordAuditAckIdempotent: recording the same ack twice is a no-op, not an
// error (at-least-once re-emit re-records the same key, ADR-0006 point 3).
func TestRecordAuditAckIdempotent(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "FILLED")
	recs, _ := db.UnackedLifecycleRecords(ctx, "i1")

	if err := db.RecordAuditAck(ctx, "i1", recs[0].Key); err != nil {
		t.Fatalf("first RecordAuditAck: %v", err)
	}
	if err := db.RecordAuditAck(ctx, "i1", recs[0].Key); err != nil {
		t.Fatalf("second RecordAuditAck (idempotent): %v", err)
	}
	rest, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}
	if len(rest) != len(recs)-1 {
		t.Fatalf("un-acked after double-ack of one key = %d, want %d", len(rest), len(recs)-1)
	}
}

// TestRecordAuditAckUnknownIntentIsNotFound: an ack for a never-appended intent is
// rejected (surfaces a wiring bug rather than silently recording an orphan).
func TestRecordAuditAckUnknownIntentIsNotFound(t *testing.T) {
	db := openTemp(t)
	if err := db.RecordAuditAck(context.Background(), "nope", "m:1"); !errors.Is(err, ErrIntentNotFound) {
		t.Fatalf("RecordAuditAck(unknown) err = %v, want ErrIntentNotFound", err)
	}
}

// TestV3AddsFullyAuditedColumnDefaultNull: a fresh store lands at V3 and a
// just-appended (unresolved) intent has no fully-audited flag set.
func TestV3AddsFullyAuditedColumnDefaultNull(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if v, err := db.schemaVersion(ctx); err != nil || v != schemaVersionV3 {
		t.Fatalf("schema version = %d, err %v, want %d", v, err, schemaVersionV3)
	}
	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if _, set, err := db.FullyAudited(ctx, "i1"); err != nil || set {
		t.Fatalf("fresh intent FullyAudited set = %v, err %v, want unset", set, err)
	}
}

// TestConcurrentAuditAck runs the ack-record + finalize paths concurrently under
// -race: single-writer serialization must let them converge to the flag set
// exactly once, with no lost ack and no spurious error.
func TestConcurrentAuditAck(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	seedResolvedIntent(t, db, ctx, "i1", "ord-1", "FILLED")
	recs, err := db.UnackedLifecycleRecords(ctx, "i1")
	if err != nil {
		t.Fatalf("UnackedLifecycleRecords: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(recs)*2)
	for _, rec := range recs {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if err := db.RecordAuditAck(ctx, "i1", key); err != nil {
				errs <- err
				return
			}
			if _, err := db.FinalizeFullyAudited(ctx, "i1"); err != nil {
				errs <- err
			}
		}(rec.Key)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent audit-ack: %v", err)
	}

	if _, set, err := db.FullyAudited(ctx, "i1"); err != nil || !set {
		t.Fatalf("after concurrent acks FullyAudited set = %v, err %v, want set", set, err)
	}
}

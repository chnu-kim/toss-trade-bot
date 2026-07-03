package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestAppendIntentRecordsPreparedMarker(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	in := Intent{IntentID: "i1", ClientOrderID: "c1", Payload: []byte(`{"symbol":"AAPL"}`)}
	if err := db.AppendIntent(ctx, in); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}

	got, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d intents, want 1", len(got))
	}
	i := got[0]
	if i.IntentID != "i1" || i.ClientOrderID != "c1" || string(i.Payload) != `{"symbol":"AAPL"}` {
		t.Fatalf("intent = %+v", i)
	}
	if i.ResolvedAt != nil {
		t.Fatalf("ResolvedAt = %v, want nil (unresolved)", i.ResolvedAt)
	}
	if len(i.Markers) != 1 || i.Markers[0].Kind != MarkerPrepared {
		t.Fatalf("markers = %+v, want single prepared marker", i.Markers)
	}
}

func TestMarkerProgressionLoadsInOrder(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("AppendMarker submit-attempted: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerAcked, "ord-99"); err != nil {
		t.Fatalf("AppendMarker acked: %v", err)
	}

	got, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	markers := got[0].Markers
	if len(markers) != 3 {
		t.Fatalf("got %d markers, want 3: %+v", len(markers), markers)
	}
	wantKinds := []MarkerKind{MarkerPrepared, MarkerSubmitAttempted, MarkerAcked}
	for i, m := range markers {
		if m.Kind != wantKinds[i] {
			t.Errorf("marker[%d] kind = %q, want %q", i, m.Kind, wantKinds[i])
		}
		if i > 0 && m.Seq <= markers[i-1].Seq {
			t.Errorf("marker seq not monotonic: %d then %d", markers[i-1].Seq, m.Seq)
		}
	}
	if markers[2].OrderID != "ord-99" {
		t.Errorf("acked orderID = %q, want ord-99", markers[2].OrderID)
	}
}

func TestResolveIntentLeavesUnresolvedSet(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	for _, id := range []string{"i1", "i2"} {
		if err := db.AppendIntent(ctx, Intent{IntentID: id, ClientOrderID: "c-" + id}); err != nil {
			t.Fatalf("AppendIntent %s: %v", id, err)
		}
	}

	if err := db.ResolveIntent(ctx, "i1", "aborted-before-submit"); err != nil {
		t.Fatalf("ResolveIntent: %v", err)
	}

	got, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	if len(got) != 1 || got[0].IntentID != "i2" {
		t.Fatalf("unresolved = %+v, want only i2", got)
	}
}

func TestResolveMissingIntentIsNotFound(t *testing.T) {
	db := openTemp(t)
	if err := db.ResolveIntent(context.Background(), "nope", "x"); !errors.Is(err, ErrIntentNotFound) {
		t.Fatalf("ResolveIntent(missing) err = %v, want ErrIntentNotFound", err)
	}
}

// TestCrashBetweenMarkersRecovers simulates a crash between two marker commits:
// a committed marker must durably survive a reopen on the same file, and the
// marker that was never committed must be absent — this is the crash-safety the
// 2-marker journal exists for (ADR-0002/0005).
func TestCrashBetweenMarkersRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.AppendIntent(ctx, Intent{IntentID: "i1", ClientOrderID: "c1"}); err != nil {
		t.Fatalf("AppendIntent: %v", err)
	}
	if err := db.AppendMarker(ctx, "i1", MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("AppendMarker: %v", err)
	}
	// "Crash" here: the acked marker is never written. Close without appending it.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the two committed markers survive, no acked marker was invented.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	got, err := db2.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents after reopen: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d intents after reopen, want 1 (unresolved intent must not be lost)", len(got))
	}
	kinds := markerKinds(got[0].Markers)
	want := []MarkerKind{MarkerPrepared, MarkerSubmitAttempted}
	if !equalKinds(kinds, want) {
		t.Fatalf("markers after reopen = %v, want %v (submit-attempted durable, acked absent)", kinds, want)
	}
}

func markerKinds(ms []Marker) []MarkerKind {
	out := make([]MarkerKind, len(ms))
	for i, m := range ms {
		out[i] = m.Kind
	}
	return out
}

func equalKinds(a, b []MarkerKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

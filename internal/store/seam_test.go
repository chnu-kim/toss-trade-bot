package store

import (
	"context"
	"testing"
)

// fakeStore is a stand-in a consumer package (order/killswitch/reconciler)
// could write to unit-test itself without a real database. Its existence proves
// the Store interface is a usable fake seam (ADR-0005 point 2 acceptance
// criterion). It records the last halt reason and answers a canned intent set.
type fakeStore struct {
	haltReason string
	halted     bool
	intents    []Intent
}

func (f *fakeStore) Atomically(ctx context.Context, fn func(tx Tx) error) error {
	return fn(fakeTx{f})
}
func (f *fakeStore) AppendIntent(ctx context.Context, in Intent) error {
	f.intents = append(f.intents, in)
	return nil
}
func (f *fakeStore) AppendMarker(context.Context, string, MarkerKind, string) error { return nil }
func (f *fakeStore) ResolveIntent(context.Context, string, string) error            { return nil }
func (f *fakeStore) LoadUnresolvedIntents(context.Context) ([]Intent, error) {
	return f.intents, nil
}
func (f *fakeStore) TripHalt(_ context.Context, reason string) error {
	f.halted, f.haltReason = true, reason
	return nil
}
func (f *fakeStore) ClearHalt(context.Context) error { f.halted = false; return nil }
func (f *fakeStore) Halt(context.Context) (HaltState, error) {
	return HaltState{Halted: f.halted, Reason: f.haltReason}, nil
}
func (f *fakeStore) SetCounter(context.Context, Counter) error { return nil }
func (f *fakeStore) Counter(_ context.Context, name string) (Counter, error) {
	return Counter{Name: name}, nil
}
func (f *fakeStore) Close() error { return nil }

type fakeTx struct{ f *fakeStore }

func (t fakeTx) AppendIntent(ctx context.Context, in Intent) error              { return t.f.AppendIntent(ctx, in) }
func (t fakeTx) AppendMarker(context.Context, string, MarkerKind, string) error { return nil }
func (t fakeTx) ResolveIntent(context.Context, string, string) error            { return nil }
func (t fakeTx) LoadUnresolvedIntents(ctx context.Context) ([]Intent, error) {
	return t.f.LoadUnresolvedIntents(ctx)
}
func (t fakeTx) TripHalt(ctx context.Context, reason string) error { return t.f.TripHalt(ctx, reason) }
func (t fakeTx) ClearHalt(ctx context.Context) error               { return t.f.ClearHalt(ctx) }
func (t fakeTx) Halt(ctx context.Context) (HaltState, error)       { return t.f.Halt(ctx) }
func (t fakeTx) SetCounter(context.Context, Counter) error         { return nil }
func (t fakeTx) Counter(ctx context.Context, name string) (Counter, error) {
	return t.f.Counter(ctx, name)
}

// compile-time assurance the fakes satisfy the seams.
var (
	_ Store = (*fakeStore)(nil)
	_ Tx    = fakeTx{}
)

func TestFakeStoreSatisfiesSeam(t *testing.T) {
	var s Store = &fakeStore{}
	ctx := context.Background()
	if err := s.Atomically(ctx, func(tx Tx) error {
		return tx.TripHalt(ctx, "test")
	}); err != nil {
		t.Fatalf("Atomically on fake: %v", err)
	}
	hs, _ := s.Halt(ctx)
	if !hs.Halted || hs.Reason != "test" {
		t.Fatalf("fake halt = %+v, want halted/test", hs)
	}
}

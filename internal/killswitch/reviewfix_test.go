package killswitch

import (
	"context"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// A zero occurredAt must not silently starve the token-refresh window: a zero
// WindowStart persists as NULL, so an un-guarded implementation resets the count
// to 1 on every failure and never reaches the threshold — a fail-open bypass
// (ADR-0013 latch scope (b): a counter that cannot accumulate must latch). The
// guard rejects it fail-closed. (codex adversarial-review [high].)
func TestReportTokenRefreshRejectsZeroTimestampFailClosed(t *testing.T) {
	ctx := context.Background()
	k := newOpen(t, openStore(t))

	if err := k.ReportTokenRefreshFailure(ctx, time.Time{}); err == nil {
		t.Fatalf("zero occurredAt token failure returned nil error (silent bypass)")
	}
	if !k.HasUnpersistedPendingHalt() {
		t.Fatalf("zero occurredAt token failure did not latch")
	}
	if ok, reason := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed after zero-timestamp token failure")
	} else if reason != "unpersisted-pending-halt" {
		t.Fatalf("blocked reason = %q, want unpersisted-pending-halt", reason)
	}
}

// An unrecognized persisted halt phase (schema drift, corruption, an
// incompatible reader) must boot fail-closed, not unhalted (ADR-0004 point 3:
// state unknown → blocked). (codex adversarial-review [medium].)
func TestBootUnknownHaltPhaseFailsClosed(t *testing.T) {
	cs := newControlStore(openStore(t))
	cs.set(func(c *controlStore) {
		c.haltOverride = &store.HaltState{Phase: store.HaltPhase("frozen-solid")}
	})

	k, err := New(context.Background(), cs, testConfig())
	if err == nil {
		t.Fatalf("New accepted an unrecognized halt phase without error")
	}
	if k == nil {
		t.Fatalf("New returned nil Switch; want a fail-closed guard")
	}
	k.NotifyScanComplete()
	if ok, reason := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed after boot on an unrecognized halt phase")
	} else if reason != "boot-halt" {
		t.Fatalf("blocked reason = %q, want boot-halt", reason)
	}
}

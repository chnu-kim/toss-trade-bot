package killswitch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	st := openStore(t)
	for name, cfg := range map[string]Config{
		"zero order threshold": {OrderFailureThreshold: 0, TokenRefreshThreshold: 1, TokenRefreshWindow: time.Minute},
		"zero token threshold": {OrderFailureThreshold: 1, TokenRefreshThreshold: 0, TokenRefreshWindow: time.Minute},
		"zero window":          {OrderFailureThreshold: 1, TokenRefreshThreshold: 1, TokenRefreshWindow: 0},
	} {
		if _, err := New(context.Background(), st, cfg); err == nil {
			t.Errorf("%s: New accepted invalid config %+v", name, cfg)
		}
	}
}

// Boot is fail-closed until the replay gate opens (ADR-0004 point 3).
func TestBootIsReplayGated(t *testing.T) {
	k, err := New(context.Background(), openStore(t), testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ok, reason := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed before replay-gate open")
	} else if reason != "replay-gate-closed" {
		t.Fatalf("blocked reason = %q, want replay-gate-closed", reason)
	}
	k.NotifyScanComplete()
	if ok, _ := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("submit still blocked after NotifyScanComplete")
	}
}

// A store halt-load failure at boot is fail-closed, not "no evidence"
// (ADR-0004 point 3): the returned Switch is boot-halted and CanSubmit is false.
func TestBootStoreLoadFailureFailsClosed(t *testing.T) {
	cs := newControlStore(openStore(t))
	cs.set(func(c *controlStore) { c.errHalt = errors.New("boom") })

	k, err := New(context.Background(), cs, testConfig())
	if err == nil {
		t.Fatalf("New returned nil error on halt-load failure")
	}
	if k == nil {
		t.Fatalf("New returned nil Switch; want a fail-closed guard")
	}
	k.NotifyScanComplete() // even after opening the gate, boot-halt keeps it blocked
	if ok, reason := k.CanSubmit("AAPL"); ok {
		t.Fatalf("submit allowed after store-load failure")
	} else if reason != "boot-halt" {
		t.Fatalf("blocked reason = %q, want boot-halt", reason)
	}
}

// persistence-wins: a durable pending or halted phase boots halted.
func TestBootPersistenceWins(t *testing.T) {
	for _, phase := range []store.HaltPhase{store.HaltPending, store.HaltHalted} {
		path := t.TempDir() + "/store.db"
		st := openStoreAt(t, path)
		ctx := context.Background()
		if err := st.MarkHaltPending(ctx, "prior-trip"); err != nil {
			t.Fatalf("MarkHaltPending: %v", err)
		}
		if phase == store.HaltHalted {
			if err := st.TripHalt(ctx, "prior-trip"); err != nil {
				t.Fatalf("TripHalt: %v", err)
			}
		}
		_ = st.Close()

		st2 := openStoreAt(t, path)
		t.Cleanup(func() { _ = st2.Close() })
		k, err := New(ctx, st2, testConfig())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		k.NotifyScanComplete()
		if ok, reason := k.CanSubmit("AAPL"); ok {
			t.Fatalf("phase %s booted unhalted", phase)
		} else if reason != "global-halt:"+string(phase) {
			t.Fatalf("phase %s blocked reason = %q", phase, reason)
		}
	}
}

func TestPerSymbolBlockIsScopedAndAutoClears(t *testing.T) {
	st := openStore(t)
	k := newOpen(t, st)
	ctx := context.Background()

	if err := k.Trip(ctx, ScopeSymbol, "AAPL", "ambiguous-submit", time.Now()); err != nil {
		t.Fatalf("Trip symbol: %v", err)
	}
	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("AAPL not blocked after per-symbol trip")
	}
	if ok, _ := k.CanSubmit("MSFT"); !ok {
		t.Fatalf("MSFT wrongly blocked by AAPL per-symbol trip")
	}
	// Per-symbol trip is memory-only (ADR-0004 point 4): no durable halt.
	if p := haltPhase(t, st); p != store.HaltNone {
		t.Fatalf("per-symbol trip persisted halt phase %s", p)
	}
	k.ClearSymbol("AAPL")
	if ok, _ := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("AAPL still blocked after ClearSymbol")
	}
}

// TOCTOU: Reserve→Reconfirm aborts on a global/same-symbol trip and passes an
// unrelated symbol; a pending window also blocks; a trip-then-clear inside the
// window progresses (level semantics, ADR-0013 — no generation).
func TestReserveReconfirm(t *testing.T) {
	ctx := context.Background()

	t.Run("aborts on global trip landed after reserve", func(t *testing.T) {
		k := newOpen(t, openStore(t))
		r := k.Reserve("AAPL")
		if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
			t.Fatalf("Trip: %v", err)
		}
		if ok, _ := k.Reconfirm(r); ok {
			t.Fatalf("Reconfirm passed after a global trip")
		}
	})

	t.Run("aborts on same-symbol trip, passes unrelated symbol", func(t *testing.T) {
		k := newOpen(t, openStore(t))
		rBlocked := k.Reserve("AAPL")
		rOther := k.Reserve("MSFT")
		if err := k.Trip(ctx, ScopeSymbol, "AAPL", "ambiguous", time.Now()); err != nil {
			t.Fatalf("Trip: %v", err)
		}
		if ok, _ := k.Reconfirm(rBlocked); ok {
			t.Fatalf("Reconfirm passed for blocked symbol")
		}
		if ok, reason := k.Reconfirm(rOther); !ok {
			t.Fatalf("Reconfirm aborted unrelated symbol: %s", reason)
		}
	})

	t.Run("trip-then-clear inside window progresses", func(t *testing.T) {
		k := newOpen(t, openStore(t))
		r := k.Reserve("AAPL")
		if err := k.Trip(ctx, ScopeGlobal, "", "manual", time.Now()); err != nil {
			t.Fatalf("Trip: %v", err)
		}
		if err := k.ClearHalt(ctx); err != nil {
			t.Fatalf("ClearHalt: %v", err)
		}
		if ok, reason := k.Reconfirm(r); !ok {
			t.Fatalf("Reconfirm aborted after trip-then-clear: %s", reason)
		}
	})
}

// No auto-resume (ADR-0004 point 6): only ClearHalt lifts a global halt, and it
// lifts durableHalt, the latch, and bootHalt together.
func TestNoAutoResumeClearHaltLiftsAllThree(t *testing.T) {
	ctx := context.Background()
	k := newOpen(t, openStore(t))
	k.BootHalt()

	// A latched (unpersisted) pending halt via a failed MarkHaltPending.
	cs := newControlStore(openStore(t))
	kl := newOpen(t, cs)
	cs.set(func(c *controlStore) { c.errMarkPending = errors.New("store down") })
	_ = kl.Trip(ctx, ScopeGlobal, "", "manual", time.Now())
	cs.set(func(c *controlStore) { c.errMarkPending = nil })

	if ok, _ := k.CanSubmit("AAPL"); ok {
		t.Fatalf("bootHalt did not block")
	}
	if err := k.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if ok, _ := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("ClearHalt did not lift bootHalt")
	}

	if ok, _ := kl.CanSubmit("AAPL"); ok {
		t.Fatalf("latch did not block")
	}
	if !kl.HasUnpersistedPendingHalt() {
		t.Fatalf("latch not reported by HasUnpersistedPendingHalt")
	}
	if err := kl.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt (latched): %v", err)
	}
	if ok, _ := kl.CanSubmit("AAPL"); !ok {
		t.Fatalf("ClearHalt did not lift latch")
	}
	if kl.HasUnpersistedPendingHalt() {
		t.Fatalf("latch still reported after ClearHalt")
	}
}

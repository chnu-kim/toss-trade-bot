package killswitch_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// spyNotifier records HaltTripped calls (the notifier seam, ADR-0004 point 8).
type spyNotifier struct {
	mu    sync.Mutex
	calls []string
}

func (s *spyNotifier) HaltTripped(reason string, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, reason)
}

func (s *spyNotifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *spyNotifier) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[len(s.calls)-1]
}

// panicNotifier proves a broken notifier cannot kill the trip path
// (unattended constraint: recover boundary).
type panicNotifier struct{}

func (panicNotifier) HaltTripped(string, time.Time) { panic("notifier exploded") }

// flakyStore wraps a real store and injects failures per method group.
// Used to prove the guard fails closed when its substrate fails
// (ADR-0004 point 3).
type flakyStore struct {
	store.Store
	failHaltLoad    bool
	failCounterLoad bool
	failWrites      bool
}

var errInjected = errors.New("injected store failure")

func (f *flakyStore) Halt(ctx context.Context) (store.HaltState, error) {
	if f.failHaltLoad {
		return store.HaltState{}, errInjected
	}
	return f.Store.Halt(ctx)
}

func (f *flakyStore) Counter(ctx context.Context, name string) (store.Counter, error) {
	if f.failCounterLoad {
		return store.Counter{}, errInjected
	}
	return f.Store.Counter(ctx, name)
}

func (f *flakyStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	if f.failWrites {
		return errInjected
	}
	return f.Store.Atomically(ctx, fn)
}

func (f *flakyStore) ClearHalt(ctx context.Context) error {
	if f.failWrites {
		return errInjected
	}
	return f.Store.ClearHalt(ctx)
}

func (f *flakyStore) SetCounter(ctx context.Context, c store.Counter) error {
	if f.failWrites {
		return errInjected
	}
	return f.Store.SetCounter(ctx, c)
}

// fakeClock is the injected clock seam (ADR-0004: 시계 mock 주입).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openStoreAt(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open(%q): %v", path, err)
	}
	return db
}

func openStore(t *testing.T) *store.DB {
	t.Helper()
	db := openStoreAt(t, filepath.Join(t.TempDir(), "store.db"))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustAllow(t *testing.T, d killswitch.Decision) {
	t.Helper()
	if !d.Allowed {
		t.Fatalf("expected allowed, got blocked: %q", d.Reason)
	}
}

func mustBlock(t *testing.T, d killswitch.Decision, reasonFragment string) {
	t.Helper()
	if d.Allowed {
		t.Fatalf("expected blocked (reason containing %q), got allowed", reasonFragment)
	}
	if d.Reason == "" {
		t.Fatal("blocked decision must carry a non-empty reason")
	}
	if reasonFragment != "" && !strings.Contains(d.Reason, reasonFragment) {
		t.Fatalf("blocked reason %q does not contain %q", d.Reason, reasonFragment)
	}
}

// ---------------------------------------------------------------------------
// Startup replay-gate (ADR-0004 point 3)
// ---------------------------------------------------------------------------

func TestGateClosedAtBootUntilReplayComplete(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)

	g := killswitch.New(ctx, db, nil, killswitch.Config{})

	// Freshly constructed: no halt, no symbol blocks, but the replay gate is
	// closed — new exposure must be blocked until the unresolved-intent scan
	// completes (restart must not bypass per-symbol protection).
	mustBlock(t, g.CanSubmit("AAPL"), "replay")

	g.MarkReplayComplete()
	mustAllow(t, g.CanSubmit("AAPL"))
}

func TestHaltLoadFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db, failHaltLoad: true}
	spy := &spyNotifier{}

	g := killswitch.New(ctx, flaky, spy, killswitch.Config{})
	g.MarkReplayComplete()

	// Cannot read the persisted halt state => treated as halted (fail-closed),
	// even after the replay gate opens.
	mustBlock(t, g.CanSubmit("AAPL"), "")
	if halted, _ := g.Halted(); !halted {
		t.Fatal("guard must report halted when halt state could not be loaded")
	}
	if spy.count() == 0 {
		t.Fatal("boot fail-closed transition should notify")
	}
}

func TestCounterLoadFailureFailsClosedAndClearRecovers(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db, failCounterLoad: true}

	g := killswitch.New(ctx, flaky, nil, killswitch.Config{})
	g.MarkReplayComplete()

	// Escalation state could not be recovered => fail-closed, not "no evidence"
	// (ADR-0004 point 7).
	mustBlock(t, g.CanSubmit("AAPL"), "")

	// While the substrate is still failing, an explicit clear must refuse.
	if err := g.ClearGlobalHalt(ctx); err == nil {
		t.Fatal("ClearGlobalHalt must fail while counter recovery still fails")
	}
	mustBlock(t, g.CanSubmit("AAPL"), "")

	// Substrate recovers; the explicit clear reloads counters and resumes.
	flaky.failCounterLoad = false
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt after recovery: %v", err)
	}
	mustAllow(t, g.CanSubmit("AAPL"))
}

// ---------------------------------------------------------------------------
// Global halt persistence across restart (ADR-0004 point 4)
// ---------------------------------------------------------------------------

func TestGlobalHaltSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")

	db1 := openStoreAt(t, path)
	g1 := killswitch.New(ctx, db1, nil, killswitch.Config{})
	g1.MarkReplayComplete()
	if err := g1.Trip(ctx, killswitch.Global(), "manual: incident drill", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustBlock(t, g1.CanSubmit("AAPL"), "incident drill")
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restart: the guard must boot halted — a restart is not a bypass.
	db2 := openStoreAt(t, path)
	defer db2.Close()
	g2 := killswitch.New(ctx, db2, nil, killswitch.Config{})
	g2.MarkReplayComplete()
	mustBlock(t, g2.CanSubmit("AAPL"), "incident drill")
	if halted, reason := g2.Halted(); !halted || !strings.Contains(reason, "incident drill") {
		t.Fatalf("expected halted with original reason after restart, got halted=%v reason=%q", halted, reason)
	}
}

func TestBootWithPersistedHaltDoesNotRenotify(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")

	db1 := openStoreAt(t, path)
	spy1 := &spyNotifier{}
	g1 := killswitch.New(ctx, db1, spy1, killswitch.Config{})
	g1.MarkReplayComplete()
	if err := g1.Trip(ctx, killswitch.Global(), "boom", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if spy1.count() != 1 {
		t.Fatalf("expected exactly one notification on trip, got %d", spy1.count())
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2 := openStoreAt(t, path)
	defer db2.Close()
	spy2 := &spyNotifier{}
	g2 := killswitch.New(ctx, db2, spy2, killswitch.Config{})
	if halted, _ := g2.Halted(); !halted {
		t.Fatal("expected halted boot")
	}
	// Booting into an already-notified halt is not a new transition.
	if spy2.count() != 0 {
		t.Fatalf("boot with persisted halt must not re-notify, got %d calls", spy2.count())
	}
}

// ---------------------------------------------------------------------------
// Threshold escalation + counter restart asymmetry (ADR-0004 point 7)
// ---------------------------------------------------------------------------

func TestTokenFailureEscalationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	cfg := killswitch.Config{TokenRefreshFailureThreshold: 3}

	db1 := openStoreAt(t, path)
	g1 := killswitch.New(ctx, db1, nil, cfg)
	g1.MarkReplayComplete()
	at := time.Now()
	for i := 0; i < 2; i++ {
		if err := g1.ReportTokenRefreshFailure(ctx, at); err != nil {
			t.Fatalf("ReportTokenRefreshFailure #%d: %v", i+1, err)
		}
	}
	mustAllow(t, g1.CanSubmit("AAPL")) // threshold not reached yet
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restart before the threshold: persisted progress must survive — one more
	// failure (not three) trips the global halt.
	db2 := openStoreAt(t, path)
	g2 := killswitch.New(ctx, db2, nil, cfg)
	g2.MarkReplayComplete()
	mustAllow(t, g2.CanSubmit("AAPL"))
	if err := g2.ReportTokenRefreshFailure(ctx, at); err != nil {
		t.Fatalf("ReportTokenRefreshFailure after restart: %v", err)
	}
	mustBlock(t, g2.CanSubmit("AAPL"), "token refresh")
	haltState, err := db2.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("escalated halt must be persisted")
	}
	c, err := db2.Counter(ctx, killswitch.CounterTokenRefreshFailures)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 3 {
		t.Fatalf("persisted counter = %d, want 3", c.Value)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// And a further restart boots halted.
	db3 := openStoreAt(t, path)
	defer db3.Close()
	g3 := killswitch.New(ctx, db3, nil, cfg)
	g3.MarkReplayComplete()
	mustBlock(t, g3.CanSubmit("AAPL"), "token refresh")
}

func TestOrderFailureConsecutiveThresholdAndResetOnSuccess(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	cfg := killswitch.Config{OrderFailureThreshold: 3}
	g := killswitch.New(ctx, db, nil, cfg)
	g.MarkReplayComplete()
	at := time.Now()

	for i := 0; i < 2; i++ {
		if err := g.ReportOrderFailure(ctx, at); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	mustAllow(t, g.CanSubmit("AAPL"))

	// Success resets the consecutive streak (both mirror and persisted).
	if err := g.ReportOrderSuccess(ctx); err != nil {
		t.Fatalf("ReportOrderSuccess: %v", err)
	}
	c, err := db.Counter(ctx, killswitch.CounterOrderFailures)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 0 {
		t.Fatalf("persisted counter after success = %d, want 0", c.Value)
	}

	for i := 0; i < 2; i++ {
		if err := g.ReportOrderFailure(ctx, at); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	mustAllow(t, g.CanSubmit("AAPL")) // reset worked: 2 < 3

	if err := g.ReportOrderFailure(ctx, at); err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	mustBlock(t, g.CanSubmit("AAPL"), "order failures")
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("order-failure escalation must persist the halt")
	}
}

func TestResetWriteFailureKeepsStreak(t *testing.T) {
	// codex adversarial (PR #57 round 2): the mirror must not forget the
	// failure streak before the durable reset committed — otherwise a failed
	// reset leaves the live guard undercounting during exactly the
	// degraded-store scenarios it must survive.
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db}
	g := killswitch.New(ctx, flaky, nil, killswitch.Config{OrderFailureThreshold: 3})
	g.MarkReplayComplete()
	at := time.Now()

	for i := 0; i < 2; i++ {
		if err := g.ReportOrderFailure(ctx, at); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}

	flaky.failWrites = true
	if err := g.ReportOrderSuccess(ctx); err == nil {
		t.Fatal("ReportOrderSuccess must surface the durable reset failure")
	}
	flaky.failWrites = false

	// The streak survived the failed reset: one more failure (not three)
	// crosses the threshold.
	mustAllow(t, g.CanSubmit("AAPL"))
	if err := g.ReportOrderFailure(ctx, at); err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	mustBlock(t, g.CanSubmit("AAPL"), "order failures")
}

func TestClearRefusesWhileHaltStateUnreadable(t *testing.T) {
	// codex review (PR #57 round 2): after a boot halt-load failure, an
	// explicit clear must not wipe durable halt state it never managed to
	// read — the clear is allowed only once the halt state is readable again.
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db, failHaltLoad: true}
	g := killswitch.New(ctx, flaky, nil, killswitch.Config{})
	g.MarkReplayComplete()
	mustBlock(t, g.CanSubmit("AAPL"), "")

	if err := g.ClearGlobalHalt(ctx); err == nil {
		t.Fatal("ClearGlobalHalt must refuse while the halt state is still unreadable")
	}
	mustBlock(t, g.CanSubmit("AAPL"), "")

	flaky.failHaltLoad = false
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt after recovery: %v", err)
	}
	mustAllow(t, g.CanSubmit("AAPL"))
}

// ---------------------------------------------------------------------------
// Per-symbol blocks + ambiguous frequency escalation (ADR-0004 points 4, 7)
// ---------------------------------------------------------------------------

func TestSymbolTripBlocksOnlyThatSymbolAndAutoClears(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	if err := g.Trip(ctx, killswitch.Symbol("TSLA"), "ambiguous submit", time.Now()); err != nil {
		t.Fatalf("Trip(symbol): %v", err)
	}
	mustBlock(t, g.CanSubmit("TSLA"), "TSLA")
	mustAllow(t, g.CanSubmit("AAPL"))

	// Per-symbol blocks auto-clear when the condition resolves (reconciler
	// closes the ambiguity) — no human needed (ADR-0004 point 6).
	g.ClearSymbol("TSLA")
	mustAllow(t, g.CanSubmit("TSLA"))

	// Per-symbol blocks are NOT persisted (re-derived from journal, point 4).
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if haltState.Halted {
		t.Fatal("a symbol-scope trip must not persist a global halt")
	}
}

func TestAmbiguousFrequencyEscalatesToGlobalHalt(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	clock := newFakeClock()
	spy := &spyNotifier{}
	cfg := killswitch.Config{
		AmbiguousTripThreshold: 3,
		AmbiguousWindow:        10 * time.Minute,
		Now:                    clock.now,
	}
	g := killswitch.New(ctx, db, spy, cfg)
	g.MarkReplayComplete()

	if err := g.Trip(ctx, killswitch.Symbol("A"), "ambiguous submit", clock.now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	clock.advance(time.Minute)
	if err := g.Trip(ctx, killswitch.Symbol("B"), "ambiguous submit", clock.now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustAllow(t, g.CanSubmit("C")) // 2 < 3: no escalation yet
	if spy.count() != 0 {
		t.Fatalf("no notification expected before escalation, got %d", spy.count())
	}

	clock.advance(time.Minute)
	if err := g.Trip(ctx, killswitch.Symbol("C"), "ambiguous submit", clock.now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustBlock(t, g.CanSubmit("D"), "")
	if halted, _ := g.Halted(); !halted {
		t.Fatal("three symbol trips within the window must escalate to a global halt")
	}
	if spy.count() != 1 {
		t.Fatalf("escalation must notify exactly once, got %d", spy.count())
	}
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("escalated global halt must be persisted")
	}
}

func TestReinjectedOldTripsDoNotEscalate(t *testing.T) {
	// Restart scenario: the reconciler re-derives per-symbol blocks by
	// re-tripping with the ORIGINAL occurredAt (ADR-0004 point 7 — ambiguous
	// frequency is recomputed, not persisted). Old occurrences outside the
	// window must re-block their symbols without tripping a global halt.
	ctx := context.Background()
	db := openStore(t)
	clock := newFakeClock()
	cfg := killswitch.Config{
		AmbiguousTripThreshold: 3,
		AmbiguousWindow:        10 * time.Minute,
		Now:                    clock.now,
	}
	g := killswitch.New(ctx, db, nil, cfg)

	old := clock.now().Add(-time.Hour)
	for _, sym := range []string{"A", "B", "C"} {
		if err := g.Trip(ctx, killswitch.Symbol(sym), "ambiguous submit (replay)", old); err != nil {
			t.Fatalf("Trip: %v", err)
		}
	}
	g.MarkReplayComplete()

	if halted, reason := g.Halted(); halted {
		t.Fatalf("stale re-injected trips must not escalate, got halt: %q", reason)
	}
	mustBlock(t, g.CanSubmit("A"), "A")
	mustAllow(t, g.CanSubmit("D"))
}

// ---------------------------------------------------------------------------
// TOCTOU: final reconfirmation (ADR-0004 point 1)
// ---------------------------------------------------------------------------

func TestReconfirmBlocksAfterConcurrentGlobalTrip(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	d := g.CanSubmit("AAPL")
	mustAllow(t, d)

	// Halt lands between CanSubmit and the irreversible submit.
	if err := g.Trip(ctx, killswitch.Global(), "spike", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustBlock(t, g.Reconfirm(d), "")
}

func TestReconfirmBlocksAfterConcurrentSymbolTrip(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	d := g.CanSubmit("AAPL")
	mustAllow(t, d)
	if err := g.Trip(ctx, killswitch.Symbol("AAPL"), "ambiguous submit", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustBlock(t, g.Reconfirm(d), "AAPL")
}

func TestReconfirmBlocksAfterTripAndClearWindow(t *testing.T) {
	// The strong generation property: even if the halt is tripped AND cleared
	// between the initial check and the final reconfirmation, the stale
	// decision must not pass — state changed under it.
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	d := g.CanSubmit("AAPL")
	mustAllow(t, d)

	if err := g.Trip(ctx, killswitch.Global(), "spike", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt: %v", err)
	}

	mustBlock(t, g.Reconfirm(d), "")

	// A fresh decision after the clear is fine.
	fresh := g.CanSubmit("AAPL")
	mustAllow(t, fresh)
	mustAllow(t, g.Reconfirm(fresh))
}

func TestZeroValueDecisionNeverReconfirms(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	mustBlock(t, g.Reconfirm(killswitch.Decision{}), "")
}

// ---------------------------------------------------------------------------
// No auto-resume (ADR-0004 point 6)
// ---------------------------------------------------------------------------

func TestGlobalHaltHasNoAutoResumePath(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	clock := newFakeClock()
	g := killswitch.New(ctx, db, nil, killswitch.Config{Now: clock.now})
	g.MarkReplayComplete()

	if err := g.Trip(ctx, killswitch.Global(), "incident", clock.now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}

	// Every non-clear lever, plus lots of time passing: still halted.
	clock.advance(48 * time.Hour)
	if err := g.ReportOrderSuccess(ctx); err != nil {
		t.Fatalf("ReportOrderSuccess: %v", err)
	}
	if err := g.ReportTokenRefreshSuccess(ctx); err != nil {
		t.Fatalf("ReportTokenRefreshSuccess: %v", err)
	}
	g.ClearSymbol("AAPL")
	g.MarkReplayComplete()
	mustBlock(t, g.CanSubmit("AAPL"), "incident")

	// Only the explicit clear resumes.
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt: %v", err)
	}
	mustAllow(t, g.CanSubmit("AAPL"))
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if haltState.Halted {
		t.Fatal("explicit clear must clear the persisted halt")
	}
}

// ---------------------------------------------------------------------------
// Tx participation (ADR-0005 point 3; 결합 지점은 발신처 소유)
// ---------------------------------------------------------------------------

func TestTripTxJoinsCallerTransaction(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	// The caller (order/reconciler) owns the atomic coupling: journal write and
	// halt trip commit together.
	err := db.Atomically(ctx, func(tx store.Tx) error {
		if err := tx.AppendIntent(ctx, store.Intent{
			IntentID:      "intent-1",
			ClientOrderID: "client-1",
			Payload:       []byte(`{}`),
			CreatedAt:     time.Now(),
		}); err != nil {
			return err
		}
		return g.TripTx(ctx, tx, killswitch.Global(), "ambiguous submit recorded", time.Now())
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}

	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted || !strings.Contains(haltState.Reason, "ambiguous submit recorded") {
		t.Fatalf("halt not persisted through caller tx: %+v", haltState)
	}
	intents, err := db.LoadUnresolvedIntents(ctx)
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	if len(intents) != 1 {
		t.Fatalf("expected the journal write to commit with the trip, got %d intents", len(intents))
	}
	mustBlock(t, g.CanSubmit("AAPL"), "")
}

func TestTripTxRollbackKeepsMirrorHalted(t *testing.T) {
	// If the caller's transaction rolls back after TripTx, the durable halt is
	// gone but the in-process mirror stays halted: the divergence must fall on
	// the safe side (blocked) — documented fail-closed behaviour.
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, nil, killswitch.Config{})
	g.MarkReplayComplete()

	sentinel := errors.New("caller aborts")
	err := db.Atomically(ctx, func(tx store.Tx) error {
		if err := g.TripTx(ctx, tx, killswitch.Global(), "will roll back", time.Now()); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel rollback error, got %v", err)
	}

	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if haltState.Halted {
		t.Fatal("rolled-back tx must not persist the halt")
	}
	mustBlock(t, g.CanSubmit("AAPL"), "") // mirror stays halted: fail-closed
}

func TestReportOrderFailureTxJoinsCallerTransaction(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	cfg := killswitch.Config{OrderFailureThreshold: 2}
	g := killswitch.New(ctx, db, nil, cfg)
	g.MarkReplayComplete()
	at := time.Now()

	if err := g.ReportOrderFailure(ctx, at); err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}

	// Second (threshold-crossing) failure arrives as part of a journal event:
	// marker + counter + halt commit together in the caller's tx.
	err := db.Atomically(ctx, func(tx store.Tx) error {
		if err := tx.AppendIntent(ctx, store.Intent{
			IntentID:      "intent-fail",
			ClientOrderID: "client-fail",
			Payload:       []byte(`{}`),
			CreatedAt:     at,
		}); err != nil {
			return err
		}
		return g.ReportOrderFailureTx(ctx, tx, at)
	})
	if err != nil {
		t.Fatalf("Atomically: %v", err)
	}

	mustBlock(t, g.CanSubmit("AAPL"), "order failures")
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("halt must be persisted through the caller tx")
	}
	c, err := db.Counter(ctx, killswitch.CounterOrderFailures)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 2 {
		t.Fatalf("persisted counter = %d, want 2", c.Value)
	}
}

// ---------------------------------------------------------------------------
// Store write failures during operation (fail-closed, ADR-0005 point 6 링크)
// ---------------------------------------------------------------------------

func TestStoreWriteFailureDuringGlobalTripFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db, failWrites: true}
	spy := &spyNotifier{}
	g := killswitch.New(ctx, flaky, spy, killswitch.Config{})
	g.MarkReplayComplete()

	err := g.Trip(ctx, killswitch.Global(), "spike", time.Now())
	if err == nil {
		t.Fatal("Trip must surface the persist failure")
	}
	// The mirror is halted regardless: fail-closed even when durability failed.
	mustBlock(t, g.CanSubmit("AAPL"), "")
	if spy.count() != 1 {
		t.Fatalf("trip transition must notify exactly once, got %d", spy.count())
	}
}

func TestCounterPersistFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db, failWrites: true}
	g := killswitch.New(ctx, flaky, nil, killswitch.Config{TokenRefreshFailureThreshold: 100})
	g.MarkReplayComplete()

	// Far below threshold, but the reconstruction-resistant signal could not be
	// persisted — a restart would silently lose escalation progress, so the
	// guard fails closed now.
	err := g.ReportTokenRefreshFailure(ctx, time.Now())
	if err == nil {
		t.Fatal("ReportTokenRefreshFailure must surface the persist failure")
	}
	mustBlock(t, g.CanSubmit("AAPL"), "")
}

func TestClearGlobalHaltFailsClosedWhenStoreClearFails(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	flaky := &flakyStore{Store: db}
	g := killswitch.New(ctx, flaky, nil, killswitch.Config{})
	g.MarkReplayComplete()

	if err := g.Trip(ctx, killswitch.Global(), "incident", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}

	flaky.failWrites = true
	if err := g.ClearGlobalHalt(ctx); err == nil {
		t.Fatal("ClearGlobalHalt must fail when the durable clear fails")
	}
	mustBlock(t, g.CanSubmit("AAPL"), "") // still halted

	flaky.failWrites = false
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt: %v", err)
	}
	mustAllow(t, g.CanSubmit("AAPL"))
}

// ---------------------------------------------------------------------------
// Notifier seam robustness
// ---------------------------------------------------------------------------

func TestNotifierPanicIsContained(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	g := killswitch.New(ctx, db, panicNotifier{}, killswitch.Config{})
	g.MarkReplayComplete()

	// A broken notifier must not kill the trip path (죽지 않는다).
	if err := g.Trip(ctx, killswitch.Global(), "spike", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	mustBlock(t, g.CanSubmit("AAPL"), "")
	haltState, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("halt must persist even when the notifier panics")
	}
}

// ---------------------------------------------------------------------------
// Concurrency (must pass under -race, ADR-0004 point 5)
// ---------------------------------------------------------------------------

func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	db := openStore(t)
	spy := &spyNotifier{}
	cfg := killswitch.Config{
		// High thresholds so the hammering below exercises the hot paths
		// without instantly halting.
		AmbiguousTripThreshold:       10_000,
		OrderFailureThreshold:        10_000,
		TokenRefreshFailureThreshold: 10_000,
	}
	g := killswitch.New(ctx, db, spy, cfg)
	g.MarkReplayComplete()

	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				d := g.CanSubmit("AAPL")
				if d.Allowed {
					g.Reconfirm(d)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			sym := fmt.Sprintf("S%d", i%7)
			if err := g.Trip(ctx, killswitch.Symbol(sym), "ambiguous submit", time.Now()); err != nil {
				t.Errorf("Trip(symbol): %v", err)
			}
			g.ClearSymbol(sym)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if err := g.ReportOrderFailure(ctx, time.Now()); err != nil {
				t.Errorf("ReportOrderFailure: %v", err)
			}
			if err := g.ReportOrderSuccess(ctx); err != nil {
				t.Errorf("ReportOrderSuccess: %v", err)
			}
		}
	}()
	wg.Wait()

	if halted, reason := g.Halted(); halted {
		t.Fatalf("no threshold should have been crossed, got halt: %q", reason)
	}
	if err := g.Trip(ctx, killswitch.Global(), "final", time.Now()); err != nil {
		t.Fatalf("Trip(global): %v", err)
	}
	mustBlock(t, g.CanSubmit("AAPL"), "final")
}

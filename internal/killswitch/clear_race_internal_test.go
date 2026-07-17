package killswitch

// White-box tests for the conditional single-transaction ClearGlobalHalt
// (PR #57 redesign). The clear commits halt=0 inside ONE store transaction
// that is fenced on the durable halt epoch (and the in-memory haltGen), so:
//
//   - a GLOBAL trip racing the clear either aborts it (fence) or is itself
//     the halt=1 re-persist (no self-repersist tx — the two-tx crash window
//     is gone),
//   - a below-threshold PER-SYMBOL trip never touches the epoch/haltGen, so
//     it never makes the clear abort and never leaves store and mirror
//     inconsistent (the CRITICAL fail-open is structurally removed).
//
// The tests are white-box because reproducing the interleavings
// deterministically needs to observe the guard's generation counters: the
// injected store hooks fire the racing action at a precise point of the
// clear and block until its in-memory phase completed.

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// authorizeForTest performs the one-time live-trading authorization
// (ADR-0007) so white-box tests start from an authorized store.
func authorizeForTest(t *testing.T, ctx context.Context, st store.Store) {
	t.Helper()
	boot := New(ctx, st, nil, Config{})
	if err := boot.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("authorize store: %v", err)
	}
}

type hookPoint int

const (
	// hookInsideClearWrite fires the racing action from inside the clear's
	// ClearHalt call — AFTER the in-tx epoch/haltGen fence has passed,
	// exercising the "fence passed, then a trip lands" ordering.
	hookInsideClearWrite hookPoint = iota
	// hookBeforeClearTx fires the racing action before the clear transaction
	// starts, exercising the "trip already applied, the in-tx fence must
	// catch it" ordering.
	hookBeforeClearTx
)

// raceStore wraps the real store and, once armed, fires a racing action
// exactly once at the configured point of the clear, then blocks until the
// action's in-memory phase (a gen or haltGen bump) is visible.
type raceStore struct {
	store.Store
	tb    *testing.T
	g     *Guard
	ctx   context.Context
	point hookPoint
	armed bool // set (single-goroutine) right before ClearGlobalHalt
	// action is the racing event fired inside the clear window. Nil defaults
	// to a global Trip.
	action func() error
	// waitFull makes fireRace block until the racing action FULLY completes
	// (its durable write committed), not just its in-memory phase. Only valid
	// with hookBeforeClearTx (the clear does not yet hold the write
	// connection, so the racing trip can commit). This reproduces the codex
	// review P1 scenario: a global trip whose epoch bump is durably committed
	// before the clear transaction runs.
	waitFull bool
	fireMu   sync.Mutex
	fired    bool
	raceDone sync.WaitGroup
	raceErr  error
}

func (s *raceStore) fireRace() {
	// Explicit fire-once guard (not sync.Once): the racing trip re-enters this
	// method via the hooked Atomically, and a re-entrant call must be a cheap
	// no-op rather than block on the in-flight fire (which, under waitFull,
	// waits for that very trip — a self-deadlock).
	s.fireMu.Lock()
	if !s.armed || s.fired {
		s.fireMu.Unlock()
		return
	}
	s.fired = true
	s.fireMu.Unlock()

	s.g.mu.RLock()
	genBefore := s.g.gen
	haltGenBefore := s.g.haltGen
	s.g.mu.RUnlock()

	action := s.action
	if action == nil {
		action = func() error {
			return s.g.Trip(s.ctx, Global(), "raced signal", time.Now())
		}
	}
	s.raceDone.Add(1)
	go func() {
		defer s.raceDone.Done()
		s.raceErr = action()
	}()

	// Wait for the action's synchronous in-memory phase; its durable write
	// (if any) then queues behind whatever transaction is open.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.g.mu.RLock()
		bumped := s.g.gen != genBefore || s.g.haltGen != haltGenBefore
		s.g.mu.RUnlock()
		if bumped {
			break
		}
		if time.Now().After(deadline) {
			s.tb.Error("racing action never applied its in-memory phase")
			return
		}
		time.Sleep(time.Millisecond)
	}
	if s.waitFull {
		// Also wait for the durable commit: the racing trip's epoch bump must
		// be persisted before the clear transaction proceeds.
		s.raceDone.Wait()
	}
}

func (s *raceStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	if s.point == hookBeforeClearTx {
		s.fireRace()
	}
	return s.Store.Atomically(ctx, func(tx store.Tx) error {
		return fn(hookedTx{Tx: tx, s: s})
	})
}

type hookedTx struct {
	store.Tx
	s *raceStore
}

func (tx hookedTx) ClearHalt(ctx context.Context) error {
	if tx.s.point == hookInsideClearWrite {
		tx.s.fireRace()
	}
	return tx.Tx.ClearHalt(ctx)
}

// setupRaced builds an authorized store with a persisted global halt, wraps
// it in a raceStore, and returns the guard plus the db path. The caller arms
// rs and calls ClearGlobalHalt.
func setupRaced(t *testing.T, cfg Config, point hookPoint, action func(g *Guard) error) (*Guard, *raceStore, *store.DB, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	authorizeForTest(t, ctx, db)
	rs := &raceStore{Store: db, tb: t, ctx: ctx, point: point}
	g := New(ctx, rs, nil, cfg)
	rs.g = g
	if action != nil {
		rs.action = func() error { return action(g) }
	}
	g.MarkReplayComplete()
	if err := g.Trip(ctx, Global(), "first incident", time.Now()); err != nil {
		t.Fatalf("Trip(global): %v", err)
	}
	return g, rs, db, path
}

func reopenHalted(t *testing.T, db *store.DB, path string) bool {
	t.Helper()
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	hs, err := db2.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	return hs.Halted
}

// TestRecoveryFailedFailsClosedEvenIfHaltCleared covers codex review P1 on the
// durability round: a Report*FailureTx counter write failure sets
// recoveryFailed while an existing global halt is being cleared. Because the
// Tx path cannot change haltGen, a concurrent non-reload ClearGlobalHalt
// (which fences only on haltGen) can still commit halt=0 and set
// halted=false. CanSubmit must then STILL fail closed on recoveryFailed, or a
// failed safety-state write silently reopens submission.
func TestRecoveryFailedFailsClosedEvenIfHaltCleared(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	authorizeForTest(t, ctx, db)

	g := New(ctx, db, nil, Config{})
	g.MarkReplayComplete()

	// A Tx-scoped failure whose durable write is not owned by us marks the
	// guard recovery-failed (safety-state write could not be trusted).
	g.markRecoveryFailed()
	// Simulate the concurrent non-reload clear that already removed the halt
	// (it fenced on haltGen, which the Tx failure did not move).
	g.mu.Lock()
	g.halted = false
	g.mu.Unlock()

	// The guard must stay fail-closed on recoveryFailed despite halted=false.
	if d := g.CanSubmit("AAPL"); d.Allowed {
		t.Fatal("recoveryFailed must fail closed even when the halt was cleared concurrently")
	}
	if d := g.Reconfirm(Decision{Allowed: true, symbol: "AAPL", gen: 1}); d.Allowed {
		t.Fatal("Reconfirm must also fail closed on recoveryFailed")
	}
}

// --- Global trip racing the clear: clear must not release the halt ----------

func TestClearGlobalTripRaceKeepsHalt(t *testing.T) {
	for _, pt := range []struct {
		name string
		p    hookPoint
	}{
		{"before-clear-tx", hookBeforeClearTx},
		{"inside-clear-write", hookInsideClearWrite},
	} {
		t.Run(pt.name, func(t *testing.T) {
			ctx := context.Background()
			g, rs, db, path := setupRaced(t, Config{}, pt.p, func(g *Guard) error {
				return g.Trip(ctx, Global(), "raced global", time.Now())
			})
			rs.armed = true
			err := g.ClearGlobalHalt(ctx)
			rs.raceDone.Wait()
			if rs.raceErr != nil {
				t.Fatalf("racing global trip: %v", rs.raceErr)
			}
			if !errors.Is(err, errClearRaced) {
				t.Fatalf("clear racing a global trip must return errClearRaced, got %v", err)
			}
			if halted, _ := g.Halted(); !halted {
				t.Fatal("mirror must stay halted after a raced global clear")
			}
			if !reopenHalted(t, db, path) {
				t.Fatal("durable halt lost: restart would boot unhalted (fail-open)")
			}
		})
	}
}

// TestClearGlobalTripFullyCommittedBeforeClearKeepsHalt reproduces the codex
// review P1 on the redesign: a global trip that durably commits its
// TripHalt/epoch bump BEFORE the clear transaction runs (but after the clear
// captured its baseline) must not be overwritten. The old code re-read the
// epoch after the race and sampled the trip's new epoch, then committed
// halt=0 and only aborted afterwards — losing the halt across a restart. The
// fixed clear fences the durable epoch against the baseline captured before
// any I/O, so it aborts before committing halt=0.
func TestClearGlobalTripFullyCommittedBeforeClearKeepsHalt(t *testing.T) {
	ctx := context.Background()
	g, rs, db, path := setupRaced(t, Config{}, hookBeforeClearTx, func(g *Guard) error {
		return g.Trip(ctx, Global(), "raced global (fully committed)", time.Now())
	})
	rs.waitFull = true // the racing trip's epoch bump commits before the clear tx
	rs.armed = true
	err := g.ClearGlobalHalt(ctx)
	rs.raceDone.Wait()
	if rs.raceErr != nil {
		t.Fatalf("racing global trip: %v", rs.raceErr)
	}
	if !errors.Is(err, errClearRaced) {
		t.Fatalf("clear must abort when a global trip already committed, got %v", err)
	}
	if halted, _ := g.Halted(); !halted {
		t.Fatal("mirror must stay halted")
	}
	if !reopenHalted(t, db, path) {
		t.Fatal("durable halt lost: the committed global trip was overwritten (fail-open)")
	}
}

// --- Threshold escalation racing the clear: same as a global trip -----------

func TestClearThresholdCrossingRaceKeepsHalt(t *testing.T) {
	ctx := context.Background()
	// A single order failure crosses the threshold and escalates to a global
	// halt (bumps haltGen + persists epoch), so it must abort the clear.
	g, rs, db, path := setupRaced(t, Config{OrderFailureThreshold: 1}, hookInsideClearWrite,
		func(g *Guard) error { return g.ReportOrderFailure(ctx, time.Now()) })
	rs.armed = true
	err := g.ClearGlobalHalt(ctx)
	rs.raceDone.Wait()
	if rs.raceErr != nil {
		t.Fatalf("racing report: %v", rs.raceErr)
	}
	if !errors.Is(err, errClearRaced) {
		t.Fatalf("clear racing a threshold escalation must return errClearRaced, got %v", err)
	}
	if halted, _ := g.Halted(); !halted {
		t.Fatal("mirror must stay halted after a raced escalation clear")
	}
	if !reopenHalted(t, db, path) {
		t.Fatal("durable halt lost after a raced escalation clear")
	}
}

// --- Below-threshold symbol trip racing the clear: the CRITICAL case --------
//
// In the OLD design this left store=halt0 / mirror=halted and needed a
// self-repersist that could crash. In the redesign a per-symbol trip never
// touches the halt epoch, so the clear of the (unrelated) GLOBAL halt SUCCEEDS
// cleanly, the symbol block simply remains in the mirror, and a restart boots
// unhalted because the operator legitimately released the global halt. There
// is no inconsistency and no fail-open.

func TestClearSymbolTripRaceReleasesGlobalCleanly(t *testing.T) {
	for _, pt := range []struct {
		name string
		p    hookPoint
	}{
		{"before-clear-tx", hookBeforeClearTx},
		{"inside-clear-write", hookInsideClearWrite},
	} {
		t.Run(pt.name, func(t *testing.T) {
			ctx := context.Background()
			g, rs, db, path := setupRaced(t, Config{AmbiguousTripThreshold: 1000}, pt.p,
				func(g *Guard) error { return g.Trip(ctx, Symbol("TSLA"), "ambiguous submit", time.Now()) })
			rs.armed = true
			err := g.ClearGlobalHalt(ctx)
			rs.raceDone.Wait()
			if rs.raceErr != nil {
				t.Fatalf("racing symbol trip: %v", rs.raceErr)
			}
			// A per-symbol trip does not affect the global halt: the clear
			// succeeds (the operator's authorization stands).
			if err != nil {
				t.Fatalf("clear racing a below-threshold symbol trip must succeed, got %v", err)
			}
			if halted, _ := g.Halted(); halted {
				t.Fatal("global halt must be released after a clean clear")
			}
			// The symbol block still stands in the mirror.
			if d := g.CanSubmit("TSLA"); d.Allowed {
				t.Fatal("the raced symbol block must remain after the global clear")
			}
			if d := g.CanSubmit("AAPL"); !d.Allowed {
				t.Fatal("unrelated symbols must be submittable after the global clear")
			}
			// The store is consistently unhalted (operator released it); the
			// symbol block is memory-only and re-derived by the reconciler.
			if reopenHalted(t, db, path) {
				t.Fatal("store must be unhalted after a clean global clear (no self-repersist inconsistency)")
			}
		})
	}
}

// --- No self-repersist: the clear issues no second halt=1 write -------------
//
// Prove the two-tx repair is gone by counting the halt writes the clear
// itself performs. A raced GLOBAL clear must issue exactly one clear
// transaction (ClearHalt) and zero TripHalt of its own — any halt=1 that
// lands comes from the racing global trip, on its own transaction.

type countingTx struct {
	store.Tx
	clears, trips *int
}

func (tx countingTx) ClearHalt(ctx context.Context) error {
	*tx.clears++
	return tx.Tx.ClearHalt(ctx)
}

func (tx countingTx) TripHalt(ctx context.Context, reason string) error {
	*tx.trips++
	return tx.Tx.TripHalt(ctx, reason)
}

type countingStore struct {
	store.Store
	afterAuthorize bool
	clears, trips  int
}

func (s *countingStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	return s.Store.Atomically(ctx, func(tx store.Tx) error {
		if !s.afterAuthorize {
			return fn(tx)
		}
		return fn(countingTx{Tx: tx, clears: &s.clears, trips: &s.trips})
	})
}

func TestClearIssuesNoSelfRepersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	authorizeForTest(t, ctx, db)

	cs := &countingStore{Store: db}
	g := New(ctx, cs, nil, Config{})
	g.MarkReplayComplete()
	if err := g.Trip(ctx, Global(), "incident", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	// Only count writes issued from here on (the clear + any racing trip).
	cs.afterAuthorize = true

	// No race: a plain clear must be a single ClearHalt, zero TripHalt.
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("ClearGlobalHalt: %v", err)
	}
	if cs.clears != 1 {
		t.Fatalf("clear must issue exactly one ClearHalt, got %d", cs.clears)
	}
	if cs.trips != 0 {
		t.Fatalf("clear must issue zero TripHalt of its own (no self-repersist), got %d", cs.trips)
	}
}

// --- Recovery-reload race preserves a concurrent failure increment ----------
//
// During a needReload clear (boot could not load counters), a below-threshold
// failure report lands right after the clear read its reload snapshot. The
// report does not bump haltGen, so the clear still SUCCEEDS (the recovery
// halt is released), but the reloaded snapshot must not overwrite the racing
// increment in the mirror.

type reloadRaceStore struct {
	store.Store
	g        *Guard
	ctx      context.Context
	failLoad bool // fail all counter reads (boot recovery failure)
	armed    bool
	once     sync.Once
	raceErr  error
}

var errInjectedReload = errors.New("injected counter load failure")

func (s *reloadRaceStore) Counter(ctx context.Context, name string) (store.Counter, error) {
	if s.failLoad {
		return store.Counter{}, errInjectedReload
	}
	c, err := s.Store.Counter(ctx, name)
	if err == nil && s.armed && name == CounterTokenRefreshFailures {
		s.once.Do(func() {
			// The reload snapshot has been read; a failure report now lands
			// fully (mirror + durable) before the clear assigns the snapshot.
			s.raceErr = s.g.ReportTokenRefreshFailure(s.ctx, time.Now())
		})
	}
	return c, err
}

func TestRecoveryReloadRacePreservesIncrement(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	rs := &reloadRaceStore{Store: db, ctx: ctx, failLoad: true}
	g := New(ctx, rs, nil, Config{})
	rs.g = g
	g.MarkReplayComplete()
	if halted, _ := g.Halted(); !halted {
		t.Fatal("boot with failing counter loads must be halted (recovery failure)")
	}

	rs.failLoad = false
	rs.armed = true
	// The recovery clear succeeds (the racing failure is below threshold, so
	// it does not re-halt), and the concurrent increment survives.
	if err := g.ClearGlobalHalt(ctx); err != nil {
		t.Fatalf("recovery clear should succeed, got %v", err)
	}
	if rs.raceErr != nil {
		t.Fatalf("racing report: %v", rs.raceErr)
	}
	g.mu.RLock()
	count := g.tokenFail.count
	g.mu.RUnlock()
	if count != 1 {
		t.Fatalf("mirror token-failure count = %d, want 1 (racing report must not be erased)", count)
	}
}

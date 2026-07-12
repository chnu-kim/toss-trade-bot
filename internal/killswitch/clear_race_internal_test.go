package killswitch

// White-box regression tests for the clear/trip race found by codex review on
// PR #57: an unconditional durable clear racing a concurrent global trip
// could wipe the freshly persisted halt, so a restart would boot unhalted —
// turning a restart into a bypass of the very halt ADR-0004 persists.
//
// The tests are white-box because reproducing the race deterministically
// needs to observe the guard's generation counter: the injected store hooks
// fire the racing Trip at a precise point of the clear and wait until the
// trip's in-memory phase (gen bump) completed before letting the clear
// proceed.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

type hookPoint int

const (
	// hookInsideClearWrite fires the racing trip from inside the durable
	// clear write itself — after any pre-write checks the clear performs,
	// exercising the "clear commits first, trip must re-persist" ordering.
	hookInsideClearWrite hookPoint = iota
	// hookBeforeClearTx fires the racing trip before the clear transaction
	// starts, exercising the "trip already raced, clear must abort without
	// touching the store" ordering.
	hookBeforeClearTx
)

// raceStore wraps the real store and, once armed, fires a racing global Trip
// exactly once at the configured point of the durable clear (both the direct
// ClearHalt path and the transactional path are hooked), then blocks until
// the trip's in-memory phase is visible.
type raceStore struct {
	store.Store
	tb    *testing.T
	g     *Guard
	ctx   context.Context
	point hookPoint
	armed bool // set (single-goroutine) right before ClearGlobalHalt
	// beforeRace, when set, runs right before the racing trip fires —
	// used to inject an event (e.g. a stale durable mark) into the window.
	beforeRace func()
	once       sync.Once
	tripDone   sync.WaitGroup
	tripErr    error
}

func (s *raceStore) fireRacingTrip() {
	if !s.armed {
		return
	}
	s.once.Do(func() {
		if s.beforeRace != nil {
			s.beforeRace()
		}
		s.g.mu.RLock()
		genBefore := s.g.gen
		s.g.mu.RUnlock()

		s.tripDone.Add(1)
		go func() {
			defer s.tripDone.Done()
			s.tripErr = s.g.Trip(s.ctx, Global(), "raced signal", time.Now())
		}()

		// Wait for the trip's synchronous in-memory phase (gen bump); its
		// durable write then queues behind whatever transaction is open.
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.g.mu.RLock()
			bumped := s.g.gen != genBefore
			s.g.mu.RUnlock()
			if bumped {
				return
			}
			if time.Now().After(deadline) {
				s.tb.Error("racing trip never applied its in-memory phase")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func (s *raceStore) ClearHalt(ctx context.Context) error {
	if s.point == hookInsideClearWrite {
		s.fireRacingTrip()
	}
	return s.Store.ClearHalt(ctx)
}

func (s *raceStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	if s.point == hookBeforeClearTx {
		s.fireRacingTrip()
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
		tx.s.fireRacingTrip()
	}
	return tx.Tx.ClearHalt(ctx)
}

func runClearRace(t *testing.T, point hookPoint) {
	runClearRaceWith(t, point, nil)
}

func runClearRaceWith(t *testing.T, point hookPoint, beforeRace func(g *Guard)) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()

	rs := &raceStore{Store: db, tb: t, ctx: ctx, point: point}
	g := New(ctx, rs, nil, Config{})
	rs.g = g
	g.MarkReplayComplete()

	if err := g.Trip(ctx, Global(), "first incident", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}

	// The clear races a fresh global trip: it must abort instead of resuming.
	if beforeRace != nil {
		rs.beforeRace = func() { beforeRace(g) }
	}
	rs.armed = true
	if err := g.ClearGlobalHalt(ctx); err == nil {
		t.Fatal("ClearGlobalHalt racing a trip must return an error")
	}
	rs.tripDone.Wait()
	if rs.tripErr != nil {
		t.Fatalf("racing Trip: %v", rs.tripErr)
	}
	if halted, _ := g.Halted(); !halted {
		t.Fatal("mirror must stay halted after a raced clear")
	}

	// The core invariant: the durable halt survived the raced clear, so a
	// restart still boots halted (restart is not a bypass).
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	closed = true
	db2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	haltState, err := db2.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if !haltState.Halted {
		t.Fatal("durable halt was lost across the raced clear: restart would boot unhalted (fail-open)")
	}
	g2 := New(ctx, db2, nil, Config{})
	g2.MarkReplayComplete()
	if d := g2.CanSubmit("AAPL"); d.Allowed {
		t.Fatal("guard rebooted after a raced clear must stay blocked")
	}
}

func TestClearRacingTripInsideClearWriteKeepsDurableHalt(t *testing.T) {
	runClearRace(t, hookInsideClearWrite)
}

func TestClearRacingTripBeforeClearTxKeepsDurableHalt(t *testing.T) {
	runClearRace(t, hookBeforeClearTx)
}

// TestStalePersistMarkCannotDefeatClearRace covers codex P1 from round 3 on
// PR #57: a halt persist that was in flight when the clear started completes
// DURING the clear window. Its durable mark must be rejected as stale —
// if it resurrected haltDurable, the coalescing trip fired next would plan no
// re-persist and the clear's commit would wipe the only durable halt, so a
// restart would boot unhalted.
func TestStalePersistMarkCannotDefeatClearRace(t *testing.T) {
	var staleSeq uint64
	captured := false
	runClearRaceWith(t, hookInsideClearWrite, func(g *Guard) {
		if !captured {
			// Simulate the pre-clear persist completing now: it carries the
			// halt sequence from before the clear bumped it.
			g.mu.RLock()
			staleSeq = g.haltSeq - 1
			g.mu.RUnlock()
			captured = true
		}
		g.markHaltDurable(staleSeq)
	})
}

package killswitch

// White-box regression test for the reset/failure race found by codex
// adversarial review on PR #57 (round 2): a counter reset racing a failure
// report could commit a durable zero AFTER the failure's persist skipped its
// write (the monotonic guard compared against the pre-reset value), erasing
// escalation progress across a restart. The failure streak must win over the
// reset in every interleaving.
//
// White-box for the same reason as the clear-race tests: deterministic
// reproduction needs to observe the guard's in-memory counter.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// resetRaceStore fires one racing ReportOrderFailure from inside the durable
// reset write (the SetCounter carrying Value == 0), then blocks until the
// failure's in-memory phase is visible.
type resetRaceStore struct {
	store.Store
	tb       *testing.T
	g        *Guard
	ctx      context.Context
	armed    bool // set (single-goroutine) right before the reset
	once     sync.Once
	raceDone sync.WaitGroup
	raceErr  error
}

func (s *resetRaceStore) maybeFire(c store.Counter) {
	if !s.armed || c.Name != CounterOrderFailures || c.Value != 0 {
		return
	}
	s.once.Do(func() {
		s.g.mu.RLock()
		countBefore := s.g.orderFail.count
		s.g.mu.RUnlock()

		s.raceDone.Add(1)
		go func() {
			defer s.raceDone.Done()
			s.raceErr = s.g.ReportOrderFailure(s.ctx, time.Now())
		}()

		deadline := time.Now().Add(5 * time.Second)
		for {
			s.g.mu.RLock()
			bumped := s.g.orderFail.count != countBefore
			s.g.mu.RUnlock()
			if bumped {
				return
			}
			if time.Now().After(deadline) {
				s.tb.Error("racing failure never applied its in-memory phase")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func (s *resetRaceStore) SetCounter(ctx context.Context, c store.Counter) error {
	s.maybeFire(c)
	return s.Store.SetCounter(ctx, c)
}

func (s *resetRaceStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	return s.Store.Atomically(ctx, func(tx store.Tx) error {
		return fn(resetHookedTx{Tx: tx, s: s})
	})
}

type resetHookedTx struct {
	store.Tx
	s *resetRaceStore
}

func (tx resetHookedTx) SetCounter(ctx context.Context, c store.Counter) error {
	tx.s.maybeFire(c)
	return tx.Tx.SetCounter(ctx, c)
}

func TestResetRacingFailureKeepsCounterProgress(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	authorizeForTest(t, ctx, db)
	rs := &resetRaceStore{Store: db, tb: t, ctx: ctx}
	g := New(ctx, rs, nil, Config{OrderFailureThreshold: 5})
	rs.g = g
	g.MarkReplayComplete()

	at := time.Now()
	for i := 0; i < 2; i++ {
		if err := g.ReportOrderFailure(ctx, at); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}

	// The reset races failure #3: the failure must win — no interleaving may
	// leave the durable counter behind the mirror.
	rs.armed = true
	if err := g.ReportOrderSuccess(ctx); err == nil {
		t.Fatal("a reset that raced a failure must not report clean success")
	}
	rs.raceDone.Wait()
	if rs.raceErr != nil {
		t.Fatalf("racing ReportOrderFailure: %v", rs.raceErr)
	}

	g.mu.RLock()
	mirror := g.orderFail.count
	g.mu.RUnlock()
	if mirror != 3 {
		t.Fatalf("mirror streak = %d, want 3 (failures win over the raced reset)", mirror)
	}
	c, err := db.Counter(ctx, CounterOrderFailures)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if c.Value != 3 {
		t.Fatalf("durable counter = %d, want 3: a restart would lose escalation progress", c.Value)
	}
}

package killswitch

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// The concrete store.DB must satisfy the narrow consumer seam (ADR-0005 point 2
// — the real store is what #34/#36 wire in).
var _ Store = (*store.DB)(nil)

// openStore opens a real temp-dir SQLite store engine for a test. Durability,
// crash and interleaving are exercised against the real engine, not an in-memory
// fake (ADR-0005 point 2 / CLAUDE.md test policy).
func openStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openStoreAt opens/reopens a store at a fixed path (restart tests).
func openStoreAt(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open %s: %v", path, err)
	}
	return db
}

// controlStore wraps a real store.DB (via the Store seam) and injects durable
// errors, panics, and interleaving hooks over the real engine. All injection
// fields are guarded by mu and snapshotted at method entry so the store stays
// race-safe when several killswitch goroutines call it under -race. Hooks are
// invoked after the snapshot is released, so a blocking hook never holds mu.
type controlStore struct {
	Store // embedded real engine provides the un-overridden methods

	mu sync.Mutex

	errHalt        error
	errMarkPending error
	errTripHalt    error
	errClearHalt   error
	errAtomically  error
	errTxCounter   error
	errTxSetCount  error
	errTxTripHalt  error

	panicMarkPending bool
	panicTxTripHalt  bool

	// hooks fire inside the corresponding call, after the real write when the
	// name says "after", otherwise before. Used to freeze an interleaving.
	afterTripHalt    func() // after Store-level TripHalt commit, before mirror publish
	beforeMarkPend   func() // before Store-level MarkHaltPending
	beforeTxTripHalt func() // inside Atomically, before tx TripHalt
	beforeTxSetCount func() // inside Atomically, before tx SetCounter
}

func newControlStore(inner Store) *controlStore {
	return &controlStore{Store: inner}
}

func (c *controlStore) set(mutate func(*controlStore)) {
	c.mu.Lock()
	mutate(c)
	c.mu.Unlock()
}

func (c *controlStore) Halt(ctx context.Context) (store.HaltState, error) {
	c.mu.Lock()
	err := c.errHalt
	c.mu.Unlock()
	if err != nil {
		return store.HaltState{}, err
	}
	return c.Store.Halt(ctx)
}

func (c *controlStore) MarkHaltPending(ctx context.Context, reason string) error {
	c.mu.Lock()
	err, doPanic, hook := c.errMarkPending, c.panicMarkPending, c.beforeMarkPend
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	if doPanic {
		panic("injected MarkHaltPending panic")
	}
	if err != nil {
		return err
	}
	return c.Store.MarkHaltPending(ctx, reason)
}

func (c *controlStore) TripHalt(ctx context.Context, reason string) error {
	c.mu.Lock()
	err, hook := c.errTripHalt, c.afterTripHalt
	c.mu.Unlock()
	if err != nil {
		return err
	}
	e := c.Store.TripHalt(ctx, reason)
	if hook != nil {
		hook()
	}
	return e
}

func (c *controlStore) ClearHalt(ctx context.Context) error {
	c.mu.Lock()
	err := c.errClearHalt
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.Store.ClearHalt(ctx)
}

func (c *controlStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	c.mu.Lock()
	err := c.errAtomically
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.Store.Atomically(ctx, func(tx store.Tx) error {
		return fn(&controlTx{Tx: tx, c: c})
	})
}

// controlTx wraps the real tx and injects errors/panics/hooks on the three
// methods killswitch calls inside Atomically. The embedded store.Tx delegates
// everything else.
type controlTx struct {
	store.Tx
	c *controlStore
}

func (t *controlTx) Counter(ctx context.Context, name string) (store.Counter, error) {
	t.c.mu.Lock()
	err := t.c.errTxCounter
	t.c.mu.Unlock()
	if err != nil {
		return store.Counter{}, err
	}
	return t.Tx.Counter(ctx, name)
}

func (t *controlTx) SetCounter(ctx context.Context, cnt store.Counter) error {
	t.c.mu.Lock()
	err, hook := t.c.errTxSetCount, t.c.beforeTxSetCount
	t.c.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}
	return t.Tx.SetCounter(ctx, cnt)
}

func (t *controlTx) TripHalt(ctx context.Context, reason string) error {
	t.c.mu.Lock()
	err, doPanic, hook := t.c.errTxTripHalt, t.c.panicTxTripHalt, t.c.beforeTxTripHalt
	t.c.mu.Unlock()
	if hook != nil {
		hook()
	}
	if doPanic {
		panic("injected tx TripHalt panic")
	}
	if err != nil {
		return err
	}
	return t.Tx.TripHalt(ctx, reason)
}

// --- small shared helpers ---

func testConfig() Config {
	return Config{
		OrderFailureThreshold: 3,
		TokenRefreshThreshold: 2,
		TokenRefreshWindow:    time.Minute,
	}
}

// newOpen builds a Switch over st, opens the replay gate, and asserts it is
// clean at boot (no halt). Most behavioral tests want an armed, submitting guard.
func newOpen(t *testing.T, st Store, opts ...Option) *Switch {
	t.Helper()
	k, err := New(context.Background(), st, testConfig(), opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k.NotifyScanComplete()
	if ok, reason := k.CanSubmit("AAPL"); !ok {
		t.Fatalf("fresh open switch blocked: %s", reason)
	}
	return k
}

// recordingNotifier captures halt notifications for assertions.
type recordingNotifier struct {
	mu      sync.Mutex
	reasons []string
}

func (r *recordingNotifier) HaltTripped(reason string) {
	r.mu.Lock()
	r.reasons = append(r.reasons, reason)
	r.mu.Unlock()
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reasons)
}

// haltPhase reads the durable halt phase directly from the store.
func haltPhase(t *testing.T, st Store) store.HaltPhase {
	t.Helper()
	hs, err := st.Halt(context.Background())
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	return hs.Phase
}

// counterValue reads a durable counter value directly from the store.
func counterValue(t *testing.T, st Store, name string) int64 {
	t.Helper()
	c, err := st.Counter(context.Background(), name)
	if err != nil {
		t.Fatalf("Counter %s: %v", name, err)
	}
	return c.Value
}

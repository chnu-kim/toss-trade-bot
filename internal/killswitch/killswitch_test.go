package killswitch_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

var errBoom = errors.New("boom")

// --- test doubles -----------------------------------------------------------

// scriptedStore wraps a real *store.DB (temp dir engine) and can inject durable
// write / read failures on demand, so the guard's fail-closed and
// durable-before-visible behaviour can be exercised without a real crash. When an
// Atomically call is scripted to fail it returns the error WITHOUT running fn —
// faithfully modelling a rolled-back transaction (nothing persisted), which is
// exactly the net durable effect of a mid-tx SQLite lock/disk error.
type scriptedStore struct {
	inner killswitch.Store

	mu          sync.Mutex
	calls       int
	failOn      map[int]error // 1-based Atomically call index → error
	failHalt    error         // if set, Halt returns this
	failCounter error         // if set, the standalone Counter read returns this

	holdCall int           // if >0, that Atomically call signals entered then blocks on release
	entered  chan struct{} // closed when the held call is entered
	release  chan struct{} // the held call waits on this

	holdGates map[int]*holdGate // per-call gates for multi-call deterministic holds
}

// holdGate blocks a specific Atomically call so a test can interleave
// deterministically: the call closes entered when reached and waits on release.
type holdGate struct {
	entered chan struct{}
	release chan struct{}
}

func (s *scriptedStore) hold(n int) *holdGate {
	g := &holdGate{entered: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	if s.holdGates == nil {
		s.holdGates = make(map[int]*holdGate)
	}
	s.holdGates[n] = g
	s.mu.Unlock()
	return g
}

func (s *scriptedStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	ferr := s.failOn[n]
	hold := s.holdCall == n
	gate := s.holdGates[n]
	s.mu.Unlock()

	if gate != nil {
		close(gate.entered)
		<-gate.release
	}
	if hold {
		close(s.entered)
		<-s.release
	}
	if ferr != nil {
		return ferr
	}
	return s.inner.Atomically(ctx, fn)
}

func (s *scriptedStore) Halt(ctx context.Context) (store.HaltState, error) {
	s.mu.Lock()
	fh := s.failHalt
	s.mu.Unlock()
	if fh != nil {
		return store.HaltState{}, fh
	}
	return s.inner.Halt(ctx)
}

func (s *scriptedStore) Counter(ctx context.Context, name string) (store.Counter, error) {
	s.mu.Lock()
	fc := s.failCounter
	s.mu.Unlock()
	if fc != nil {
		return store.Counter{}, fc
	}
	return s.inner.Counter(ctx, name)
}

func (s *scriptedStore) setFailOn(m map[int]error) {
	s.mu.Lock()
	s.failOn = m
	s.mu.Unlock()
}

type notifyCall struct {
	reason string
	at     time.Time
}

type recNotifier struct {
	mu        sync.Mutex
	calls     []notifyCall
	panicking bool
}

func (r *recNotifier) HaltTripped(reason string, at time.Time) {
	r.mu.Lock()
	r.calls = append(r.calls, notifyCall{reason, at})
	panicking := r.panicking
	r.mu.Unlock()
	if panicking {
		panic("notifier boom")
	}
}

func (r *recNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recNotifier) lastReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return ""
	}
	return r.calls[len(r.calls)-1].reason
}

// --- helpers ----------------------------------------------------------------

func mustOpen(t *testing.T, path string) *store.DB {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db
}

// openTempStore opens a fresh store and auto-closes it at test end.
func openTempStore(t *testing.T) (*store.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db := mustOpen(t, path)
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// openGuard builds a guard on a fresh real store, with the gate already opened
// (NotifyScanComplete) so tests that are not about the startup window can submit.
func openGuard(t *testing.T, cfg killswitch.Config) (*killswitch.Guard, *store.DB) {
	t.Helper()
	db, _ := openTempStore(t)
	g := killswitch.New(context.Background(), db, &recNotifier{}, cfg)
	g.NotifyScanComplete()
	return g, db
}

// --- boot / fail-closed at construction ------------------------------------

func TestNew_BootsHaltedOnLoadFailure(t *testing.T) {
	db, _ := openTempStore(t)
	notifier := &recNotifier{}
	ss := &scriptedStore{inner: db, failHalt: errBoom}

	g := killswitch.New(context.Background(), ss, notifier, killswitch.Config{})
	g.NotifyScanComplete() // even with the gate open, a load failure boots halted

	if allowed, reason := g.CanSubmit("AAPL"); allowed {
		t.Fatalf("CanSubmit allowed after halt-load failure; want blocked (reason=%q)", reason)
	}
	if !g.Snapshot().Halted {
		t.Fatalf("snapshot = %+v, want halted after load failure", g.Snapshot())
	}
	if notifier.count() == 0 {
		t.Fatalf("expected a notification on fail-closed boot")
	}
}

func TestNew_BootsHaltedOnDurablePending(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	if err := db.MarkHaltPending(ctx, "trip-initiated"); err != nil {
		t.Fatalf("MarkHaltPending: %v", err)
	}

	g := killswitch.New(ctx, db, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("durable pending must boot halted (persistence-wins)")
	}
}

func TestNew_BootsHaltedOnDurableHalted(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	if err := db.TripHalt(ctx, "prior-halt"); err != nil {
		t.Fatalf("TripHalt: %v", err)
	}

	g := killswitch.New(ctx, db, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("durable halted must boot halted")
	}
}

func TestNew_FreshBootGateClosedUntilScanComplete(t *testing.T) {
	db, _ := openTempStore(t)
	g := killswitch.New(context.Background(), db, &recNotifier{}, killswitch.Config{})

	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("fresh boot before scan complete must block (startup replay gate)")
	}
	g.NotifyScanComplete()
	if allowed, reason := g.CanSubmit("AAPL"); !allowed {
		t.Fatalf("after scan complete want allowed, got blocked: %q", reason)
	}
}

// --- per-symbol blocks ------------------------------------------------------

func TestSymbolBlockAndClear(t *testing.T) {
	g, _ := openGuard(t, killswitch.Config{})
	ctx := context.Background()

	if err := g.Trip(ctx, killswitch.Symbol("AAPL"), "ambiguous outcome", time.Now()); err != nil {
		t.Fatalf("Trip symbol: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("blocked symbol must not be submittable")
	}
	if allowed, _ := g.CanSubmit("MSFT"); !allowed {
		t.Fatal("an unrelated symbol must stay submittable")
	}

	g.ClearSymbol("AAPL")
	if allowed, _ := g.CanSubmit("AAPL"); !allowed {
		t.Fatal("symbol must be submittable again after ClearSymbol")
	}
}

// --- global trip: durable-before-visible ------------------------------------

func TestTripGlobal_DurableBeforeVisible_Success(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{})
	ctx := context.Background()

	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err != nil {
		t.Fatalf("Trip global: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("global halt must block CanSubmit")
	}
	hs, err := db.Halt(ctx)
	if err != nil {
		t.Fatalf("Halt: %v", err)
	}
	if hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt phase = %q, want halted (durable committed before visible)", hs.Phase)
	}
}

// MarkHaltPending fails: the mirror must stay blocked (pending), the store must
// show none (nothing committed), and the guard must report an unpersisted pending.
func TestTripGlobal_MarkPendingFails_FailClosed(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err == nil {
		t.Fatal("Trip must return the durable write error")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("mirror must stay blocked (pending) after MarkHaltPending failure")
	}
	hs, _ := db.Halt(ctx)
	if hs.Phase != store.HaltNone {
		t.Fatalf("store phase = %q, want none (nothing committed)", hs.Phase)
	}
	if !g.HasUnpersistedPendingHalt() {
		t.Fatal("guard must report an in-memory pending the store does not reflect")
	}
}

// TripHalt fails after MarkHaltPending committed: the mirror must stay blocked
// (NOT reverted to unhalted), the store must show pending, and a restart must
// boot halted (2-phase lifecycle recovery).
func TestTripGlobal_TripHaltFails_NotReverted_RestartHalted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()
	db := mustOpen(t, path)
	ss := &scriptedStore{inner: db, failOn: map[int]error{2: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err == nil {
		t.Fatal("Trip must return the TripHalt error")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("mirror must NOT revert to unhalted after a failed TripHalt")
	}
	hs, _ := db.Halt(ctx)
	if hs.Phase != store.HaltPending {
		t.Fatalf("store phase = %q, want pending (MarkHaltPending committed, TripHalt did not)", hs.Phase)
	}
	if g.HasUnpersistedPendingHalt() {
		t.Fatal("a durably-persisted pending must NOT report as unpersisted (#36 detects it via the store read)")
	}

	// Simulate crash: close and reopen the store, build a fresh guard.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db2 := mustOpen(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	g2 := killswitch.New(ctx, db2, &recNotifier{}, killswitch.Config{})
	g2.NotifyScanComplete()
	if allowed, _ := g2.CanSubmit("AAPL"); allowed {
		t.Fatal("restart must boot halted from a durable pending (persistence-wins)")
	}
}

// The in-flight window (mirror pending, durable write not yet committed) must
// block CanSubmit. Run under -race.
func TestTripGlobal_PendingInFlightWindowBlocks(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{
		inner:    db,
		holdCall: 1, // hold the MarkHaltPending commit open
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	done := make(chan error, 1)
	go func() { done <- g.Trip(ctx, killswitch.Global(), "manual", time.Now()) }()

	<-ss.entered // the trip has set the mirror pending and is blocked in the commit
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must block during the in-flight pending window")
	}
	close(ss.release)
	if err := <-done; err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must stay blocked (halted) after the trip completes")
	}
}

// --- count-before-resolve (order failures) ----------------------------------

func TestReportOrderFailure_CountFirst_TripsAtThreshold(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{OrderFailureThreshold: 3})
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := g.ReportOrderFailure(ctx, time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure %d: %v", i, err)
		}
		// The counter is durably committed by the time the call returns — a caller
		// that resolves only after return can never resolve-before-count.
		c, _ := db.Counter(ctx, "killswitch:order-consecutive-failures")
		if c.Value != int64(i) {
			t.Fatalf("after report %d, durable counter = %d, want %d", i, c.Value, i)
		}
	}
	hs, _ := db.Halt(ctx)
	if hs.Phase != store.HaltHalted {
		t.Fatalf("threshold reached but durable halt = %q, want halted (same tx as counter)", hs.Phase)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("guard must block after order-failure escalation")
	}
}

func TestReportOrderFailure_Overcount_Tolerated(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{OrderFailureThreshold: 2})
	ctx := context.Background()

	// Report past the threshold (models restart re-report / reconciler re-count).
	for i := 0; i < 5; i++ {
		if err := g.ReportOrderFailure(ctx, time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	c, _ := db.Counter(ctx, "killswitch:order-consecutive-failures")
	if c.Value != 5 {
		t.Fatalf("counter = %d, want 5 (overcount tolerated)", c.Value)
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltHalted {
		t.Fatalf("halt = %q, want halted (overcount ⇒ over-halt = safe)", hs.Phase)
	}
}

// A threshold-crossing report whose durable write fails must stay fail-closed:
// the mirror is pre-set pending BEFORE the durable write, so a commit error leaves
// CanSubmit blocked (the counter itself rolls back). ADR-0012 Decision 1.
func TestReportOrderFailure_DurableTripError_FailClosed(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{OrderFailureThreshold: 1})
	g.NotifyScanComplete()

	if err := g.ReportOrderFailure(ctx, time.Now()); err == nil {
		t.Fatal("ReportOrderFailure must return the durable write error (caller must not resolve)")
	}
	c, _ := db.Counter(ctx, "killswitch:order-consecutive-failures")
	if c.Value != 0 {
		t.Fatalf("counter = %d, want 0 (rolled back)", c.Value)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("a threshold-crossing report whose durable write failed must stay blocked (fail-closed)")
	}
}

// The token-refresh count-first path has the same fail-closed contract.
func TestReportTokenRefreshFailure_DurableTripError_FailClosed(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{TokenRefreshFailureThreshold: 1})
	g.NotifyScanComplete()

	if err := g.ReportTokenRefreshFailure(ctx, time.Now()); err == nil {
		t.Fatal("ReportTokenRefreshFailure must return the durable write error")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("a threshold-crossing token report whose durable write failed must stay blocked (fail-closed)")
	}
}

// A BELOW-threshold report whose durable write fails must NOT halt: only
// threshold-crossing reports fail-closed, so a transient store hiccup on an early
// failure does not spuriously halt the bot.
func TestReportOrderFailure_BelowThreshold_StoreError_NotBlocked(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{OrderFailureThreshold: 3})
	g.NotifyScanComplete()

	if err := g.ReportOrderFailure(ctx, time.Now()); err == nil {
		t.Fatal("ReportOrderFailure must surface the store error")
	}
	if allowed, _ := g.CanSubmit("AAPL"); !allowed {
		t.Fatal("a below-threshold store error must not fail-closed the guard (no spurious halt)")
	}
}

// The in-flight window of a count-first threshold trip must block concurrent
// submitters: while the durable commit is in flight, CanSubmit sees pending.
// Run under -race.
func TestReportOrderFailure_InFlightWindowBlocks(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{
		inner:    db,
		holdCall: 1, // hold the count-first Atomically (counter++/TripHalt)
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{OrderFailureThreshold: 1})
	g.NotifyScanComplete()

	done := make(chan error, 1)
	go func() { done <- g.ReportOrderFailure(ctx, time.Now()) }()

	<-ss.entered // the report has pre-set the mirror pending and is committing
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must block during the count-first in-flight window")
	}
	close(ss.release)
	if err := <-done; err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must stay blocked (halted) after the count-first trip completes")
	}
}

func TestReportTokenRefreshFailure_InFlightWindowBlocks(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{
		inner:    db,
		holdCall: 1,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{TokenRefreshFailureThreshold: 1})
	g.NotifyScanComplete()

	done := make(chan error, 1)
	go func() { done <- g.ReportTokenRefreshFailure(ctx, time.Now()) }()

	<-ss.entered
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must block during the token count-first in-flight window")
	}
	close(ss.release)
	if err := <-done; err != nil {
		t.Fatalf("ReportTokenRefreshFailure: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("CanSubmit must stay blocked after the token count-first trip")
	}
}

// A global Trip launched while a count-first report holds a speculative pending
// must never be lost: the transition lock serializes it strictly after the report
// (which reverts its own speculative pending), and it then durably trips. This
// guards against the report's revert eating a concurrent trip (a fail-open the
// transition serialization closes structurally).
func TestReportOrderFailure_ConcurrentGlobalTripNotLost(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failCounter: errBoom} // pre-read errors ⇒ speculative pending
	reportTx := ss.hold(1)                                // hold the report's counter++ tx
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{OrderFailureThreshold: 3})
	g.NotifyScanComplete()

	reportDone := make(chan error, 1)
	go func() { reportDone <- g.ReportOrderFailure(ctx, time.Now()) }()
	<-reportTx.entered // report holds the transition lock + a speculative pending

	tripDone := make(chan error, 1)
	go func() { tripDone <- g.Trip(ctx, killswitch.Global(), "manual concurrent", time.Now()) }()

	close(reportTx.release) // report commits (no trip), reverts its pending, releases the lock
	if err := <-reportDone; err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	if err := <-tripDone; err != nil { // trip serializes after and durably trips
		t.Fatalf("Trip: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("a global trip concurrent with a count-first speculative pending must not be lost")
	}
}

// P1-A: a global Trip that arrives while a ClearHalt's durable write is in flight
// must not be dropped. Before the transition serialization, the trip hit the
// idempotent no-op (mirror still halted mid-clear) and returned without writing,
// then the clear wiped the halt — losing the new danger signal (fail-open). The
// transition lock serializes the trip strictly after the clear, so it sees the
// cleared mirror and durably re-trips.
func TestClearInFlight_ConcurrentTripNotDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()
	db := mustOpen(t, path)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.TripHalt(ctx, "prior halt"); err != nil { // boot halted
		t.Fatalf("seed TripHalt: %v", err)
	}
	ss := &scriptedStore{inner: db}
	clearTx := ss.hold(1) // hold ClearHalt's durable write
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	clearDone := make(chan error, 1)
	go func() { clearDone <- g.ClearHalt(ctx) }()
	<-clearTx.entered // clear's durable write is in flight; mirror still halted

	tripDone := make(chan error, 1)
	go func() { tripDone <- g.Trip(ctx, killswitch.Global(), "new danger during clear", time.Now()) }()

	// Give the trip a chance either to finish (older code: idempotent no-op while
	// the mirror is still halted) or to prove it is blocked on the transition lock.
	tripFinished := false
	var tripErr error
	select {
	case tripErr = <-tripDone:
		tripFinished = true
	case <-time.After(200 * time.Millisecond):
	}

	close(clearTx.release) // clear commits (durable none), lowers the mirror, releases the lock
	if err := <-clearDone; err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if !tripFinished {
		tripErr = <-tripDone
	}
	if tripErr != nil {
		t.Fatalf("Trip: %v", tripErr)
	}

	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("a trip arriving during a clear-in-flight was dropped — submissions reopened (fail-open)")
	}
	hs, _ := db.Halt(ctx)
	if hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt = %q, want halted (the concurrent trip must have durably re-tripped)", hs.Phase)
	}
}

// P1-B: an ambiguous escalation must not be suppressed by a count-first
// speculative pending. Before the transition serialization, tripSymbol read the
// speculative pending as "already halted" and skipped the escalation; the report
// then reverted its pending, leaving the ambiguous threshold crossed with no halt
// (fail-open). The transition lock serializes the escalation after the report, so
// it observes the reverted mirror (none) and does escalate to a durable halt.
func TestSpeculativePending_DoesNotSuppressAmbiguousEscalation(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	ss := &scriptedStore{inner: db, failCounter: errBoom} // report pre-read errors ⇒ speculative pending
	reportTx := ss.hold(1)                                // hold the report's counter++ tx
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{
		OrderFailureThreshold: 3,
		AmbiguousThreshold:    1, // a single ambiguous symbol trip escalates
		AmbiguousWindow:       time.Hour,
	})
	g.NotifyScanComplete()

	reportDone := make(chan error, 1)
	go func() { reportDone <- g.ReportOrderFailure(ctx, time.Now()) }()
	<-reportTx.entered // report holds the transition lock + a speculative pending

	escDone := make(chan error, 1)
	go func() { escDone <- g.Trip(ctx, killswitch.Symbol("AAPL"), "ambiguous", time.Now()) }()

	escFinished := false
	var escErr error
	select {
	case escErr = <-escDone:
		escFinished = true // older code: escalation suppressed, returns without a global halt
	case <-time.After(200 * time.Millisecond):
	}

	close(reportTx.release) // report commits (no trip), reverts its speculative pending
	if err := <-reportDone; err != nil {
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	if !escFinished {
		escErr = <-escDone
	}
	if escErr != nil {
		t.Fatalf("Trip(symbol): %v", escErr)
	}

	// An unrelated symbol must be blocked by the escalation's global halt.
	if allowed, _ := g.CanSubmit("MSFT"); allowed {
		t.Fatal("ambiguous escalation was suppressed by a speculative pending — global halt lost (fail-open)")
	}
	hs, _ := db.Halt(ctx)
	if hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt = %q, want halted (ambiguous escalation must durably trip)", hs.Phase)
	}
}

func TestReportOrderSuccess_ResetsCounter(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{OrderFailureThreshold: 3})
	ctx := context.Background()

	for i := 0; i < 2; i++ { // 2 failures, below threshold 3
		if err := g.ReportOrderFailure(ctx, time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure: %v", err)
		}
	}
	if err := g.ReportOrderSuccess(ctx); err != nil {
		t.Fatalf("ReportOrderSuccess: %v", err)
	}
	c, _ := db.Counter(ctx, "killswitch:order-consecutive-failures")
	if c.Value != 0 {
		t.Fatalf("counter after success = %d, want 0", c.Value)
	}
	// Threshold must re-charge from zero: 2 more failures must NOT trip.
	for i := 0; i < 2; i++ {
		if err := g.ReportOrderFailure(ctx, time.Now()); err != nil {
			t.Fatalf("ReportOrderFailure post-reset: %v", err)
		}
	}
	if g.Snapshot().Halted {
		t.Fatal("must not be halted: reset means the threshold has to re-charge")
	}
}

// --- token refresh failures (counted, persisted, windowed) ------------------

func TestReportTokenRefreshFailure_CountedPersistTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)

	db := mustOpen(t, path)
	g := killswitch.New(ctx, db, &recNotifier{}, killswitch.Config{
		TokenRefreshFailureThreshold: 3,
		TokenRefreshWindow:           time.Hour,
	})
	g.NotifyScanComplete()

	for i := 0; i < 2; i++ { // 2 failures, below threshold
		if err := g.ReportTokenRefreshFailure(ctx, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("ReportTokenRefreshFailure: %v", err)
		}
	}
	if g.Snapshot().Halted {
		t.Fatal("2 < 3 must not trip")
	}
	// Restart below threshold: the counter must survive.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db2 := mustOpen(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	c, _ := db2.Counter(ctx, "killswitch:token-refresh-failures")
	if c.Value != 2 {
		t.Fatalf("counter after restart = %d, want 2 (reconstruction-resistant)", c.Value)
	}

	g2 := killswitch.New(ctx, db2, &recNotifier{}, killswitch.Config{
		TokenRefreshFailureThreshold: 3,
		TokenRefreshWindow:           time.Hour,
	})
	g2.NotifyScanComplete()
	// One more failure within the window reaches the threshold and trips.
	if err := g2.ReportTokenRefreshFailure(ctx, base.Add(3*time.Minute)); err != nil {
		t.Fatalf("ReportTokenRefreshFailure (3rd): %v", err)
	}
	if hs, _ := db2.Halt(ctx); hs.Phase != store.HaltHalted {
		t.Fatalf("halt = %q, want halted at threshold (same tx)", hs.Phase)
	}
	if !g2.Snapshot().Halted {
		t.Fatal("guard must be halted after the 3rd token-refresh failure")
	}
}

func TestReportTokenRefreshFailure_WindowReset(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{
		TokenRefreshFailureThreshold: 3,
		TokenRefreshWindow:           time.Hour,
	})
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)

	for i := 0; i < 2; i++ { // 2 within the window
		if err := g.ReportTokenRefreshFailure(ctx, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("ReportTokenRefreshFailure: %v", err)
		}
	}
	// A failure well past the window resets the count to 1, not 3 → no trip.
	if err := g.ReportTokenRefreshFailure(ctx, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("ReportTokenRefreshFailure past window: %v", err)
	}
	c, _ := db.Counter(ctx, "killswitch:token-refresh-failures")
	if c.Value != 1 {
		t.Fatalf("counter after window reset = %d, want 1", c.Value)
	}
	if g.Snapshot().Halted {
		t.Fatal("window reset must not trip")
	}
}

// --- clear: manual only, no auto-resume -------------------------------------

func TestClearHalt_ManualOnly(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{OrderFailureThreshold: 1})
	ctx := context.Background()

	if err := g.ReportOrderFailure(ctx, time.Now()); err != nil { // trips (threshold 1)
		t.Fatalf("ReportOrderFailure: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("expected halted")
	}
	// A success report resets the counter but must NOT resume a tripped halt.
	if err := g.ReportOrderSuccess(ctx); err != nil {
		t.Fatalf("ReportOrderSuccess: %v", err)
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("ReportOrderSuccess must not un-halt a tripped global halt (manual clear only)")
	}
	// Only ClearHalt un-halts.
	if err := g.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if allowed, reason := g.CanSubmit("AAPL"); !allowed {
		t.Fatalf("after ClearHalt want allowed, got blocked: %q", reason)
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltNone {
		t.Fatalf("store phase after clear = %q, want none", hs.Phase)
	}
}

func TestClearHalt_FailClosedOnDurableError(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	if err := db.TripHalt(ctx, "prior"); err != nil {
		t.Fatalf("seed TripHalt: %v", err)
	}
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	if err := g.ClearHalt(ctx); err == nil {
		t.Fatal("ClearHalt must return the durable write error")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("halt must remain in effect when ClearHalt fails to commit (fail-closed)")
	}
}

// --- TOCTOU: Reserve / Reconfirm --------------------------------------------

func TestReconfirm_AbortsOnGlobalTrip(t *testing.T) {
	g, _ := openGuard(t, killswitch.Config{})
	ctx := context.Background()

	r, allowed, _ := g.Reserve("AAPL")
	if !allowed {
		t.Fatal("Reserve should be allowed before any trip")
	}
	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if ok, reason := g.Reconfirm(r); ok {
		t.Fatalf("Reconfirm must abort after a global trip; reason=%q", reason)
	}
}

func TestReconfirm_AbortsOnReservedSymbolTrip_AllowsUnrelated(t *testing.T) {
	g, ctx := mustGuard(t)

	// Reserved symbol gets blocked → Reconfirm aborts.
	rA, allowed, _ := g.Reserve("AAPL")
	if !allowed {
		t.Fatal("Reserve AAPL should be allowed")
	}
	if err := g.Trip(ctx, killswitch.Symbol("AAPL"), "ambiguous", time.Now()); err != nil {
		t.Fatalf("Trip symbol AAPL: %v", err)
	}
	if ok, _ := g.Reconfirm(rA); ok {
		t.Fatal("Reconfirm must abort when the reserved symbol is now blocked")
	}

	// An unrelated symbol block must NOT abort a different symbol's reservation.
	rB, allowed, _ := g.Reserve("MSFT")
	if !allowed {
		t.Fatal("Reserve MSFT should be allowed")
	}
	if err := g.Trip(ctx, killswitch.Symbol("TSLA"), "ambiguous", time.Now()); err != nil {
		t.Fatalf("Trip symbol TSLA: %v", err)
	}
	if ok, reason := g.Reconfirm(rB); !ok {
		t.Fatalf("Reconfirm for MSFT must still pass after an unrelated (TSLA) block: %q", reason)
	}
}

// --- replay gate + boot-halt affordance -------------------------------------

func TestReplayGate_ClosedWhenBootHalted_UntilClear(t *testing.T) {
	g, ctx := mustGuard(t) // gate opened by mustGuard
	// mustGuard opens the gate; force a boot-halt to close it again.
	g.BootHalt("conservative unclean boot", time.Now())

	// Scan-complete signal must NOT open the gate while boot-halted.
	g.NotifyScanComplete()
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("boot-halted guard must stay blocked even after NotifyScanComplete")
	}
	if g.HasUnpersistedPendingHalt() {
		t.Fatal("a conservative boot-halt is halted, not pending — must not report unpersisted pending")
	}

	// Only ClearHalt reopens it.
	if err := g.ClearHalt(ctx); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if allowed, reason := g.CanSubmit("AAPL"); !allowed {
		t.Fatalf("after ClearHalt (scan complete) want allowed, got blocked: %q", reason)
	}
}

func TestReplayGate_OpensOnlyOnScanComplete(t *testing.T) {
	db, _ := openTempStore(t)
	g := killswitch.New(context.Background(), db, &recNotifier{}, killswitch.Config{})
	if g.Snapshot().GateOpen {
		t.Fatal("gate must be closed before scan complete")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("closed gate must block")
	}
	g.NotifyScanComplete()
	if !g.Snapshot().GateOpen {
		t.Fatal("gate must open on scan complete")
	}
}

// --- graceful-shutdown affordance -------------------------------------------

func TestGracefulShutdown_QueryAndFinalize(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	// MarkHaltPending fails (store fully down at the trip instant) → in-memory
	// pending the store cannot show.
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	if err := g.Trip(ctx, killswitch.Global(), "disk-full", time.Now()); err == nil {
		t.Fatal("Trip must fail when MarkHaltPending fails")
	}
	if !g.HasUnpersistedPendingHalt() {
		t.Fatal("query must report the in-memory pending the store does not reflect")
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltNone {
		t.Fatalf("store phase = %q, want none (store read cannot see the pending)", hs.Phase)
	}

	// Finalize now succeeds (store recovered): the pending is durably promoted.
	ss.setFailOn(nil)
	if err := g.FinalizePendingHalt(ctx); err != nil {
		t.Fatalf("FinalizePendingHalt: %v", err)
	}
	if g.HasUnpersistedPendingHalt() {
		t.Fatal("after a successful finalize the pending must be persisted")
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltHalted {
		t.Fatalf("store phase after finalize = %q, want halted", hs.Phase)
	}
}

func TestGracefulShutdown_FinalizeFailure_StillUnpersisted(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	// Both the initial trip's MarkHaltPending and the finalize's MarkHaltPending fail.
	ss := &scriptedStore{inner: db, failOn: map[int]error{1: errBoom, 2: errBoom}}
	g := killswitch.New(ctx, ss, &recNotifier{}, killswitch.Config{})
	g.NotifyScanComplete()

	if err := g.Trip(ctx, killswitch.Global(), "disk-full", time.Now()); err == nil {
		t.Fatal("Trip must fail")
	}
	if err := g.FinalizePendingHalt(ctx); err == nil {
		t.Fatal("FinalizePendingHalt must return the durable error")
	}
	if !g.HasUnpersistedPendingHalt() {
		t.Fatal("a failed finalize must leave the pending reported as unpersisted (#36 refuses clean)")
	}
}

// --- ambiguous-frequency escalation -----------------------------------------

func TestAmbiguousFrequency_EscalatesToGlobal(t *testing.T) {
	g, db := openGuard(t, killswitch.Config{AmbiguousThreshold: 3, AmbiguousWindow: time.Hour})
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)

	// 2 ambiguous trips: symbols blocked, no global escalation yet.
	for i, sym := range []string{"AAPL", "MSFT"} {
		if err := g.Trip(ctx, killswitch.Symbol(sym), "ambiguous", base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("Trip %s: %v", sym, err)
		}
	}
	if g.Snapshot().Halted {
		t.Fatal("2 < 3 ambiguous must not escalate to global")
	}
	// The 3rd within the window escalates to a durable global halt.
	if err := g.Trip(ctx, killswitch.Symbol("TSLA"), "ambiguous", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("Trip TSLA: %v", err)
	}
	if !g.Snapshot().Halted {
		t.Fatal("3rd ambiguous within the window must escalate to global")
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltHalted {
		t.Fatalf("escalation must be durable: halt = %q, want halted", hs.Phase)
	}
}

func TestAmbiguousFrequency_OutsideWindow_NoEscalation(t *testing.T) {
	g, _ := openGuard(t, killswitch.Config{AmbiguousThreshold: 3, AmbiguousWindow: time.Minute})
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)

	// 3 ambiguous trips spread far apart: never 3 within a 1-minute window.
	for i, sym := range []string{"AAPL", "MSFT", "TSLA"} {
		if err := g.Trip(ctx, killswitch.Symbol(sym), "ambiguous", base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("Trip %s: %v", sym, err)
		}
	}
	if g.Snapshot().Halted {
		t.Fatal("ambiguous trips outside the window must not escalate")
	}
}

// --- notifier isolation -----------------------------------------------------

func TestNotifier_CalledOnTrip_AndPanicIsolated(t *testing.T) {
	db, _ := openTempStore(t)
	ctx := context.Background()
	notifier := &recNotifier{panicking: true}
	g := killswitch.New(ctx, db, notifier, killswitch.Config{})
	g.NotifyScanComplete()

	// A panicking notifier must not crash the guard or lose the halt.
	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err != nil {
		t.Fatalf("Trip: %v", err)
	}
	if notifier.count() == 0 {
		t.Fatal("notifier must have been called on the trip")
	}
	if notifier.lastReason() == "" {
		t.Fatal("notifier must receive the halt reason")
	}
	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("halt must survive a panicking notifier")
	}
	if hs, _ := db.Halt(ctx); hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt = %q, want halted despite notifier panic", hs.Phase)
	}
}

// --- concurrency (-race) ----------------------------------------------------

func TestConcurrentSubmitAndTrip(t *testing.T) {
	g, _ := openGuard(t, killswitch.Config{OrderFailureThreshold: 1000})
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: hammer the hot path (CanSubmit + Reserve/Reconfirm).
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				g.CanSubmit("AAPL")
				r, _, _ := g.Reserve("AAPL")
				g.Reconfirm(r)
			}
		}()
	}
	// Writers: churn per-symbol blocks and report failures.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = g.Trip(ctx, killswitch.Symbol("AAPL"), "ambiguous", time.Now())
				g.ClearSymbol("AAPL")
				_ = g.ReportOrderFailure(ctx, time.Now())
			}
		}()
	}

	// A single global trip somewhere in the middle.
	if err := g.Trip(ctx, killswitch.Global(), "manual", time.Now()); err != nil {
		t.Fatalf("Trip global: %v", err)
	}
	close(stop)
	wg.Wait()

	if allowed, _ := g.CanSubmit("AAPL"); allowed {
		t.Fatal("guard must remain halted after the global trip")
	}
}

// mustGuard builds a guard with the gate open and returns it with a context.
func mustGuard(t *testing.T) (*killswitch.Guard, context.Context) {
	t.Helper()
	g, _ := openGuard(t, killswitch.Config{})
	return g, context.Background()
}

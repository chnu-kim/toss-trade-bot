package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// --- fakes -------------------------------------------------------------

// recorder captures the order of the load-bearing shutdown/boot steps. The
// sentinel contract is entirely an ORDERING contract (ADR-0012 Decision 1(c)),
// so "what happened before what" is the assertion, not just the end state.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) indexOf(e string) int {
	for i, got := range r.snapshot() {
		if got == e {
			return i
		}
	}
	return -1
}

type fakeSentinelStore struct {
	rec *recorder

	mu           sync.Mutex
	lifecycle    store.LifecycleState
	lifecycleErr error
	setErr       error
	halt         store.HaltState
	haltErr      error
	// ctxErrs records whether any call saw a cancelled context, which would
	// mean the shutdown path tried to write with the already-cancelled signal
	// context.
	sawCancelled bool
}

func newFakeStore(rec *recorder, lifecycle store.LifecycleState) *fakeSentinelStore {
	return &fakeSentinelStore{rec: rec, lifecycle: lifecycle, halt: store.HaltState{Phase: store.HaltNone}}
}

func (f *fakeSentinelStore) note(ctx context.Context, e string) {
	if ctx.Err() != nil {
		f.mu.Lock()
		f.sawCancelled = true
		f.mu.Unlock()
	}
	if f.rec != nil {
		f.rec.add(e)
	}
}

func (f *fakeSentinelStore) Lifecycle(ctx context.Context) (store.LifecycleState, error) {
	f.note(ctx, "lifecycle-read")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lifecycleErr != nil {
		return "", f.lifecycleErr
	}
	return f.lifecycle, nil
}

func (f *fakeSentinelStore) SetLifecycle(ctx context.Context, s store.LifecycleState) (err error) {
	f.note(ctx, "set-lifecycle-"+string(s))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.lifecycle = s
	return nil
}

func (f *fakeSentinelStore) Halt(ctx context.Context) (store.HaltState, error) {
	f.note(ctx, "halt-read")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.haltErr != nil {
		return store.HaltState{}, f.haltErr
	}
	return f.halt, nil
}

func (f *fakeSentinelStore) current() store.LifecycleState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lifecycle
}

type fakeGuard struct {
	rec *recorder

	mu            sync.Mutex
	bootHalted    bool
	unpersisted   bool
	finalizeErr   error
	finalizeCalls int
	// finalizeClears models the real killswitch: a successful FinalizePendingHalt
	// clears the latch, but a bootHalt-only guard stays unpersisted forever
	// (bootHalt is in-memory and NOT finalizable).
	finalizeClears bool
}

func (g *fakeGuard) BootHalt() {
	if g.rec != nil {
		g.rec.add("boot-halt")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bootHalted = true
}

func (g *fakeGuard) HasUnpersistedPendingHalt() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.unpersisted
}

func (g *fakeGuard) FinalizePendingHalt(ctx context.Context) error {
	if g.rec != nil {
		g.rec.add("finalize-pending-halt")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.finalizeCalls++
	if g.finalizeErr != nil {
		return g.finalizeErr
	}
	if g.finalizeClears {
		g.unpersisted = false
	}
	return nil
}

func (g *fakeGuard) halted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bootHalted
}

// --- boot --------------------------------------------------------------

// TestBootSentinel_CleanPreviousRunTrustsDurable is crash-timing case (i): the
// previous run wrote clean, so this boot trusts the durable state and does NOT
// come up conservatively halted.
func TestBootSentinel_CleanPreviousRunTrustsDurable(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleClean)
	g := &fakeGuard{rec: rec}

	d := BootSentinel(context.Background(), st, g, testLogger())

	if d.Conservative {
		t.Fatalf("clean previous run must not boot conservatively halted: %+v", d)
	}
	if g.halted() {
		t.Fatal("BootHalt must not be called after a clean previous shutdown")
	}
	if d.Previous != store.LifecycleClean {
		t.Fatalf("Previous = %q, want clean", d.Previous)
	}
	if got := st.current(); got != store.LifecycleRunning {
		t.Fatalf("sentinel after boot = %q, want running", got)
	}
}

// TestBootSentinel_UncleanPreviousRunBootsHalted is crash-timing case (ii): the
// sentinel still reads running, meaning the previous run never certified itself
// clean (crash / kill / store error / unpersisted pending halt). A
// non-reconstructable global halt cannot be excluded, so the boot is
// conservatively halted until a manual clear (ADR-0012 Decision 1(c)).
func TestBootSentinel_UncleanPreviousRunBootsHalted(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	d := BootSentinel(context.Background(), st, g, testLogger())

	if !d.Conservative {
		t.Fatal("unclean previous run must boot conservatively halted")
	}
	if !g.halted() {
		t.Fatal("BootHalt must be called on an unclean boot")
	}
	if got := st.current(); got != store.LifecycleRunning {
		t.Fatalf("sentinel after boot = %q, want running", got)
	}
}

// TestBootSentinel_ReadsBeforeFlippingToRunning pins the ordering invariant that
// the previous value must be OBSERVED before it is overwritten. A flip-first
// implementation would read its own running and never detect an unclean run.
func TestBootSentinel_ReadsBeforeFlippingToRunning(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	BootSentinel(context.Background(), st, g, testLogger())

	read, write := rec.indexOf("lifecycle-read"), rec.indexOf("set-lifecycle-running")
	if read == -1 || write == -1 {
		t.Fatalf("missing sentinel steps: %v", rec.snapshot())
	}
	if read > write {
		t.Fatalf("sentinel read must precede the running flip; got %v", rec.snapshot())
	}
}

// TestBootSentinel_SetLifecycleFailureBootsHalted covers the ADR-0012 rule that a
// failed boot write is itself a fail-closed boot: without a durable running
// marker this run cannot be detected as unclean later, so it must not trade.
func TestBootSentinel_SetLifecycleFailureBootsHalted(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleClean)
	st.setErr = errors.New("disk on fire")
	g := &fakeGuard{rec: rec}

	d := BootSentinel(context.Background(), st, g, testLogger())

	if !d.Conservative || !g.halted() {
		t.Fatalf("a failed running flip must boot conservatively halted: %+v", d)
	}
	if d.Err == nil {
		t.Fatal("the sentinel write error must be reported for logging")
	}
}

// TestBootSentinel_ReadFailureBootsHalted: an unreadable sentinel cannot prove a
// clean previous shutdown, and "state unknown" is blocked (ADR-0004 point 3).
func TestBootSentinel_ReadFailureBootsHalted(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleClean)
	st.lifecycleErr = errors.New("halt row missing")
	g := &fakeGuard{rec: rec}

	d := BootSentinel(context.Background(), st, g, testLogger())

	if !d.Conservative || !g.halted() {
		t.Fatalf("an unreadable sentinel must boot conservatively halted: %+v", d)
	}
	// Even when the read failed, the run must still be marked running so the
	// NEXT boot can detect this run as unclean.
	if rec.indexOf("set-lifecycle-running") == -1 {
		t.Fatalf("running flip must still be attempted after a read failure: %v", rec.snapshot())
	}
}

// TestBoot_FlipsRunningBeforeStartingRecovery is the core boot ordering
// invariant (ADR-0012 Decision 1(c) sentinel fail-open #1): the running flip
// happens before the recovery scan that eventually opens the replay gate. If
// recovery could start first, a crash inside the scan window would leave a
// stale clean behind and the next boot would trust it.
func TestBoot_FlipsRunningBeforeStartingRecovery(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleClean)
	g := &fakeGuard{rec: rec}

	started := make(chan struct{})
	Boot(context.Background(), st, g, testLogger(), func() {
		rec.add("start-recovery")
		close(started)
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery was never started")
	}

	flip, start := rec.indexOf("set-lifecycle-running"), rec.indexOf("start-recovery")
	if flip == -1 || start == -1 {
		t.Fatalf("missing boot steps: %v", rec.snapshot())
	}
	if flip > start {
		t.Fatalf("running flip must precede recovery start; got %v", rec.snapshot())
	}
}

// --- shutdown ----------------------------------------------------------

// TestShutdownSentinel_WritesCleanOnHealthyExit is the happy path that makes
// crash-timing case (i) reachable at all.
func TestShutdownSentinel_WritesCleanOnHealthyExit(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

	if !d.WroteClean {
		t.Fatalf("a healthy drained shutdown must write clean: %+v", d)
	}
	if got := st.current(); got != store.LifecycleClean {
		t.Fatalf("sentinel = %q, want clean", got)
	}
}

// TestShutdownSentinel_RefusesCleanWhenPendingHaltCannotPersist is crash-timing
// case (iv): a run whose global halt never reached the store must not certify
// itself clean, or the next boot would silently reopen (sentinel fail-open #2).
func TestShutdownSentinel_RefusesCleanWhenPendingHaltCannotPersist(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec, unpersisted: true, finalizeErr: errors.New("store down")}

	d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

	if d.WroteClean {
		t.Fatal("clean must not be written when a pending halt failed to persist")
	}
	if got := st.current(); got != store.LifecycleRunning {
		t.Fatalf("sentinel = %q, want running (left unclean)", got)
	}
	if g.finalizeCalls != 1 {
		t.Fatalf("FinalizePendingHalt calls = %d, want 1", g.finalizeCalls)
	}
}

// TestShutdownSentinel_RefusesCleanWhenLatchSurvivesFinalize closes the
// bootHalt trap: killswitch reports bootHalt through HasUnpersistedPendingHalt,
// but FinalizePendingHalt is a deliberate no-op for it. A "finalize returned
// nil ⇒ eligible" reading would let a boot-halted run certify itself clean and
// reopen on the next boot.
func TestShutdownSentinel_RefusesCleanWhenLatchSurvivesFinalize(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	// finalizeClears=false models bootHalt: finalize succeeds (no-op) yet the
	// guard still reports an unpersisted halt.
	g := &fakeGuard{rec: rec, unpersisted: true, finalizeClears: false}

	d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

	if d.WroteClean {
		t.Fatal("clean must not be written while the guard still reports an unpersisted halt")
	}
	if got := st.current(); got != store.LifecycleRunning {
		t.Fatalf("sentinel = %q, want running", got)
	}
}

// TestShutdownSentinel_FinalizedLatchStillBlocksViaDurableHalt: once the latch
// is finalized the halt IS durable, so the durable read (case ii) refuses the
// clean instead. Either way an operator-clearable halt survives the restart.
func TestShutdownSentinel_FinalizedLatchStillBlocksViaDurableHalt(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	st.halt = store.HaltState{Phase: store.HaltHalted, Reason: "token refresh"}
	g := &fakeGuard{rec: rec, unpersisted: true, finalizeClears: true}

	d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

	if d.WroteClean {
		t.Fatal("clean must not be written over a durable halt")
	}
	if g.finalizeCalls != 1 {
		t.Fatalf("FinalizePendingHalt calls = %d, want 1", g.finalizeCalls)
	}
}

// TestShutdownSentinel_RefusesCleanOnDurablePendingHalt is the read-only arm of
// the eligibility rule: a durable pending/halted global halt refuses the clean
// with no write entry point at all.
func TestShutdownSentinel_RefusesCleanOnDurablePendingHalt(t *testing.T) {
	for _, phase := range []store.HaltPhase{store.HaltPending, store.HaltHalted} {
		t.Run(string(phase), func(t *testing.T) {
			st := newFakeStore(nil, store.LifecycleRunning)
			st.halt = store.HaltState{Phase: phase}
			g := &fakeGuard{}

			d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

			if d.WroteClean {
				t.Fatalf("clean must not be written with a durable %s halt", phase)
			}
		})
	}
}

// TestShutdownSentinel_RefusesCleanWhenHaltUnreadable: an unreadable halt phase
// cannot prove the absence of an unresolved halt (ADR-0004 point 3).
func TestShutdownSentinel_RefusesCleanWhenHaltUnreadable(t *testing.T) {
	st := newFakeStore(nil, store.LifecycleRunning)
	st.haltErr = errors.New("store closed")
	g := &fakeGuard{}

	d := ShutdownSentinel(context.Background(), st, g, true, testLogger())

	if d.WroteClean {
		t.Fatal("clean must not be written when the halt phase is unreadable")
	}
	if d.Err == nil {
		t.Fatal("the halt read error must be reported")
	}
}

// TestShutdownSentinel_RefusesCleanWhenNotDrained: a shutdown that timed out
// with goroutines still running is not a proven-normal exit path — an in-flight
// trip could still be mutating state, so the run stays unclean.
func TestShutdownSentinel_RefusesCleanWhenNotDrained(t *testing.T) {
	st := newFakeStore(nil, store.LifecycleRunning)
	g := &fakeGuard{}

	d := ShutdownSentinel(context.Background(), st, g, false, testLogger())

	if d.WroteClean {
		t.Fatal("clean must not be written after a drain timeout")
	}
}

// TestShutdown_WritesCleanBeforeClosingSinks is the AC ordering assertion: the
// conditional clean lands while audit and store are still open, then the sinks
// close in order. Writing it after the store closed would always fail.
func TestShutdown_WritesCleanBeforeClosingSinks(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	d := Shutdown(context.Background(), ShutdownPlan{
		Sentinel: st,
		Guard:    g,
		Drained:  true,
		Logger:   testLogger(),
		Closers: []NamedCloser{
			{Name: "audit", Close: func() error { rec.add("close-audit"); return nil }},
			{Name: "store", Close: func() error { rec.add("close-store"); return nil }},
		},
	})

	if !d.WroteClean {
		t.Fatalf("expected a clean sentinel: %+v", d)
	}
	events := rec.snapshot()
	clean, a, s := rec.indexOf("set-lifecycle-clean"), rec.indexOf("close-audit"), rec.indexOf("close-store")
	if clean == -1 || a == -1 || s == -1 {
		t.Fatalf("missing shutdown steps: %v", events)
	}
	if !(clean < a && a < s) {
		t.Fatalf("want clean < close-audit < close-store, got %v", events)
	}
}

// TestShutdown_ClosesSinksEvenWhenCleanRefused: refusing the clean is a safety
// decision, not a reason to leak file handles or skip the audit sink's own
// flush. Everything still closes, in order.
func TestShutdown_ClosesSinksEvenWhenCleanRefused(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec, unpersisted: true, finalizeErr: errors.New("nope")}

	d := Shutdown(context.Background(), ShutdownPlan{
		Sentinel: st,
		Guard:    g,
		Drained:  true,
		Logger:   testLogger(),
		Closers: []NamedCloser{
			{Name: "audit", Close: func() error { rec.add("close-audit"); return nil }},
			{Name: "store", Close: func() error { rec.add("close-store"); return nil }},
		},
	})

	if d.WroteClean {
		t.Fatal("clean must have been refused")
	}
	if rec.indexOf("close-audit") == -1 || rec.indexOf("close-store") == -1 {
		t.Fatalf("sinks must still close: %v", rec.snapshot())
	}
}

// TestShutdown_UsesDetachedContext is the trap that makes the whole shutdown
// path a no-op if missed: the signal context is ALREADY cancelled by the time
// shutdown runs, so every durable write (finalize, clean) would fail with
// context.Canceled and silently leave the run unclean forever.
func TestShutdown_UsesDetachedContext(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := Shutdown(ctx, ShutdownPlan{
		Sentinel: st,
		Guard:    g,
		Drained:  true,
		Logger:   testLogger(),
	})

	if !d.WroteClean {
		t.Fatalf("shutdown must survive an already-cancelled signal context: %+v", d)
	}
	st.mu.Lock()
	saw := st.sawCancelled
	st.mu.Unlock()
	if saw {
		t.Fatal("shutdown passed a cancelled context to a durable write")
	}
}

// TestShutdown_ClosersRunEvenIfOneFails keeps a failing audit close from
// stranding the store handle.
func TestShutdown_ClosersRunEvenIfOneFails(t *testing.T) {
	rec := &recorder{}
	st := newFakeStore(rec, store.LifecycleRunning)
	g := &fakeGuard{rec: rec}

	d := Shutdown(context.Background(), ShutdownPlan{
		Sentinel: st,
		Guard:    g,
		Drained:  true,
		Logger:   testLogger(),
		Closers: []NamedCloser{
			{Name: "audit", Close: func() error { rec.add("close-audit"); return errors.New("flush failed") }},
			{Name: "store", Close: func() error { rec.add("close-store"); return nil }},
		},
	})

	if rec.indexOf("close-store") == -1 {
		t.Fatalf("store must close even after an audit close failure: %v", rec.snapshot())
	}
	if d.Err == nil || !strings.Contains(d.Err.Error(), "flush failed") {
		t.Fatalf("close error must be reported, got %v", d.Err)
	}
}

// testLogger discards output: these tests assert behaviour, not log text, and a
// noisy logger would drown a real failure.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// This file is the ADR-0012 Decision 1(c) crash-timing suite. Unlike
// sentinel_test.go (fakes, for error injection), every case here runs against a
// REAL store on disk and a REAL kill switch, and models a process boundary by
// closing the store and reopening the same file. A "crash" is simply a run that
// closes without going through Shutdown.
//
// The four cases are the ones ADR-0012 booked as mandatory follow-ups:
//
//	(i)   clean shutdown  → next boot trusts durable state
//	(ii)  unclean run     → next boot is conservatively halted until a manual clear
//	(iii) stale clean     → the boot flip invalidates it, so a later crash is still seen
//	(iv)  pending halt that never persisted → the run refuses to certify itself clean

const (
	testOrderFailureThreshold = 3
	testTokenRefreshThreshold = 3
	testTokenRefreshWindow    = 15 * time.Minute
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testKillswitchConfig() killswitch.Config {
	return killswitch.Config{
		OrderFailureThreshold: testOrderFailureThreshold,
		TokenRefreshThreshold: testTokenRefreshThreshold,
		TokenRefreshWindow:    testTokenRefreshWindow,
	}
}

// run models one process lifetime over a fixed database path.
type run struct {
	t     *testing.T
	db    *store.DB
	guard *killswitch.Switch
}

// startRun opens the store at path and builds a kill switch over it, exactly as
// the boot sequence does before the sentinel judgment.
func startRun(t *testing.T, path string) *run {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	guard, err := killswitch.New(context.Background(), db, testKillswitchConfig())
	if err != nil {
		t.Fatalf("killswitch.New: %v", err)
	}
	return &run{t: t, db: db, guard: guard}
}

// boot runs the real boot-time sentinel judgment and then opens the replay gate
// as a successful reconciler boot scan would.
func (r *run) boot() BootDecision {
	r.t.Helper()
	d := Boot(context.Background(), r.db, r.guard, discardLogger(), func() {
		// Stand-in for reconciler.BootScan succeeding: the gate opens only
		// after the scan, and only after the sentinel flip that Boot owns.
		r.guard.NotifyScanComplete()
	})
	return d
}

// crash ends the run WITHOUT the shutdown sentinel — the kill -9 / panic /
// power-loss path. The sentinel is left at running.
func (r *run) crash() {
	r.t.Helper()
	if err := r.db.Close(); err != nil {
		r.t.Fatalf("close store: %v", err)
	}
}

// shutdown ends the run through the real graceful path.
func (r *run) shutdown() ShutdownDecision {
	r.t.Helper()
	d := Shutdown(context.Background(), ShutdownPlan{
		Sentinel: r.db,
		Guard:    r.guard,
		Drained:  true,
		Logger:   discardLogger(),
		Closers:  []NamedCloser{{Name: "store", Close: r.db.Close}},
	})
	return d
}

func (r *run) canSubmit() bool {
	allowed, _ := r.guard.CanSubmit("005930")
	return allowed
}

// TestCrashTiming_CleanShutdownIsTrustedOnRestart is case (i). It also pins the
// gate contract the acceptance criteria ask for: submission is blocked before
// the scan completes and allowed after.
func TestCrashTiming_CleanShutdownIsTrustedOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	// Run 1: a fresh store starts at running, so the very first boot is
	// conservatively halted (initial-authorization gate). An operator clears it.
	r1 := startRun(t, path)
	if d := r1.boot(); !d.Conservative {
		t.Fatalf("first-ever boot should be conservatively halted, got %+v", d)
	}
	if r1.canSubmit() {
		t.Fatal("a conservatively halted boot must block submission")
	}
	if err := r1.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("operator ClearHalt: %v", err)
	}
	if !r1.canSubmit() {
		t.Fatal("after a manual clear and a completed scan, submission must be allowed")
	}
	if d := r1.shutdown(); !d.WroteClean {
		t.Fatalf("a healthy run must certify itself clean: %+v", d)
	}

	// Run 2: the sentinel says the previous run ended clean, so durable state
	// is trusted — no conservative halt, and the gate opens on scan completion.
	r2 := startRun(t, path)
	defer r2.db.Close()
	d := r2.boot()
	if d.Conservative {
		t.Fatalf("a boot after a clean shutdown must trust durable state: %+v", d)
	}
	if d.Previous != store.LifecycleClean {
		t.Fatalf("Previous = %q, want clean", d.Previous)
	}
	if !r2.canSubmit() {
		t.Fatal("submission must be allowed after a clean restart and a completed scan")
	}
}

// TestCrashTiming_GateIsShutUntilScanCompletes pins the replay-gate half of the
// boot contract on its own: the flip and the boot decision alone do not open
// submission — only the scan does (ADR-0004 point 3).
func TestCrashTiming_GateIsShutUntilScanCompletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	// Get past the initial-authorization gate so the ONLY thing left blocking
	// is the replay gate.
	r0 := startRun(t, path)
	r0.boot()
	if err := r0.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if d := r0.shutdown(); !d.WroteClean {
		t.Fatalf("expected clean shutdown: %+v", d)
	}

	r1 := startRun(t, path)
	defer r1.db.Close()

	scanned := false
	d := Boot(context.Background(), r1.db, r1.guard, discardLogger(), func() {
		// Observed at the instant recovery starts: the sentinel is already
		// running, and the gate is still shut.
		if got, err := r1.db.Lifecycle(context.Background()); err != nil || got != store.LifecycleRunning {
			t.Errorf("sentinel at recovery start = %q (err %v), want running", got, err)
		}
		if r1.canSubmit() {
			t.Error("submission must be blocked before the scan completes")
		}
		scanned = true
		r1.guard.NotifyScanComplete()
	})

	if !scanned {
		t.Fatal("recovery never ran")
	}
	if d.Conservative {
		t.Fatalf("boot after a clean shutdown should not be conservative: %+v", d)
	}
	if !r1.canSubmit() {
		t.Fatal("submission must be allowed once the scan completes")
	}
}

// TestCrashTiming_UncleanRunBootsConservativelyHalted is case (ii): a crashed
// run leaves the sentinel at running, and the next boot stays fail-closed even
// after the scan completes — only a manual clear reopens it (ADR-0004 point 6).
func TestCrashTiming_UncleanRunBootsConservativelyHalted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	r1 := startRun(t, path)
	r1.boot()
	if err := r1.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	r1.crash() // no shutdown sentinel: this run never certifies itself

	r2 := startRun(t, path)
	defer r2.db.Close()
	d := r2.boot()

	if !d.Conservative {
		t.Fatalf("a boot after an unclean run must be conservatively halted: %+v", d)
	}
	if r2.canSubmit() {
		t.Fatal("a conservatively halted boot must stay blocked even after the scan completes")
	}
	if err := r2.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("operator ClearHalt: %v", err)
	}
	if !r2.canSubmit() {
		t.Fatal("a manual clear must reopen submission")
	}
}

// TestCrashTiming_StaleCleanIsInvalidatedByTheBootFlip is case (iii), the
// subtlest one. Run 1 ends clean. Run 2 boots (flipping the sentinel to
// running) and then CRASHES. If the boot flip were missing or ran after the
// gate opened, run 2's crash would be masked by run 1's stale clean and run 3
// would happily trust durable state.
func TestCrashTiming_StaleCleanIsInvalidatedByTheBootFlip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	// Run 1: ends clean, leaving a clean marker on disk.
	r1 := startRun(t, path)
	r1.boot()
	if err := r1.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if d := r1.shutdown(); !d.WroteClean {
		t.Fatalf("run 1 should have ended clean: %+v", d)
	}
	if got, _ := readLifecycle(t, path); got != store.LifecycleClean {
		t.Fatalf("sentinel after run 1 = %q, want clean", got)
	}

	// Run 2: boots over that clean marker, then crashes.
	r2 := startRun(t, path)
	if d := r2.boot(); d.Conservative {
		t.Fatalf("run 2 should have trusted run 1's clean marker: %+v", d)
	}
	r2.crash()

	// The stale clean must be gone: run 2's boot flip overwrote it.
	if got, _ := readLifecycle(t, path); got != store.LifecycleRunning {
		t.Fatalf("sentinel after run 2's crash = %q, want running (stale clean must not survive)", got)
	}

	// Run 3: sees run 2's crash, not run 1's clean.
	r3 := startRun(t, path)
	defer r3.db.Close()
	d := r3.boot()
	if !d.Conservative {
		t.Fatalf("run 3 must see run 2 as unclean, not trust run 1's stale clean: %+v", d)
	}
	if r3.canSubmit() {
		t.Fatal("run 3 must stay blocked")
	}
}

// TestCrashTiming_UnpersistedPendingHaltRefusesClean is case (iv). The kill
// switch trips on a token-refresh failure while the store rejects the durable
// write, so the halt exists only in memory. The run then shuts down gracefully.
// It must NOT certify itself clean — otherwise the halt would vanish (it is
// non-reconstructable: no journal replay can re-derive a token failure) and the
// next boot would reopen submission, which is exactly ADR-0004 point 6's
// forbidden auto-resume.
func TestCrashTiming_UnpersistedPendingHaltRefusesClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")

	// Get past the initial-authorization gate first, so the refusal we observe
	// is caused by the unpersisted halt and not by a leftover boot halt.
	r0 := startRun(t, path)
	r0.boot()
	if err := r0.guard.ClearHalt(context.Background()); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}
	if d := r0.shutdown(); !d.WroteClean {
		t.Fatalf("setup run should end clean: %+v", d)
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	// The kill switch sees a store whose halt writes fail; the sentinel still
	// sees the healthy store, which isolates the eligibility rule from a
	// blanket "everything is broken" outcome.
	broken := &haltWriteFailingStore{DB: db}
	guard, err := killswitch.New(context.Background(), broken, testKillswitchConfig())
	if err != nil {
		t.Fatalf("killswitch.New: %v", err)
	}
	Boot(context.Background(), db, guard, discardLogger(), guard.NotifyScanComplete)

	// A trip whose durable write fails latches in memory only.
	if err := guard.ReportTokenRefreshFailure(context.Background(), time.Now()); err == nil {
		t.Fatal("expected the durable counter write to fail")
	}
	if !guard.HasUnpersistedPendingHalt() {
		t.Fatal("the failed trip must latch as an unpersisted pending halt")
	}
	if allowed, _ := guard.CanSubmit("005930"); allowed {
		t.Fatal("an unpersisted pending halt must block submission")
	}

	d := Shutdown(context.Background(), ShutdownPlan{
		Sentinel: db,
		Guard:    guard,
		Drained:  true,
		Logger:   discardLogger(),
	})
	if d.WroteClean {
		t.Fatal("a run holding an unpersistable global halt must not certify itself clean")
	}
	if got, err := db.Lifecycle(context.Background()); err != nil || got != store.LifecycleRunning {
		t.Fatalf("sentinel = %q (err %v), want running", got, err)
	}

	// And the next boot inherits the conservative halt, which is the whole
	// point of refusing the clean.
	db.Close()
	r2 := startRun(t, path)
	defer r2.db.Close()
	if d := r2.boot(); !d.Conservative {
		t.Fatalf("the next boot must be conservatively halted: %+v", d)
	}
	if r2.canSubmit() {
		t.Fatal("the next boot must stay blocked")
	}
}

// readLifecycle reopens the store just to read the sentinel, so a test can
// inspect the on-disk value between simulated process lifetimes.
func readLifecycle(t *testing.T, path string) (store.LifecycleState, error) {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	return db.Lifecycle(context.Background())
}

// haltWriteFailingStore is a real store whose halt/counter WRITES fail, modelling
// a store that is down exactly when the kill switch needs to persist a trip.
// Reads still work, so the failure is narrowly the durable-write arm ADR-0012
// Decision 1 calls out.
type haltWriteFailingStore struct {
	*store.DB
}

var errStoreDown = errors.New("test: durable halt write unavailable")

func (s *haltWriteFailingStore) Atomically(ctx context.Context, fn func(tx store.Tx) error) error {
	return errStoreDown
}
func (s *haltWriteFailingStore) MarkHaltPending(ctx context.Context, reason string) error {
	return errStoreDown
}
func (s *haltWriteFailingStore) TripHalt(ctx context.Context, reason string) error {
	return errStoreDown
}
func (s *haltWriteFailingStore) SetCounter(ctx context.Context, c store.Counter) error {
	return errStoreDown
}

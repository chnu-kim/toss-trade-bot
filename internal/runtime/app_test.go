package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/config"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		BaseURL:                   "https://openapi.tossinvest.com",
		ClientID:                  "test-id",
		ClientSecret:              config.Secret("test-secret"),
		AccountSeq:                "42",
		AccountSeqNum:             42,
		StorePath:                 filepath.Join(dir, "state", "bot.db"),
		AuditDir:                  filepath.Join(dir, "audit"),
		OrderFailureThreshold:     config.DefaultOrderFailureThreshold,
		TokenRefreshThreshold:     config.DefaultTokenRefreshThreshold,
		TokenRefreshWindow:        config.DefaultTokenRefreshWindow,
		AmbiguousBacklogThreshold: config.DefaultAmbiguousBacklogThreshold,
		SettleWindow:              config.DefaultSettleWindow,
		ReevalInterval:            config.DefaultReevalInterval,
		ShutdownTimeout:           2 * time.Second,
	}
}

func TestAssemble_BuildsEveryComponent(t *testing.T) {
	app, err := Assemble(context.Background(), testConfig(t), discardLogger())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer app.Close()

	if app.Guard() == nil {
		t.Fatal("kill switch not assembled")
	}
	if app.Submitter() == nil {
		t.Fatal("submit path not assembled (it must be wired even while dormant)")
	}
	// A fresh store's sentinel is running, so the guard must already be
	// blocked before the boot sequence even runs.
	if allowed, _ := app.Guard().CanSubmit("005930"); allowed {
		t.Fatal("submission must be blocked before boot completes")
	}
}

// TestAssemble_CreatesDurableDirectories: an unattended process must come up on
// a clean host without a human pre-creating its state directories.
func TestAssemble_CreatesDurableDirectories(t *testing.T) {
	cfg := testConfig(t)
	app, err := Assemble(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer app.Close()

	if _, err := os.Stat(filepath.Dir(cfg.StorePath)); err != nil {
		t.Fatalf("store directory not created: %v", err)
	}
	if _, err := os.Stat(cfg.AuditDir); err != nil {
		t.Fatalf("audit directory not created: %v", err)
	}
}

// TestAssemble_RejectsMissingAccountSeq: every account-scoped call needs it, and
// a zero would silently target no account at all.
func TestAssemble_RejectsMissingAccountSeq(t *testing.T) {
	cfg := testConfig(t)
	cfg.AccountSeqNum = 0

	if _, err := Assemble(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("Assemble must reject a missing accountSeq")
	}
}

// TestAssemble_ReleasesStoreOnLaterFailure keeps a half-built app from leaking
// the database handle. On a restart-happy unattended host, a leaked write
// connection would make the next attempt fail too.
func TestAssemble_ReleasesStoreOnLaterFailure(t *testing.T) {
	cfg := testConfig(t)
	// Make the audit directory un-creatable by putting a regular file where it
	// must live, so assembly fails AFTER the store has been opened.
	if err := os.MkdirAll(filepath.Dir(cfg.AuditDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.AuditDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Assemble(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatal("Assemble must fail when the audit sink cannot be opened")
	}

	// The store must have been released: reopening it must succeed.
	db, err := store.Open(cfg.StorePath)
	if err != nil {
		t.Fatalf("store was not released by the failed assembly: %v", err)
	}
	db.Close()
}

// TestApp_RunBootsHaltedAndShutsDownCleanly is the end-to-end skeleton contract:
// Run boots, blocks until the context is cancelled, and returns having decided
// the sentinel. A first boot is conservatively halted, so it must NOT certify
// itself clean on the way out (the boot halt is in-memory and unpersistable).
func TestApp_RunBootsHaltedAndShutsDownCleanly(t *testing.T) {
	cfg := testConfig(t)
	app, err := Assemble(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	// Give the boot sequence time to land the running flip.
	waitFor(t, 3*time.Second, func() bool {
		lc, err := readLifecycle(t, cfg.StorePath)
		return err == nil && lc == store.LifecycleRunning
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// A boot-halted run must leave the sentinel unclean so the next boot stays
	// blocked too.
	lc, err := readLifecycle(t, cfg.StorePath)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if lc != store.LifecycleRunning {
		t.Fatalf("sentinel = %q, want running (a boot-halted run may not certify itself clean)", lc)
	}
}

// TestApp_RunWritesCleanAfterAnOperatorClear is the same path once the halt has
// been cleared: now the run IS eligible and the next boot may trust it.
func TestApp_RunWritesCleanAfterAnOperatorClear(t *testing.T) {
	cfg := testConfig(t)
	app, err := Assemble(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	waitFor(t, 3*time.Second, func() bool {
		lc, err := readLifecycle(t, cfg.StorePath)
		return err == nil && lc == store.LifecycleRunning
	})

	// The operator authorises this deployment.
	if err := app.Guard().ClearHalt(context.Background()); err != nil {
		t.Fatalf("ClearHalt: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	lc, err := readLifecycle(t, cfg.StorePath)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if lc != store.LifecycleClean {
		t.Fatalf("sentinel = %q, want clean", lc)
	}
}

// --- token-refresh escalation adapter ----------------------------------

type recordingTokenGuard struct {
	mu    sync.Mutex
	calls []time.Time
	err   error
	// sawCancelled records a report that arrived with an already-cancelled
	// context, which would fail the durable write for no good reason.
	sawCancelled bool
}

func (g *recordingTokenGuard) ReportTokenRefreshFailure(ctx context.Context, at time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		g.sawCancelled = true
	}
	g.calls = append(g.calls, at)
	return g.err
}

// TestTokenFailureReporter_ReportsThroughTheCountedSeam pins that the token hook
// escalates via the COUNTED report, not a direct global trip. A direct trip
// would bypass the threshold/window contract the kill switch owns (#32) and
// halt the bot on a single transient blip.
func TestTokenFailureReporter_ReportsThroughTheCountedSeam(t *testing.T) {
	g := &recordingTokenGuard{}
	report := tokenFailureReporter(context.Background(), g, discardLogger())

	at := time.Now()
	report(at)

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) != 1 {
		t.Fatalf("reports = %d, want 1", len(g.calls))
	}
	if !g.calls[0].Equal(at) {
		t.Fatalf("occurredAt = %s, want %s", g.calls[0], at)
	}
}

// TestTokenFailureReporter_SurvivesGuardError: a failing report is already
// fail-closed inside the kill switch (it latches). The reporter must log it, not
// panic out of the token manager's goroutine.
func TestTokenFailureReporter_SurvivesGuardError(t *testing.T) {
	g := &recordingTokenGuard{err: errors.New("store down")}
	report := tokenFailureReporter(context.Background(), g, discardLogger())

	report(time.Now()) // must not panic

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) != 1 {
		t.Fatalf("reports = %d, want 1", len(g.calls))
	}
}

// TestTokenFailureReporter_DetachesFromTheLifetimeContext is a fail-closed-in-
// the-wrong-direction guard. If the report inherited the cancelled shutdown
// context, a token failure racing shutdown would fail its durable write, latch
// an unpersisted halt, and force EVERY later boot to come up halted — an
// operator-visible outage caused by nothing but a shutdown race.
func TestTokenFailureReporter_DetachesFromTheLifetimeContext(t *testing.T) {
	g := &recordingTokenGuard{}
	ctx, cancel := context.WithCancel(context.Background())
	report := tokenFailureReporter(ctx, g, discardLogger())
	cancel()

	report(time.Now())

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sawCancelled {
		t.Fatal("the token escalation must not inherit the cancelled lifetime context")
	}
	if len(g.calls) != 1 {
		t.Fatalf("reports = %d, want 1", len(g.calls))
	}
}

// --- notifier ----------------------------------------------------------

// TestSlogNotifier_LogsTrips: the notification channel is undecided, so a halt
// is promoted to the log — the only post-mortem surface an unattended process
// has.
func TestSlogNotifier_LogsTrips(t *testing.T) {
	var buf syncBuffer
	n := slogNotifier{logger: NewLogger(&buf)}

	n.HaltTripped("token refresh failure")

	if got := buf.String(); !strings.Contains(got, "token refresh failure") {
		t.Fatalf("halt reason missing from the log: %s", got)
	}
}

// TestSlogNotifier_IsNonBlocking: killswitch invokes the notifier while holding
// its transition lock, so the adapter must return promptly and never call back
// into the guard.
func TestSlogNotifier_IsNonBlocking(t *testing.T) {
	var buf syncBuffer
	n := slogNotifier{logger: NewLogger(&buf)}

	done := make(chan struct{})
	go func() {
		n.HaltTripped("reason")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notifier blocked")
	}
}

// --- helpers -----------------------------------------------------------

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// TestApp_RunRefusesBootWhenTheSentinelFlipFails is the process-level half of
// the fatal rule: without a durable running marker the run is invisible to the
// next boot's crash detection, so Run must return an error (non-zero exit, which
// makes the supervisor retry) instead of trading on a stale clean marker.
func TestApp_RunRefusesBootWhenTheSentinelFlipFails(t *testing.T) {
	cfg := testConfig(t)
	app, err := Assemble(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Swap in a sentinel seam whose running flip fails, leaving the real store
	// (and therefore the previous marker) untouched.
	app.sentinel = &flipFailingSentinel{SentinelStore: app.db}

	// Run must return promptly WITHOUT waiting for a shutdown signal.
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run must fail when the running flip never persisted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run blocked instead of refusing the boot")
	}
}

// flipFailingSentinel fails only the running flip; reads and the clean write
// pass through, so the test isolates exactly the durable-marker failure.
type flipFailingSentinel struct {
	SentinelStore
}

func (s *flipFailingSentinel) SetLifecycle(ctx context.Context, ls store.LifecycleState) error {
	if ls == store.LifecycleRunning {
		return errors.New("test: sentinel write unavailable")
	}
	return s.SentinelStore.SetLifecycle(ctx, ls)
}

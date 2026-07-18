package reconciler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
	"github.com/chnu-kim/toss-trade-bot/internal/toss"
	_ "modernc.org/sqlite" // read-only verification handle on the real store file
)

// The tests below run against the REAL store engine in a temp dir, the REAL
// audit writer in a temp dir, and the REAL kill switch — the money-safety
// ordering contracts (count-before-resolve, durable-before-visible, the ack ↔
// prune-gate wiring) only mean anything against the real durable behaviour.
// Only the Toss API is faked, either with an httptest server behind a real
// *toss.Client or with a stub that can inject failures deterministically.

// --- fixtures ---------------------------------------------------------------

const testAccountSeq int64 = 4242

// baseTime anchors the injected clock. It must track wall time rather than be a
// hard-coded instant: the store stamps journal markers with time.Now() and the
// reconciler compares those stamps against the injected clock, so an injected
// clock in the past would make every intent look like it was created in the
// future. Anchoring here and then Advance()-ing keeps every window boundary
// exact without any sleeping.
var baseTime = time.Now()

// testClock is an injectable clock. It is mutex-guarded because the ticker tests
// advance it from the test goroutine while the reconciler loop reads it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock { return &testClock{t: baseTime} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// discardLogger keeps test output readable; the code under test logs heavily by
// design (unattended operation makes logs the only diagnosis surface).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openStore opens a real store in a temp dir and returns it with its path, so
// assertions can read the durable rows back independently of the seam under
// test.
func openStore(t *testing.T) (*store.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// intentRow is the durable truth about one intent, read straight from SQLite so
// the assertion does not go through the same seam the code under test used.
type intentRow struct {
	resolution   string
	resolved     bool
	fullyAudited bool
}

func readIntentRow(t *testing.T, path, intentID string) (intentRow, bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open sqlite for verification: %v", err)
	}
	defer db.Close()

	var (
		resolution   string
		resolvedAt   sql.NullInt64
		fullyAudited sql.NullInt64
	)
	err = db.QueryRow(
		`SELECT resolution, resolved_at, fully_audited_at FROM intents WHERE intent_id = ?`, intentID,
	).Scan(&resolution, &resolvedAt, &fullyAudited)
	if errors.Is(err, sql.ErrNoRows) {
		return intentRow{}, false
	}
	if err != nil {
		t.Fatalf("read intent row %q: %v", intentID, err)
	}
	return intentRow{
		resolution:   resolution,
		resolved:     resolvedAt.Valid,
		fullyAudited: fullyAudited.Valid,
	}, true
}

// assertResolution fails unless intentID is durably resolved with want.
func assertResolution(t *testing.T, path, intentID, want string) {
	t.Helper()
	row, ok := readIntentRow(t, path, intentID)
	if !ok {
		t.Fatalf("intent %q not found", intentID)
	}
	if !row.resolved {
		t.Fatalf("intent %q is still unresolved, want resolution %q", intentID, want)
	}
	if row.resolution != want {
		t.Fatalf("intent %q resolution = %q, want %q", intentID, row.resolution, want)
	}
}

// assertUnresolved fails unless intentID is still durably unresolved. This is the
// assertion behind "preserve what you cannot establish" — the reconciler must
// leave evidence in place rather than guess a terminal state.
func assertUnresolved(t *testing.T, path, intentID string) {
	t.Helper()
	row, ok := readIntentRow(t, path, intentID)
	if !ok {
		t.Fatalf("intent %q not found", intentID)
	}
	if row.resolved {
		t.Fatalf("intent %q was resolved as %q, want it left unresolved", intentID, row.resolution)
	}
}

// openAudit opens a real durable audit writer in a temp dir.
func openAudit(t *testing.T) *audit.Writer {
	t.Helper()
	w, err := audit.New(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// newSwitch builds a real kill switch over st.
func newSwitch(t *testing.T, st killswitch.Store, cfg killswitch.Config) *killswitch.Switch {
	t.Helper()
	k, err := killswitch.New(context.Background(), st, cfg)
	if err != nil {
		t.Fatalf("new killswitch: %v", err)
	}
	return k
}

func defaultKillswitchConfig() killswitch.Config {
	return killswitch.Config{
		OrderFailureThreshold: 3,
		TokenRefreshThreshold: 3,
		TokenRefreshWindow:    time.Minute,
	}
}

// --- journal seeding --------------------------------------------------------

// seedPrepared appends an intent at the prepared marker, exactly as the submit
// path's AppendIntent does.
func seedPrepared(t *testing.T, db *store.DB, intentID, symbol string) {
	t.Helper()
	payload, err := json.Marshal(order.OrderRequest{
		Symbol:    symbol,
		Side:      order.SideBuy,
		OrderType: order.OrderTypeLimit,
		Quantity:  "10",
		Price:     "1000",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := db.AppendIntent(context.Background(), store.Intent{
		IntentID:      intentID,
		ClientOrderID: "cid-" + intentID,
		Payload:       payload,
		CreatedAt:     baseTime,
	}); err != nil {
		t.Fatalf("append intent %s: %v", intentID, err)
	}
}

// seedSubmitAttempted seeds prepared + submit-attempted — the ambiguous shape
// once the settle window elapses.
func seedSubmitAttempted(t *testing.T, db *store.DB, intentID, symbol string) {
	t.Helper()
	seedPrepared(t, db, intentID, symbol)
	if err := db.AppendMarker(context.Background(), intentID, store.MarkerSubmitAttempted, ""); err != nil {
		t.Fatalf("append submit-attempted %s: %v", intentID, err)
	}
}

// seedAcked seeds the full prepared → submit-attempted → acked progression.
func seedAcked(t *testing.T, db *store.DB, intentID, symbol, orderID string) {
	t.Helper()
	seedSubmitAttempted(t, db, intentID, symbol)
	if err := db.AppendMarker(context.Background(), intentID, store.MarkerAcked, orderID); err != nil {
		t.Fatalf("append acked %s: %v", intentID, err)
	}
}

// unresolvedIDs returns the ids still in the unresolved set.
func unresolvedIDs(t *testing.T, db *store.DB) []string {
	t.Helper()
	intents, err := db.LoadUnresolvedIntents(context.Background())
	if err != nil {
		t.Fatalf("load unresolved: %v", err)
	}
	ids := make([]string, 0, len(intents))
	for _, in := range intents {
		ids = append(ids, in.IntentID)
	}
	return ids
}

func isUnresolved(t *testing.T, db *store.DB, intentID string) bool {
	t.Helper()
	for _, id := range unresolvedIDs(t, db) {
		if id == intentID {
			return true
		}
	}
	return false
}

// orderFailureCount reads the durable consecutive-order-failure counter by the
// name killswitch owns. The name is duplicated here on purpose: it is killswitch
// internal state and this test asserts the reconciler's ORDERING against it.
const counterOrderFailureName = "killswitch.order_failure_streak"

func orderFailureCount(t *testing.T, db *store.DB) int64 {
	t.Helper()
	c, err := db.Counter(context.Background(), counterOrderFailureName)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return c.Value
}

func haltPhase(t *testing.T, db *store.DB) store.HaltPhase {
	t.Helper()
	hs, err := db.Halt(context.Background())
	if err != nil {
		t.Fatalf("read halt: %v", err)
	}
	return hs.Phase
}

// --- fake Toss order API ----------------------------------------------------

// fakeAPI serves GetOrder from a table, and can fail or panic on demand.
type fakeAPI struct {
	mu      sync.Mutex
	orders  map[string]order.Order
	errs    map[string]error
	panicOn map[string]bool
	calls   map[string]int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		orders:  map[string]order.Order{},
		errs:    map[string]error{},
		panicOn: map[string]bool{},
		calls:   map[string]int{},
	}
}

func (f *fakeAPI) set(orderID string, status order.OrderStatus, symbol string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders[orderID] = order.Order{OrderID: orderID, Symbol: symbol, Status: status}
}

func (f *fakeAPI) setOrder(o order.Order) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders[o.OrderID] = o
}

func (f *fakeAPI) fail(orderID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[orderID] = err
}

func (f *fakeAPI) clearFail(orderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.errs, orderID)
}

func (f *fakeAPI) callCount(orderID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[orderID]
}

func (f *fakeAPI) GetOrder(_ context.Context, accountSeq int64, orderID string) (order.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[orderID]++
	if accountSeq != testAccountSeq {
		return order.Order{}, fmt.Errorf("unexpected accountSeq %d", accountSeq)
	}
	if f.panicOn[orderID] {
		panic("fakeAPI: injected panic for " + orderID)
	}
	if err, ok := f.errs[orderID]; ok {
		return order.Order{}, err
	}
	o, ok := f.orders[orderID]
	if !ok {
		return order.Order{}, fmt.Errorf("fakeAPI: no order %q", orderID)
	}
	return o, nil
}

// httpOrderAPI builds a REAL *order.Client over a REAL *toss.Client pointed at an
// httptest server, so at least one path exercises the true HTTP/decode surface
// rather than a stub.
func httpOrderAPI(t *testing.T, handler http.HandlerFunc) *order.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-1", "token_type": "Bearer", "expires_in": 86400,
			})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	api, err := toss.NewClient(srv.URL, "id", "secret")
	if err != nil {
		t.Fatalf("toss client: %v", err)
	}
	return order.NewClient(api)
}

// --- recording seams --------------------------------------------------------

// callLog records the ORDER of the operations whose sequencing is a safety
// contract (count-before-resolve above all).
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	l.calls = append(l.calls, s)
	l.mu.Unlock()
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

func (l *callLog) indexOf(s string) int {
	for i, c := range l.snapshot() {
		if c == s {
			return i
		}
	}
	return -1
}

func (l *callLog) contains(s string) bool { return l.indexOf(s) >= 0 }

func (l *callLog) count(s string) int {
	n := 0
	for _, c := range l.snapshot() {
		if c == s {
			n++
		}
	}
	return n
}

// recordingJournal wraps the real store and records the calls whose ordering
// matters, and can inject failures.
type recordingJournal struct {
	Journal
	log        *callLog
	mu         sync.Mutex
	resolveErr map[string]error
	loadErr    error
	loadPanic  bool
	// onResolve runs immediately before a resolve is delegated, so a test can
	// observe the DURABLE world at that exact instant (the count-before-resolve
	// assertion needs "was the counter already committed when resolve ran?", which
	// call order alone does not prove).
	onResolve func(intentID string)
	// starts/completions make the loop tests deterministic instead of timing
	// dependent: every cycle begins with LoadUnresolvedIntents and ends with
	// LoadNotFullyAuditedIntents, so a test can wait for the exact cycle it
	// triggered rather than polling for a side effect that may not have happened
	// yet.
	starts      int
	completions int
}

func (j *recordingJournal) counts() (starts, completions int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.starts, j.completions
}

func wrapJournal(j Journal, log *callLog) *recordingJournal {
	return &recordingJournal{Journal: j, log: log, resolveErr: map[string]error{}}
}

func (j *recordingJournal) setLoadErr(err error) {
	j.mu.Lock()
	j.loadErr = err
	j.mu.Unlock()
}

func (j *recordingJournal) setLoadPanic(p bool) {
	j.mu.Lock()
	j.loadPanic = p
	j.mu.Unlock()
}

func (j *recordingJournal) failResolve(intentID string, err error) {
	j.mu.Lock()
	j.resolveErr[intentID] = err
	j.mu.Unlock()
}

func (j *recordingJournal) clearResolveFailures() {
	j.mu.Lock()
	j.resolveErr = map[string]error{}
	j.mu.Unlock()
}

func (j *recordingJournal) LoadNotFullyAuditedIntents(ctx context.Context) ([]store.Intent, error) {
	out, err := j.Journal.LoadNotFullyAuditedIntents(ctx)
	j.mu.Lock()
	j.completions++
	j.mu.Unlock()
	return out, err
}

func (j *recordingJournal) LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error) {
	j.mu.Lock()
	j.starts++
	loadErr, loadPanic := j.loadErr, j.loadPanic
	j.mu.Unlock()
	if loadPanic {
		panic("recordingJournal: injected load panic")
	}
	if loadErr != nil {
		return nil, loadErr
	}
	return j.Journal.LoadUnresolvedIntents(ctx)
}

func (j *recordingJournal) ResolveIntent(ctx context.Context, intentID, resolution string) error {
	j.mu.Lock()
	err := j.resolveErr[intentID]
	hook := j.onResolve
	j.mu.Unlock()
	if hook != nil {
		hook(intentID)
	}
	if err != nil {
		j.log.add("resolve-failed:" + intentID)
		return err
	}
	j.log.add("resolve:" + intentID + ":" + resolution)
	return j.Journal.ResolveIntent(ctx, intentID, resolution)
}

// recordingGuard wraps the real kill switch and records every reported/tripped
// signal, so ordering assertions do not depend on reading killswitch internals.
type recordingGuard struct {
	Guard
	log *callLog
}

func wrapGuard(g Guard, log *callLog) *recordingGuard {
	return &recordingGuard{Guard: g, log: log}
}

func (g *recordingGuard) Trip(ctx context.Context, scope killswitch.Scope, symbol, reason string, occurredAt time.Time) error {
	if scope == killswitch.ScopeGlobal {
		g.log.add("trip-global:" + reason)
	} else {
		g.log.add("trip-symbol:" + symbol)
	}
	return g.Guard.Trip(ctx, scope, symbol, reason, occurredAt)
}

func (g *recordingGuard) ClearSymbol(symbol string) {
	g.log.add("clear-symbol:" + symbol)
	g.Guard.ClearSymbol(symbol)
}

func (g *recordingGuard) NotifyScanComplete() {
	g.log.add("notify-scan-complete")
	g.Guard.NotifyScanComplete()
}

func (g *recordingGuard) ReportOrderFailure(ctx context.Context, reason string, occurredAt time.Time) error {
	err := g.Guard.ReportOrderFailure(ctx, reason, occurredAt)
	if err != nil {
		g.log.add("report-order-failure-error")
		return err
	}
	g.log.add("report-order-failure")
	return nil
}

func (g *recordingGuard) ReportOrderSuccess(ctx context.Context) error {
	g.log.add("report-order-success")
	return g.Guard.ReportOrderSuccess(ctx)
}

func (g *recordingGuard) BootHalt() {
	g.log.add("boot-halt")
	g.Guard.BootHalt()
}

// --- audit fakes ------------------------------------------------------------

// failingSink wraps a real audit writer and can turn one emit class into a
// fail-closed (non-durable) failure.
type failingSink struct {
	AuditSink
	mu            sync.Mutex
	failLifecycle bool
	failFill      bool
	lifecycle     []audit.OrderLifecycleEvent
	fills         []audit.FillEvent
	errors        []audit.ErrorEvent
}

func wrapSink(s AuditSink) *failingSink { return &failingSink{AuditSink: s} }

func (s *failingSink) EmitOrderLifecycle(ctx context.Context, ev audit.OrderLifecycleEvent) (audit.Ack, error) {
	s.mu.Lock()
	fail := s.failLifecycle
	s.lifecycle = append(s.lifecycle, ev)
	s.mu.Unlock()
	if fail {
		return audit.Ack{}, &audit.FailClosedError{Op: "test-fsync", Err: fmt.Errorf("injected")}
	}
	return s.AuditSink.EmitOrderLifecycle(ctx, ev)
}

func (s *failingSink) EmitFill(ctx context.Context, ev audit.FillEvent) (audit.Ack, error) {
	s.mu.Lock()
	fail := s.failFill
	s.fills = append(s.fills, ev)
	s.mu.Unlock()
	if fail {
		return audit.Ack{}, &audit.FailClosedError{Op: "test-fsync", Err: fmt.Errorf("injected")}
	}
	return s.AuditSink.EmitFill(ctx, ev)
}

func (s *failingSink) EmitError(ctx context.Context, ev audit.ErrorEvent) (audit.Ack, error) {
	s.mu.Lock()
	s.errors = append(s.errors, ev)
	s.mu.Unlock()
	return s.AuditSink.EmitError(ctx, ev)
}

func (s *failingSink) fillEvents() []audit.FillEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.FillEvent, len(s.fills))
	copy(out, s.fills)
	return out
}

func (s *failingSink) lifecycleEvents() []audit.OrderLifecycleEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.OrderLifecycleEvent, len(s.lifecycle))
	copy(out, s.lifecycle)
	return out
}

func (s *failingSink) errorEvents() []audit.ErrorEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.ErrorEvent, len(s.errors))
	copy(out, s.errors)
	return out
}

// --- rig --------------------------------------------------------------------

// rig is the assembled system under test: real store, real audit, real kill
// switch, fake API, injected clock.
type rig struct {
	t       *testing.T
	db      *store.DB
	path    string
	sw      *killswitch.Switch
	api     *fakeAPI
	sink    *failingSink
	journal *recordingJournal
	guard   *recordingGuard
	log     *callLog
	clock   *testClock
	rec     *Reconciler
	ticks   chan time.Time
}

type rigOption func(*Config)

func withThreshold(n int) rigOption {
	return func(c *Config) { c.AmbiguousBacklogThreshold = n }
}

func withSettleWindow(d time.Duration) rigOption {
	return func(c *Config) { c.SettleWindow = d }
}

func newRig(t *testing.T, opts ...rigOption) *rig {
	t.Helper()
	db, path := openStore(t)
	sw := newSwitch(t, db, defaultKillswitchConfig())
	return newRigWith(t, db, path, sw, opts...)
}

// newRigWith assembles a rig over an already-built store and kill switch, for
// tests that need to inject failures at the killswitch's own store seam.
func newRigWith(t *testing.T, db *store.DB, path string, sw *killswitch.Switch, opts ...rigOption) *rig {
	t.Helper()
	log := &callLog{}
	api := newFakeAPI()
	sink := wrapSink(openAudit(t))
	journal := wrapJournal(db, log)
	guard := wrapGuard(sw, log)
	clock := newClock()
	// Unbuffered: a send is a rendezvous with the loop's select, so a test knows
	// the tick was actually delivered rather than merely queued.
	ticks := make(chan time.Time)

	cfg := Config{
		Journal:                   journal,
		Guard:                     guard,
		API:                       api,
		Audit:                     sink,
		AccountSeq:                testAccountSeq,
		AmbiguousBacklogThreshold: 2,
		SettleWindow:              30 * time.Second,
		ReevalInterval:            time.Second,
		Now:                       clock.Now,
		Ticks:                     ticks,
		Logger:                    discardLogger(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	rec, err := New(cfg)
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	return &rig{
		t: t, db: db, path: path, sw: sw, api: api, sink: sink, journal: journal,
		guard: guard, log: log, clock: clock, rec: rec, ticks: ticks,
	}
}

// NOTE on the injected clock and the process watermark: the reconciler records
// when it FIRST scanned the journal, and a fill submitted at or before that point
// is treated as belonging to a previous process (its counter reset is withheld —
// see TestPreexistingFillDoesNotResetTheStreak). In these tests that distinction
// is expressed by where the clock sits when boot() runs: seeds land with wall-clock
// marker times just after baseTime, so a test that advances the clock BEFORE
// booting is declaring "this process started later, these intents are crash
// recovery", while a test that boots at baseTime is declaring "this process
// submitted them". Both are exercised deliberately.

// pastSettle moves the clock past the settle window so every seeded
// submit-attempted intent is unambiguously ambiguous.
func (r *rig) pastSettle() {
	r.clock.Set(baseTime.Add(24 * time.Hour))
}

func (r *rig) boot() error {
	return r.rec.BootScan(context.Background())
}

func (r *rig) cycle() error {
	return r.rec.Reconcile(context.Background())
}

func (r *rig) canSubmit(symbol string) (bool, string) {
	return r.sw.CanSubmit(symbol)
}

// waitFor polls cond until it holds or the deadline elapses. The loop tests drive
// a real goroutine, so an assertion must wait for the cycle rather than assume it
// already ran; polling (instead of sleeping a fixed amount) keeps the test fast
// and non-flaky.
// awaitBoot blocks until the loop's boot scan has fully finished BOTH passes, so
// the loop is parked on its select. Waiting only for the replay gate would not be
// enough: the gate opens between pass 1 and pass 2, so a tick sent then would race
// with the tail of the boot scan and a test could mistake the boot's own audit
// pass for the cycle it triggered.
func (r *rig) awaitBoot() {
	r.t.Helper()
	waitFor(r.t, "the boot scan to open the replay gate", func() bool {
		return r.log.contains("notify-scan-complete")
	})
	waitFor(r.t, "both boot passes to finish", func() bool {
		_, completions := r.journal.counts()
		return completions >= 1
	})
}

// tick delivers one re-evaluation tick and waits for that cycle to COMPLETE, so
// the assertion after it is not a race against the loop goroutine. It must be
// called with the loop idle (see awaitBoot).
func (r *rig) tick() {
	r.t.Helper()
	_, before := r.journal.counts()
	r.ticks <- time.Now()
	waitFor(r.t, "the triggered cycle to complete", func() bool {
		_, after := r.journal.counts()
		return after > before
	})
}

// tickExpectingFailure delivers a tick for a cycle that will fail before it can
// complete, so it waits for the cycle to have STARTED instead.
func (r *rig) tickExpectingFailure() {
	r.t.Helper()
	before, _ := r.journal.counts()
	r.ticks <- time.Now()
	waitFor(r.t, "the triggered cycle to start", func() bool {
		after, _ := r.journal.counts()
		return after > before
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

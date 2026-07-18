package order

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// --- test doubles (all guarded for -race) ---

// stubAPI is a submitAPI that records how many POSTs it received and returns a
// configured outcome. The POST count is the load-bearing assertion for "no
// duplicate submit" and "no auto-resubmit".
type stubAPI struct {
	mu      sync.Mutex
	n       int
	lastReq OrderRequest
	resp    OrderResponse
	err     error
}

func (s *stubAPI) SubmitOrder(_ context.Context, _ int64, req OrderRequest) (OrderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.lastReq = req
	return s.resp, s.err
}

func (s *stubAPI) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *stubAPI) req() OrderRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
}

// fakeGuard is a guard whose CanSubmit / Reconfirm verdicts are set
// independently so a test can drive the TOCTOU window (allow first, block on the
// final re-check). It records every Trip so a test can assert that an ambiguous
// submit never trips and an audit fail-closed trips exactly once, globally.
type fakeGuard struct {
	mu          sync.Mutex
	canOK       bool
	canReason   string
	reconfirmOK bool
	reconReason string
	trips       []killswitch.Scope
}

func (g *fakeGuard) CanSubmit(string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.canOK, g.canReason
}

func (g *fakeGuard) Reserve(string) killswitch.Reservation { return killswitch.Reservation{} }

func (g *fakeGuard) Reconfirm(killswitch.Reservation) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reconfirmOK, g.reconReason
}

func (g *fakeGuard) Trip(_ context.Context, scope killswitch.Scope, _, _ string, _ time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trips = append(g.trips, scope)
	return nil
}

func (g *fakeGuard) tripCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.trips)
}

func allowingGuard() *fakeGuard { return &fakeGuard{canOK: true, reconfirmOK: true} }

// fakeAudit is an auditSink that records the marker of every lifecycle emit and
// every error emit, and can be told to fail-closed on a specific marker.
type fakeAudit struct {
	mu           sync.Mutex
	events       []audit.OrderLifecycleEvent
	errs         []audit.ErrorEvent
	failClosedOn string // marker to fail-close on; "" = never
}

func (a *fakeAudit) EmitOrderLifecycle(ctx context.Context, ev audit.OrderLifecycleEvent) (audit.Ack, error) {
	// Mirror the real *audit.Writer contract: it checks ctx.Err() first and returns
	// it (a NON-fail-closed error) before writing anything. This is what makes a
	// caller-cancelled emit skip the record — the fail-open the detach must close.
	if err := ctx.Err(); err != nil {
		return audit.Ack{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	if a.failClosedOn != "" && ev.Marker == a.failClosedOn {
		return audit.Ack{}, &audit.FailClosedError{Op: "test", Err: errors.New("disk full")}
	}
	return audit.Ack{IdempotencyKey: ev.IntentID + ":" + ev.Marker}, nil
}

func (a *fakeAudit) EmitError(ctx context.Context, ev audit.ErrorEvent) (audit.Ack, error) {
	if err := ctx.Err(); err != nil {
		return audit.Ack{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.errs = append(a.errs, ev)
	return audit.Ack{IdempotencyKey: ev.IntentID + ":" + ev.Operation}, nil
}

func (a *fakeAudit) hasMarker(marker string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.Marker == marker {
			return true
		}
	}
	return false
}

// preservedOrderID reports whether an ERROR audit record carrying orderID was
// emitted — the orphan-orderId preservation on the independent audit medium.
func (a *fakeAudit) preservedOrderID(orderID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.errs {
		if e.OrderID == orderID {
			return true
		}
	}
	return false
}

// failOnAppend is a journal decorator that injects a durable-write error for a
// chosen marker over a real underlying store, to exercise the store-failure
// fail-closed paths.
type failOnAppend struct {
	inner      journal
	failIntent bool             // fail AppendIntent (the prepared marker)
	failMarker store.MarkerKind // fail this AppendMarker kind ("" = none)
	err        error
}

func (f *failOnAppend) AppendIntent(ctx context.Context, in store.Intent) error {
	if f.failIntent {
		return f.err
	}
	return f.inner.AppendIntent(ctx, in)
}

func (f *failOnAppend) AppendMarker(ctx context.Context, intentID string, kind store.MarkerKind, orderID string) error {
	if f.failMarker != "" && kind == f.failMarker {
		return f.err
	}
	return f.inner.AppendMarker(ctx, intentID, kind, orderID)
}

func (f *failOnAppend) LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error) {
	return f.inner.LoadUnresolvedIntents(ctx)
}

// resolvedDuplicateJournal simulates re-submitting an intentId whose row already
// exists but has been RESOLVED (#35): AppendIntent hits a PK collision, yet the
// unresolved scan that drives idempotency does not surface the resolved row (so the
// submit path cannot tell this duplicate replay apart from a durability failure).
type resolvedDuplicateJournal struct{ collisionErr error }

func (j *resolvedDuplicateJournal) AppendIntent(context.Context, store.Intent) error {
	return j.collisionErr
}

func (j *resolvedDuplicateJournal) AppendMarker(context.Context, string, store.MarkerKind, string) error {
	return nil
}

func (j *resolvedDuplicateJournal) LoadUnresolvedIntents(context.Context) ([]store.Intent, error) {
	return nil, nil
}

// cancelAfterAppend is a journal decorator that cancels the caller ctx the instant
// a chosen marker's own durable write succeeds, exercising the window between a
// committed marker and its mandatory audit emit. The underlying marker write ran
// on the (still-live) caller ctx and committed; only what comes AFTER sees the
// cancellation.
type cancelAfterAppend struct {
	inner    journal
	cancel   context.CancelFunc
	onIntent bool             // cancel right after AppendIntent (the prepared marker)
	onMarker store.MarkerKind // cancel right after this AppendMarker kind ("" = none)
}

func (c *cancelAfterAppend) AppendIntent(ctx context.Context, in store.Intent) error {
	err := c.inner.AppendIntent(ctx, in)
	if err == nil && c.onIntent {
		c.cancel()
	}
	return err
}

func (c *cancelAfterAppend) AppendMarker(ctx context.Context, intentID string, kind store.MarkerKind, orderID string) error {
	err := c.inner.AppendMarker(ctx, intentID, kind, orderID)
	if err == nil && c.onMarker != "" && kind == c.onMarker {
		c.cancel()
	}
	return err
}

func (c *cancelAfterAppend) LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error) {
	return c.inner.LoadUnresolvedIntents(ctx)
}

func (a *fakeAudit) markers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.events))
	for i, e := range a.events {
		out[i] = e.Marker
	}
	return out
}

// cancelingAPI models the money-critical race: the POST is ACCEPTED by Toss (an
// orderId comes back — the order is irreversible), but the caller's ctx is
// cancelled / its deadline elapses during that same POST. It cancels the caller
// ctx at the instant it returns success, so the acked bookkeeping that follows
// runs under a cancelled caller ctx.
type cancelingAPI struct {
	cancel context.CancelFunc
	resp   OrderResponse
	n      int32
}

func (a *cancelingAPI) SubmitOrder(_ context.Context, _ int64, _ OrderRequest) (OrderResponse, error) {
	atomic.AddInt32(&a.n, 1)
	a.cancel()         // the caller's deadline elapses mid-POST...
	return a.resp, nil // ...but Toss accepted the order — orderId is in hand.
}

func (a *cancelingAPI) calls() int { return int(atomic.LoadInt32(&a.n)) }

type wakeSpy struct{ n int32 }

func (w *wakeSpy) wake()      { atomic.AddInt32(&w.n, 1) }
func (w *wakeSpy) count() int { return int(atomic.LoadInt32(&w.n)) }

// --- helpers ---

func newStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newAudit(t *testing.T) *audit.Writer {
	t.Helper()
	w, err := audit.New(filepath.Join(t.TempDir(), "audit"))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func newKill(t *testing.T, st *store.DB) *killswitch.Switch {
	t.Helper()
	ks, err := killswitch.New(context.Background(), st, killswitch.Config{
		OrderFailureThreshold: 3,
		TokenRefreshThreshold: 3,
		TokenRefreshWindow:    time.Minute,
	})
	if err != nil {
		t.Fatalf("killswitch.New: %v", err)
	}
	ks.NotifyScanComplete() // open the replay gate so CanSubmit can allow
	return ks
}

func validReq(symbol string) OrderRequest {
	return OrderRequest{Symbol: symbol, Side: SideBuy, OrderType: OrderTypeLimit, Quantity: "10", Price: "150.00"}
}

func markerKindsOf(t *testing.T, st *store.DB, intentID string) []store.MarkerKind {
	t.Helper()
	ins, err := st.LoadUnresolvedIntents(context.Background())
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	for _, in := range ins {
		if in.IntentID == intentID {
			out := make([]store.MarkerKind, len(in.Markers))
			for i, m := range in.Markers {
				out[i] = m.Kind
			}
			return out
		}
	}
	return nil
}

func intentByID(t *testing.T, st *store.DB, intentID string) (store.Intent, bool) {
	t.Helper()
	ins, err := st.LoadUnresolvedIntents(context.Background())
	if err != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", err)
	}
	for _, in := range ins {
		if in.IntentID == intentID {
			return in, true
		}
	}
	return store.Intent{}, false
}

func eqKinds(a []store.MarkerKind, b ...store.MarkerKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- tests ---

// TestSubmitIntentSuccessMarkerSequence is the happy path against a REAL store,
// REAL audit writer and REAL kill-switch: the markers must be exactly
// prepared -> submit-attempted -> acked, each once, with the orderId on acked,
// exactly one POST, and no trip (AC "성공 경로의 마커 시퀀스").
func TestSubmitIntentSuccessMarkerSequence(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ks := newKill(t, st)
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-1"}}
	var wake wakeSpy

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: newAudit(t), Guard: ks, API: api, AccountSeq: 7, Wake: wake.wake,
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if out.Status != StatusAcked || out.OrderID != "ord-1" {
		t.Fatalf("outcome = %+v, want acked ord-1", out)
	}
	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want 1", api.calls())
	}
	if got := markerKindsOf(t, st, "i1"); !eqKinds(got, store.MarkerPrepared, store.MarkerSubmitAttempted, store.MarkerAcked) {
		t.Fatalf("markers = %v, want prepared/submit-attempted/acked", got)
	}
	// acked marker must carry the orderId; the kill-switch must not be tripped.
	in, _ := intentByID(t, st, "i1")
	if last := in.Markers[len(in.Markers)-1]; last.Kind != store.MarkerAcked || last.OrderID != "ord-1" {
		t.Fatalf("acked marker = %+v, want orderId ord-1", last)
	}
	if allowed, _ := ks.CanSubmit("AAPL"); !allowed {
		t.Fatalf("kill-switch tripped on happy path")
	}
	if wake.count() != 0 {
		t.Fatalf("reconciler woken on happy path: %d", wake.count())
	}
	// The derived clientOrderId must have travelled to the POST unchanged.
	if got := api.req().ClientOrderID; got != DeriveClientOrderID("i1") {
		t.Fatalf("POST clientOrderId = %q, want derived %q", got, DeriveClientOrderID("i1"))
	}
}

// TestSubmitIntentEmitsAuditPerMarker pins that every marker transition emits an
// order-lifecycle audit record, in order (ADR-0006 point 4). Uses a recording
// fakeAudit so the emitted sequence is directly observable.
func TestSubmitIntentEmitsAuditPerMarker(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	fa := &fakeAudit{}
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-9"}}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: fa, Guard: allowingGuard(), API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")}); err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	want := []string{string(store.MarkerPrepared), string(store.MarkerSubmitAttempted), string(store.MarkerAcked)}
	got := fa.markers()
	if len(got) != len(want) {
		t.Fatalf("audit markers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit marker[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
	// acked audit must be keyed by orderId (ADR-0006 point 3): its OrderID field set.
	fa.mu.Lock()
	acked := fa.events[2]
	fa.mu.Unlock()
	if acked.OrderID != "ord-9" {
		t.Fatalf("acked audit OrderID = %q, want ord-9", acked.OrderID)
	}
}

// TestSubmitIntentIdempotentReplay pins ADR-0002 point 2: re-calling with the
// same intentId returns the existing state and makes NO second POST (AC 멱등).
func TestSubmitIntentIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-1"}}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: allowingGuard(), API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	first, err := sub.SubmitIntent(ctx, Intent{IntentID: "dup", Request: validReq("AAPL")})
	if err != nil || first.Status != StatusAcked {
		t.Fatalf("first submit = %+v, err %v", first, err)
	}
	second, err := sub.SubmitIntent(ctx, Intent{IntentID: "dup", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("second submit err: %v", err)
	}
	if second.Status != StatusDuplicate {
		t.Fatalf("second status = %v, want duplicate", second.Status)
	}
	if second.OrderID != "ord-1" {
		t.Fatalf("duplicate outcome OrderID = %q, want ord-1", second.OrderID)
	}
	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want 1 (no duplicate submit)", api.calls())
	}
}

// TestSubmitIntentIdempotentUnderConcurrency drives many concurrent submits of
// the same intentId. The intent_id PRIMARY KEY is the true barrier, so exactly
// one POST may happen no matter the interleaving (verified under -race).
func TestSubmitIntentIdempotentUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-c"}}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: allowingGuard(), API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = sub.SubmitIntent(ctx, Intent{IntentID: "race", Request: validReq("AAPL")})
		}()
	}
	wg.Wait()

	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want exactly 1 across %d concurrent same-id submits", api.calls(), n)
	}
}

// TestSubmitIntentCanSubmitBlockedWritesNothing pins ADR-0004 point 1: a blocked
// initial CanSubmit writes NOTHING to the journal and makes no POST (AC).
func TestSubmitIntentCanSubmitBlockedWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	fa := &fakeAudit{}
	api := &stubAPI{}
	g := &fakeGuard{canOK: false, canReason: "global-halt", reconfirmOK: true}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: fa, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %v, want blocked", out.Status)
	}
	if _, ok := intentByID(t, st, "i1"); ok {
		t.Fatalf("intent written despite blocked CanSubmit")
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
	if len(fa.markers()) != 0 {
		t.Fatalf("audit emitted %v despite blocked CanSubmit", fa.markers())
	}
}

// TestSubmitIntentReconfirmBlockedLeavesPreparedOnly pins the TOCTOU seam
// (ADR-0004 point 1): CanSubmit allows, then the final Reconfirm blocks, so the
// intent is left prepared-only (no submit-attempted, no POST) for the reconciler
// to close as aborted-before-submit.
func TestSubmitIntentReconfirmBlockedLeavesPreparedOnly(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{}
	g := &fakeGuard{canOK: true, reconfirmOK: false, reconReason: "trip-in-flight"}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %v, want blocked", out.Status)
	}
	if got := markerKindsOf(t, st, "i1"); !eqKinds(got, store.MarkerPrepared) {
		t.Fatalf("markers = %v, want prepared-only (submit-attempted must be absent)", got)
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0 (blocked before the irreversible submit)", api.calls())
	}
	// count-first guard: a guard abort must NOT resolve the intent (that is #35's
	// job); it stays unresolved for the reconciler.
	in, _ := intentByID(t, st, "i1")
	if in.ResolvedAt != nil {
		t.Fatalf("intent resolved by order on guard-abort; must be left unresolved for #35")
	}
	if g.tripCount() != 0 {
		t.Fatalf("guard tripped on a plain block: %d", g.tripCount())
	}
}

// TestSubmitIntentPostAmbiguousUnresolvedWakesNoResubmit pins ADR-0003 point 1/4:
// an ambiguous POST (error/timeout) leaves the intent unresolved with
// submit-attempted but no acked, wakes the reconciler exactly once, makes exactly
// one POST (no auto-resubmit), does NOT resolve the intent, and — crucially — does
// NOT trip / ReportOrderFailure (ambiguous submit is a separate trigger; wiring it
// to the order-failure counter would double-count, ADR-0012 #34 함의 c).
func TestSubmitIntentPostAmbiguousUnresolvedWakesNoResubmit(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{err: errors.New("timeout: no response")}
	g := allowingGuard()
	var wake wakeSpy

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: g, API: api, AccountSeq: 1, Wake: wake.wake,
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("SubmitIntent returned hard error for ambiguous submit: %v", err)
	}
	if out.Status != StatusUnresolved {
		t.Fatalf("status = %v, want unresolved", out.Status)
	}
	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want exactly 1 (no auto-resubmit)", api.calls())
	}
	if wake.count() != 1 {
		t.Fatalf("wake count = %d, want 1", wake.count())
	}
	if got := markerKindsOf(t, st, "i1"); !eqKinds(got, store.MarkerPrepared, store.MarkerSubmitAttempted) {
		t.Fatalf("markers = %v, want prepared/submit-attempted (acked must be absent)", got)
	}
	in, _ := intentByID(t, st, "i1")
	if in.ResolvedAt != nil {
		t.Fatalf("order resolved an ambiguous submit; must delegate to #35 (unresolved)")
	}
	if g.tripCount() != 0 {
		t.Fatalf("ambiguous submit tripped the kill-switch %d time(s); it must not (separate trigger)", g.tripCount())
	}
}

// TestSubmitIntentAuditFailClosedTripsGlobal pins ADR-0006 point 6: a fail-closed
// audit write escalates to a GLOBAL kill-switch trip, via the kill-switch's own
// durable path (store) — not bound to any order transaction (TripTx-free,
// ADR-0012 Decision 2). Uses a REAL store + REAL (closed) audit writer + REAL
// kill-switch so the trip is observed on the real durable halt.
func TestSubmitIntentAuditFailClosedTripsGlobal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ks := newKill(t, st)
	aw := newAudit(t)
	aw.Close() // closing forces every subsequent Emit to fail-closed
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-x"}}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: aw, Guard: ks, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	_, err = sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err == nil {
		t.Fatalf("SubmitIntent returned nil error on audit fail-closed")
	}
	// The real kill-switch must now be globally halted (durable).
	if allowed, _ := ks.CanSubmit("AAPL"); allowed {
		t.Fatalf("kill-switch not tripped after audit fail-closed")
	}
	hs, herr := st.Halt(ctx)
	if herr != nil {
		t.Fatalf("Halt: %v", herr)
	}
	if hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt phase = %q, want halted", hs.Phase)
	}
	// Fail-closed happened at the prepared marker, before the POST.
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0 (fail-closed before submit)", api.calls())
	}
}

// TestSubmitIntentAuditFailClosedTripIsIndependentCommit pins that the trip is a
// bare kill-switch report, NOT atomically bound to the order write: the prepared
// intent (its own commit) survives while a global trip fires exactly once. Uses a
// fakeAudit failing at prepared and a fakeGuard recording trips.
func TestSubmitIntentAuditFailClosedTripIsIndependentCommit(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	fa := &fakeAudit{failClosedOn: string(store.MarkerPrepared)}
	g := allowingGuard()
	api := &stubAPI{}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: fa, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("expected fail-closed error")
	}
	// prepared was its own durable commit and is NOT rolled back with the trip.
	if got := markerKindsOf(t, st, "i1"); !eqKinds(got, store.MarkerPrepared) {
		t.Fatalf("markers = %v, want prepared (independent commit)", got)
	}
	if g.tripCount() != 1 {
		t.Fatalf("trip count = %d, want exactly 1 global trip", g.tripCount())
	}
	g.mu.Lock()
	scope := g.trips[0]
	g.mu.Unlock()
	if scope != killswitch.ScopeGlobal {
		t.Fatalf("trip scope = %v, want global", scope)
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
}

// TestSubmitIntentEmptyIntentIDRejected pins that the strategy contract (a
// non-empty intentId, ADR-0002 point 2) is enforced before anything is written.
func TestSubmitIntentEmptyIntentIDRejected(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: allowingGuard(), API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("empty intentId accepted")
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
	ins, _ := st.LoadUnresolvedIntents(ctx)
	if len(ins) != 0 {
		t.Fatalf("intents written for empty intentId: %d", len(ins))
	}
}

// TestSubmitIntentAckSurvivesCallerCancel pins ADR-0002 point 3: once the POST is
// accepted, resp.OrderID is the order's ONLY durable truth handle (clientOrderId
// is not queryable — ADR-0002 point 4), so recording it must NOT be at the mercy
// of the caller's ctx. Here the caller ctx is cancelled during the POST; the
// accepted order's acked marker + orderId must still be durably recorded (the
// post-POST bookkeeping is a cancellation-immune critical section). prepared and
// submit-attempted precede the cancel and must be present too.
func TestSubmitIntentAckSurvivesCallerCancel(t *testing.T) {
	st := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &cancelingAPI{cancel: cancel, resp: OrderResponse{OrderID: "ord-live"}}
	var wake wakeSpy

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: newAudit(t), Guard: allowingGuard(), API: api, AccountSeq: 1, Wake: wake.wake,
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if out.Status != StatusAcked || out.OrderID != "ord-live" {
		t.Fatalf("outcome = %+v, want acked ord-live (accepted order's handle must persist despite caller cancel)", out)
	}
	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want 1", api.calls())
	}
	if got := markerKindsOf(t, st, "i1"); !eqKinds(got, store.MarkerPrepared, store.MarkerSubmitAttempted, store.MarkerAcked) {
		t.Fatalf("markers = %v, want prepared/submit-attempted/acked (acked must survive caller cancel)", got)
	}
	in, _ := intentByID(t, st, "i1")
	last := in.Markers[len(in.Markers)-1]
	if last.Kind != store.MarkerAcked || last.OrderID != "ord-live" {
		t.Fatalf("acked marker = %+v, want orderId ord-live durably recorded", last)
	}
}

// TestSubmitIntentPreparedAuditSurvivesCallerCancel pins ADR-0006: the audit
// record for a COMMITTED marker is mandatory and its absence must never be a
// silent skip. Here the caller ctx is cancelled the instant the prepared marker is
// durably committed; the prepared audit record must still be written (the emit is
// cancellation-immune), and — since cancellation is not a durability failure — no
// global halt trips. Without the detach this is a fail-open hole: a durable marker
// with no audit and no halt.
func TestSubmitIntentPreparedAuditSurvivesCallerCancel(t *testing.T) {
	st := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fa := &fakeAudit{}
	g := allowingGuard()
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-1"}}
	j := &cancelAfterAppend{inner: st, cancel: cancel, onIntent: true}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: fa, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	// The submit itself will not complete (the caller ctx is cancelled right after
	// prepared, so the later submit-attempted marker write aborts) — that is fine.
	// What must hold is that the prepared marker's mandatory audit was NOT skipped.
	_, _ = sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})

	if !fa.hasMarker(string(store.MarkerPrepared)) {
		t.Fatalf("prepared audit record missing after caller cancel — fail-open (committed marker, no audit)")
	}
	if g.tripCount() != 0 {
		t.Fatalf("caller cancel tripped the halt %d time(s); cancellation is not a durability failure", g.tripCount())
	}
}

// TestSubmitIntentSubmitAttemptedAuditSurvivesCallerCancel is the submit-attempted
// twin of the prepared test above (same fail-open class).
func TestSubmitIntentSubmitAttemptedAuditSurvivesCallerCancel(t *testing.T) {
	st := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fa := &fakeAudit{}
	g := allowingGuard()
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-1"}}
	j := &cancelAfterAppend{inner: st, cancel: cancel, onMarker: store.MarkerSubmitAttempted}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: fa, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	_, _ = sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})

	if !fa.hasMarker(string(store.MarkerSubmitAttempted)) {
		t.Fatalf("submit-attempted audit record missing after caller cancel — fail-open")
	}
	if g.tripCount() != 0 {
		t.Fatalf("caller cancel tripped the halt %d time(s); cancellation is not a durability failure", g.tripCount())
	}
}

// TestSubmitIntentAckedStoreFailureTripsGlobal pins ADR-0005 point 6 for the most
// acute store failure: after the POST is ACCEPTED (order live, irreversible), a
// genuine acked-marker durable-write failure must (a) preserve the orderId on the
// independent audit medium, (b) trip the GLOBAL halt (not a per-symbol block — a
// store medium failure is system-wide), (c) return a HARD error, and never
// auto-resubmit. A real kill-switch (over a healthy store) observes the durable
// trip; the acked write fails only through the order journal decorator.
func TestSubmitIntentAckedStoreFailureTripsGlobal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ks := newKill(t, st)
	fa := &fakeAudit{}
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-live"}}
	var wake wakeSpy
	j := &failOnAppend{inner: st, failMarker: store.MarkerAcked, err: errors.New("disk full")}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: fa, Guard: ks, API: api, AccountSeq: 1, Wake: wake.wake,
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	out, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")})
	if err == nil {
		t.Fatalf("acked store failure returned nil error (fail-open); want a hard error")
	}
	if out.OrderID != "ord-live" {
		t.Fatalf("outcome OrderID = %q, want ord-live carried through", out.OrderID)
	}
	// (a) orderId preserved on the independent audit channel.
	if !fa.preservedOrderID("ord-live") {
		t.Fatalf("orderId not preserved to the audit channel — the sole truth handle was lost")
	}
	// (b) global halt tripped, durably.
	if allowed, _ := ks.CanSubmit("AAPL"); allowed {
		t.Fatalf("global halt not tripped after acked store failure")
	}
	hs, herr := st.Halt(ctx)
	if herr != nil {
		t.Fatalf("Halt: %v", herr)
	}
	if hs.Phase != store.HaltHalted {
		t.Fatalf("durable halt phase = %q, want halted", hs.Phase)
	}
	// no auto-resubmit.
	if api.calls() != 1 {
		t.Fatalf("POST calls = %d, want exactly 1 (no resubmit on store failure)", api.calls())
	}
}

// TestSubmitIntentSubmitAttemptedStoreFailureTripsGlobal pins ADR-0005 point 6 for
// the submit-attempted marker: a durable-write failure trips the global halt,
// returns a hard error, and the POST never happens.
func TestSubmitIntentSubmitAttemptedStoreFailureTripsGlobal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ks := newKill(t, st)
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-x"}}
	j := &failOnAppend{inner: st, failMarker: store.MarkerSubmitAttempted, err: errors.New("disk full")}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: &fakeAudit{}, Guard: ks, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("submit-attempted store failure returned nil error; want a hard error")
	}
	if allowed, _ := ks.CanSubmit("AAPL"); allowed {
		t.Fatalf("global halt not tripped after submit-attempted store failure")
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0 (fail-closed before the irreversible submit)", api.calls())
	}
}

// TestSubmitIntentPreparedFailureDoesNotTrip pins that a prepared AppendIntent
// failure returns a hard error but does NOT trip the global halt. order cannot
// distinguish a PK collision (a duplicate replay of an already-resolved intent —
// its row remains but is out of the unresolved set) from a genuine durability
// failure without store support #34 may not add, so it must not halt the whole bot
// on what may be a mere duplicate (fail-closed-wrong-direction). Money-safety holds
// regardless: a prepared failure means no POST. Contrast submit-attempted / acked,
// whose autoincrement markers cannot PK-collide and DO fail-closed below.
func TestSubmitIntentPreparedFailureDoesNotTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	g := allowingGuard()
	api := &stubAPI{}
	j := &failOnAppend{inner: st, failIntent: true, err: errors.New("disk full")}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: &fakeAudit{}, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("prepared failure returned nil error; want a hard error")
	}
	if g.tripCount() != 0 {
		t.Fatalf("prepared failure tripped the global halt %d time(s); must not (may be a duplicate replay)", g.tripCount())
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
}

// TestSubmitIntentPreparedPKCollisionDoesNotTrip is the direct regression guard for
// the spurious-halt bug: re-submitting a RESOLVED intentId hits an AppendIntent PK
// collision that the unresolved scan does not surface. It must return a hard error
// WITHOUT tripping the global halt and without a POST — a duplicate replay must
// never halt the whole bot.
func TestSubmitIntentPreparedPKCollisionDoesNotTrip(t *testing.T) {
	g := allowingGuard()
	api := &stubAPI{}
	j := &resolvedDuplicateJournal{collisionErr: errors.New("UNIQUE constraint failed: intents.intent_id")}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: &fakeAudit{}, Guard: g, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(context.Background(), Intent{IntentID: "resolved-1", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("expected a hard error on a duplicate (resolved) intentId")
	}
	if g.tripCount() != 0 {
		t.Fatalf("a duplicate replay tripped the global halt %d time(s); must not (fail-closed-wrong-direction)", g.tripCount())
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
}

// TestSubmitIntentCallerCancelAtSubmitAttemptedDoesNotTrip pins the counterpart to
// the store-failure tests: a pre-POST marker write that fails because the CALLER
// cancelled (not because the medium failed) is a clean abort — it returns a hard
// error but must NOT trip the global halt (ADR-0005 point 6 escalates a durability
// medium failure, not caller cancellation). Here the caller ctx is cancelled right
// after prepared commits, so the subsequent submit-attempted write fails with
// context.Canceled.
func TestSubmitIntentCallerCancelAtSubmitAttemptedDoesNotTrip(t *testing.T) {
	st := newStore(t)
	ks := newKill(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &stubAPI{}
	j := &cancelAfterAppend{inner: st, cancel: cancel, onIntent: true}

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: j, Audit: newAudit(t), Guard: ks, API: api, AccountSeq: 1, Wake: func() {},
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	if _, err := sub.SubmitIntent(ctx, Intent{IntentID: "i1", Request: validReq("AAPL")}); err == nil {
		t.Fatalf("expected an abort error when the caller cancels mid-submit")
	}
	if allowed, _ := ks.CanSubmit("AAPL"); !allowed {
		t.Fatalf("caller cancellation tripped the global halt; a clean pre-POST abort must not (contrast store-failure)")
	}
	if api.calls() != 0 {
		t.Fatalf("POST calls = %d, want 0", api.calls())
	}
}

// TestSubmitIntentInvalidRequestRejectedBeforeJournal pins that a structurally
// invalid OrderRequest (a strategy-contract violation the #33 wrapper rejects
// before any network POST) is rejected at the SubmitIntent entry, BEFORE any
// journal write. Otherwise a submit-attempted marker ("POST may have happened")
// would be forged for an intent whose POST could never occur, leaving a false
// ambiguous state the reconciler cannot resolve against Toss (ADR-0002). Nothing
// must be written: no prepared, no submit-attempted, no POST, no wake, no trip.
func TestSubmitIntentInvalidRequestRejectedBeforeJournal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	api := &stubAPI{resp: OrderResponse{OrderID: "ord-x"}}
	g := allowingGuard()
	var wake wakeSpy

	sub, err := NewSubmitter(SubmitterConfig{
		Journal: st, Audit: newAudit(t), Guard: g, API: api, AccountSeq: 1, Wake: wake.wake,
	})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	cases := []struct {
		name string
		id   string
		req  OrderRequest
	}{
		{"missing-symbol", "inv-1", OrderRequest{Side: SideBuy, OrderType: OrderTypeLimit, Quantity: "10", Price: "150.00"}},
		{"missing-side", "inv-2", OrderRequest{Symbol: "AAPL", OrderType: OrderTypeLimit, Quantity: "10", Price: "150.00"}},
		{"missing-ordertype", "inv-3", OrderRequest{Symbol: "AAPL", Side: SideBuy, Quantity: "10", Price: "150.00"}},
		{"quantity-and-amount", "inv-4", OrderRequest{Symbol: "AAPL", Side: SideBuy, OrderType: OrderTypeMarket, Quantity: "10", OrderAmount: "100"}},
		{"neither-quantity-nor-amount", "inv-5", OrderRequest{Symbol: "AAPL", Side: SideBuy, OrderType: OrderTypeMarket}},
	}
	for _, c := range cases {
		out, err := sub.SubmitIntent(ctx, Intent{IntentID: c.id, Request: c.req})
		if err == nil {
			t.Errorf("%s: expected a validation error, got outcome %+v", c.name, out)
		}
		if _, ok := intentByID(t, st, c.id); ok {
			t.Errorf("%s: intent entered the journal despite an invalid request", c.name)
		}
	}
	if api.calls() != 0 {
		t.Errorf("POST calls = %d, want 0 (an invalid request must never POST)", api.calls())
	}
	if wake.count() != 0 {
		t.Errorf("wake count = %d, want 0 (an invalid request must not wake the reconciler)", wake.count())
	}
	if g.tripCount() != 0 {
		t.Errorf("trip count = %d, want 0", g.tripCount())
	}
	ins, lerr := st.LoadUnresolvedIntents(ctx)
	if lerr != nil {
		t.Fatalf("LoadUnresolvedIntents: %v", lerr)
	}
	if len(ins) != 0 {
		t.Errorf("journal holds %d intents, want 0 (no invalid request may create prepared/submit-attempted)", len(ins))
	}
}

// TestNewSubmitterValidatesDeps pins that missing required dependencies are
// rejected at construction rather than nil-panicking mid-submit (unattended
// safety).
func TestNewSubmitterValidatesDeps(t *testing.T) {
	st := newStore(t)
	base := SubmitterConfig{
		Journal: st, Audit: &fakeAudit{}, Guard: allowingGuard(), API: &stubAPI{}, AccountSeq: 1, Wake: func() {},
	}
	if _, err := NewSubmitter(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	mut := func(f func(*SubmitterConfig)) SubmitterConfig { c := base; f(&c); return c }
	cases := map[string]SubmitterConfig{
		"nil journal": mut(func(c *SubmitterConfig) { c.Journal = nil }),
		"nil audit":   mut(func(c *SubmitterConfig) { c.Audit = nil }),
		"nil guard":   mut(func(c *SubmitterConfig) { c.Guard = nil }),
		"nil api":     mut(func(c *SubmitterConfig) { c.API = nil }),
	}
	for name, cfg := range cases {
		if _, err := NewSubmitter(cfg); err == nil {
			t.Errorf("%s: NewSubmitter accepted a missing dependency", name)
		}
	}
}

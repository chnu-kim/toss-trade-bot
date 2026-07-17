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
// can be told to fail-closed on a specific marker.
type fakeAudit struct {
	mu           sync.Mutex
	events       []audit.OrderLifecycleEvent
	failClosedOn string // marker to fail-close on; "" = never
}

func (a *fakeAudit) EmitOrderLifecycle(_ context.Context, ev audit.OrderLifecycleEvent) (audit.Ack, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	if a.failClosedOn != "" && ev.Marker == a.failClosedOn {
		return audit.Ack{}, &audit.FailClosedError{Op: "test", Err: errors.New("disk full")}
	}
	return audit.Ack{IdempotencyKey: ev.IntentID + ":" + ev.Marker}, nil
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

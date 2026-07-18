package reconciler

import (
	"context"
	"errors"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// These tests are the regression suite for ADR-0014 Decision 8 (Consequence (f)):
// the reconciler reads NO halt carrier to gate resolve/finalize, and that is safe
// because the persistence-wins hazard is unreachable on these paths — ambiguous
// intents are structurally unresolvable, and order-failure resolution is fenced by
// count-before-resolve. The other half of the decision matters just as much: a
// halt-phase guard would FREEZE finalization for as long as a human-clear-only
// halt (or a bootHalt) stands, which is an availability self-injury with no safety
// gain.

// --- (f)(1) order-failure evidence survives both failure arms ---------------

// txInjectingStore lets a test tamper with the transaction killswitch runs its
// count-first increment in, without modifying internal/killswitch or
// internal/store.
type txInjectingStore struct {
	killswitch.Store
	wrap func(store.Tx) store.Tx
}

func (s txInjectingStore) Atomically(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.Atomically(ctx, func(tx store.Tx) error { return fn(s.wrap(tx)) })
}

// errOnSetCounterTx makes the durable counter write fail, which rolls the whole
// transaction back (counter unchanged, threshold TripHalt unwritten).
type errOnSetCounterTx struct {
	store.Tx
	err error
}

func (t errOnSetCounterTx) SetCounter(context.Context, store.Counter) error { return t.err }

// panicAfterSetCounterTx increments the counter inside the transaction and THEN
// panics, which is the arm ADR-0014 Decision 8 reasons about: Atomically's
// deferred Rollback runs during the panic unwind BEFORE withTripCarrier's
// recover, so the increment never becomes durable and the caller still sees a
// non-nil error.
type panicAfterSetCounterTx struct {
	store.Tx
}

func (t panicAfterSetCounterTx) SetCounter(ctx context.Context, c store.Counter) error {
	if err := t.Tx.SetCounter(ctx, c); err != nil {
		return err
	}
	panic("injected panic after the counter increment, inside the store transaction")
}

func TestNoGuard_OrderFailureEvidenceSurvivesStoreError(t *testing.T) {
	db, path := openStore(t)
	injected := errors.New("durable medium failure")
	sw := newSwitch(t, txInjectingStore{
		Store: db,
		wrap:  func(tx store.Tx) store.Tx { return errOnSetCounterTx{Tx: tx, err: injected} },
	}, defaultKillswitchConfig())
	r := newRigWith(t, db, path, sw)

	seedAcked(t, r.db, "i-rej", "AAA", "ord-1")
	r.api.set("ord-1", order.OrderStatusRejected, "AAA")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if !r.log.contains("report-order-failure-error") {
		t.Fatal("expected the injected durable failure to surface from ReportOrderFailure")
	}
	// The whole point: the intent stays as re-count evidence.
	assertUnresolved(t, r.path, "i-rej")
	if got := orderFailureCount(t, r.db); got != 0 {
		t.Fatalf("counter = %d, want 0 — the failed transaction must have rolled back", got)
	}
	if r.log.contains("resolve:i-rej:" + ResolutionRejected) {
		t.Fatal("the intent was resolved despite the failure count never becoming durable (permanent undercount)")
	}
}

// TestNoGuard_OrderFailureEvidenceSurvivesPanic verifies the panic arm the ADR
// reasons about, and specifically that the blocking does NOT depend on bootHalt:
// what keeps the evidence is the non-nil return plus the transaction rollback.
func TestNoGuard_OrderFailureEvidenceSurvivesPanic(t *testing.T) {
	db, path := openStore(t)
	sw := newSwitch(t, txInjectingStore{
		Store: db,
		wrap:  func(tx store.Tx) store.Tx { return panicAfterSetCounterTx{Tx: tx} },
	}, defaultKillswitchConfig())
	r := newRigWith(t, db, path, sw)

	seedAcked(t, r.db, "i-rej", "AAA", "ord-1")
	r.api.set("ord-1", order.OrderStatusRejected, "AAA")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if !r.log.contains("report-order-failure-error") {
		t.Fatal("a panic inside the count transaction must surface as a non-nil error to the caller")
	}
	// Directly confirm the ADR's claim: the increment is NOT durable, because
	// Atomically's deferred Rollback runs before killswitch's recover.
	if got := orderFailureCount(t, r.db); got != 0 {
		t.Fatalf("counter = %d, want 0 — the panic must have rolled the increment back", got)
	}
	assertUnresolved(t, r.path, "i-rej")
}

// --- (f)(2) ambiguous is structurally inviolable ----------------------------

// TestNoGuard_AmbiguousIsStructurallyUnresolvable is the structural argument that
// lets Decision 8 drop the persistence-wins guard: an ambiguous intent has no
// acked marker, therefore no orderId, therefore no lookup handle, therefore no
// path by which the reconciler could ever resolve it — and with ResolvedAt nil,
// FinalizeFullyAudited refuses to prune-gate it. The reconstruction evidence for
// an ambiguous-triggered halt can never be destroyed by this component.
func TestNoGuard_AmbiguousIsStructurallyUnresolvable(t *testing.T) {
	r := newRig(t, withThreshold(99))
	seedSubmitAttempted(t, r.db, "i-amb", "AAA")
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	// Many cycles, including with a global halt standing and with a bootHalt set.
	for i := 0; i < 5; i++ {
		if err := r.cycle(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	assertUnresolved(t, r.path, "i-amb")
	row, _ := readIntentRow(t, r.path, "i-amb")
	if row.fullyAudited {
		t.Fatal("an unresolved ambiguous intent became prune-eligible")
	}
	// The prune gate refuses it directly, too.
	done, err := r.db.FinalizeFullyAudited(context.Background(), "i-amb")
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if done {
		t.Fatal("FinalizeFullyAudited flagged an unresolved intent")
	}
	// No resolve of any kind was even attempted for it.
	for _, resolution := range []string{ResolutionAbortedBeforeSubmit, ResolutionFilled, ResolutionCanceled, ResolutionRejected} {
		if r.log.contains("resolve:i-amb:" + resolution) {
			t.Fatalf("the reconciler resolved an ambiguous intent as %q", resolution)
		}
	}
}

// --- (f)(3) no freeze under a standing halt ---------------------------------

// TestNoGuard_NoFreezeUnderDurableHalt is the codex R2 regression: a global halt
// is human-clear-only and can stand for days. Confirmed-order resolution, the
// prepared-only close, and audit finalization must all keep converging under it —
// they create no new exposure, and freezing them was pure availability loss.
func TestNoGuard_NoFreezeUnderDurableHalt(t *testing.T) {
	r := newRig(t)
	seedPrepared(t, r.db, "i-abort", "AAA")
	seedAcked(t, r.db, "i-fill", "BBB", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "BBB", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "5"},
	})
	r.clock.Advance(2 * minPreparedAbandonWindow)

	// A standing durable global halt (as an operator-visible emergency stop would
	// leave it), plus an in-memory bootHalt.
	if err := r.sw.Trip(context.Background(), killswitch.ScopeGlobal, "", "operator-stop", r.clock.Now()); err != nil {
		t.Fatalf("trip global: %v", err)
	}
	r.sw.BootHalt()
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q, want halted", got)
	}
	if !r.sw.HasUnpersistedPendingHalt() {
		t.Fatal("expected HasUnpersistedPendingHalt to be true with a bootHalt standing")
	}

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Nothing froze.
	assertResolution(t, r.path, "i-abort", ResolutionAbortedBeforeSubmit)
	assertResolution(t, r.path, "i-fill", ResolutionFilled)
	for _, id := range []string{"i-abort", "i-fill"} {
		row, _ := readIntentRow(t, r.path, id)
		if !row.fullyAudited {
			t.Fatalf("audit finalization froze for %q while a halt was standing", id)
		}
	}
	// The halt itself is untouched — the reconciler never clears a global halt.
	if got := haltPhase(t, r.db); got != store.HaltHalted {
		t.Fatalf("halt phase = %q after reconciliation, want it left halted (human-clear-only)", got)
	}
	if allowed, _ := r.canSubmit("BBB"); allowed {
		t.Fatal("submissions were allowed while the global halt stood")
	}
}

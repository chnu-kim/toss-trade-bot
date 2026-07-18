package reconciler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// The three marker branches of ADR-0003 point 2, each against the real store.

// --- branch 1: prepared-only ------------------------------------------------

func TestBranch_PreparedOnly_ClosesAbortedBeforeSubmit(t *testing.T) {
	r := newRig(t)
	seedPrepared(t, r.db, "i-1", "005930")
	// Age it past the abandon window: the POST provably never happened and no
	// submitter can still be working on it.
	r.clock.Advance(2 * minPreparedAbandonWindow)

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertResolution(t, r.path, "i-1", ResolutionAbortedBeforeSubmit)
	// The terminal audit record must exist and be acked, which closes the prune
	// gate for this intent (ADR-0006 point 4).
	row, _ := readIntentRow(t, r.path, "i-1")
	if !row.fullyAudited {
		t.Fatal("prepared-only intent closed but its audit trail was not converged (fully-audited flag unset)")
	}
	// The reconciler never re-issues the order: a re-decision at a restart-stale
	// price belongs to the strategy (ADR-0003 point 4).
	if r.api.callCount("") != 0 {
		t.Fatal("a prepared-only intent must not trigger any order API call")
	}
}

// TestBranch_PreparedOnly_YoungIntentIsLeftAlone is the concurrency guard: on a
// LIVE cycle a submitter may be sitting between its prepared commit and its
// submit-attempted commit. Closing that intent would terminally resolve an order
// that is about to be POSTed for real, dropping a live order out of the
// unresolved set and out of all later reconciliation.
func TestBranch_PreparedOnly_YoungIntentIsLeftAlone(t *testing.T) {
	r := newRig(t)
	seedPrepared(t, r.db, "i-young", "005930")
	// Position the clock relative to the DURABLE prepared marker, one tick before
	// the abandon window elapses.
	preparedAt := markerTimeOf(t, r.db, "i-young", store.MarkerPrepared)
	r.clock.Set(preparedAt.Add(minPreparedAbandonWindow - time.Nanosecond))

	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	assertUnresolved(t, r.path, "i-young")

	r.clock.Set(preparedAt.Add(minPreparedAbandonWindow))
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	assertResolution(t, r.path, "i-young", ResolutionAbortedBeforeSubmit)
}

// --- branch 2: acked (orderId present) --------------------------------------

func TestBranch_Acked_ClosedFilled(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-fill", "005930", "ord-1")
	r.api.setOrder(order.Order{
		OrderID: "ord-1", Symbol: "005930", Status: order.OrderStatusFilled,
		Execution: order.OrderExecution{FilledQuantity: "10", AverageFilledPrice: ptr("1000")},
	})

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertResolution(t, r.path, "i-fill", ResolutionFilled)
	// ADR-0012 Decision 4: a fill resets the consecutive-failure streak.
	if !r.log.contains("report-order-success") {
		t.Fatal("FILLED did not reset the consecutive-order-failure counter")
	}
	// The execution snapshot is audited (Toss has no per-fill event stream, so the
	// cumulative snapshot IS the fill record — ADR-0006).
	if len(r.sink.fillEvents()) != 1 {
		t.Fatalf("want exactly one fill record, got %d", len(r.sink.fillEvents()))
	}
}

func TestBranch_Acked_ClosedCanceledIsNeitherFailureNorSuccess(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-cancel", "005930", "ord-1")
	r.api.set("ord-1", order.OrderStatusCanceled, "005930")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertResolution(t, r.path, "i-cancel", ResolutionCanceled)
	if r.log.contains("report-order-failure") {
		t.Fatal("a cancel must not increment the order-failure counter — it is not a rejection")
	}
	if r.log.contains("report-order-success") {
		t.Fatal("a cancel must not reset the failure streak — it is not a fill")
	}
}

func TestBranch_Acked_OpenOrderIsTrackedNotResolved(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-open", "005930", "ord-1")
	r.api.set("ord-1", order.OrderStatusPending, "005930")

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertUnresolved(t, r.path, "i-open")
	// Exactly ONE lookup: the boot pass classifies and hands the order to the live
	// tracker; it must never poll to completion inline (ADR-0014 Decision 9).
	if got := r.api.callCount("ord-1"); got != 1 {
		t.Fatalf("GetOrder called %d times during the boot scan, want exactly 1 (no inline polling)", got)
	}
}

// TestBranch_Acked_UnknownStatusPreservesAndBlocks covers a status code newer
// than this build (and REPLACED): the truth is not establishable, so the intent
// is preserved and the symbol is blocked rather than guessed closed.
func TestBranch_Acked_UnknownStatusPreservesAndBlocks(t *testing.T) {
	for _, status := range []order.OrderStatus{"SOME_FUTURE_STATUS", order.OrderStatusReplaced} {
		t.Run(string(status), func(t *testing.T) {
			r := newRig(t)
			seedAcked(t, r.db, "i-unknown", "005930", "ord-1")
			r.api.set("ord-1", status, "005930")

			if err := r.boot(); err != nil {
				t.Fatalf("boot: %v", err)
			}

			assertUnresolved(t, r.path, "i-unknown")
			if allowed, reason := r.canSubmit("005930"); allowed {
				t.Fatal("an unclassifiable order status must block its symbol fail-closed")
			} else if reason != "symbol-blocked" {
				t.Fatalf("blocked for %q, want the per-symbol block", reason)
			}
			// Another symbol keeps trading: the block is local, not global.
			if allowed, reason := r.canSubmit("000660"); !allowed {
				t.Fatalf("an unrelated symbol was blocked (%s); the floor must be local", reason)
			}
		})
	}
}

// TestBranch_Acked_LookupFailureLeavesUnresolvedAndDoesNotBlockGate is ADR-0014
// Consequence (c): a bounded-retry exhaustion delays truth, it does not close the
// intent, and it does not hold the replay gate shut.
func TestBranch_Acked_LookupFailureLeavesUnresolvedAndDoesNotBlockGate(t *testing.T) {
	r := newRig(t)
	seedAcked(t, r.db, "i-flaky", "005930", "ord-1")
	r.api.fail("ord-1", errors.New("transport cut"))

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	assertUnresolved(t, r.path, "i-flaky")
	if !r.log.contains("notify-scan-complete") {
		t.Fatal("the replay gate must open even when an acked lookup could not be established")
	}
	if allowed, reason := r.canSubmit("005930"); !allowed {
		t.Fatalf("a transient lookup failure blocked the symbol (%s); that is a delay, not evidence", reason)
	}

	// It is re-driven on the next cycle and converges once the API answers.
	r.api.clearFail("ord-1")
	r.api.set("ord-1", order.OrderStatusFilled, "005930")
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	assertResolution(t, r.path, "i-flaky", ResolutionFilled)
}

// TestBranch_Acked_RealHTTPSurface drives the acked branch through the REAL
// order client over the REAL toss client against httptest, so the decode and
// identity guards on GET /orders/{orderId} are exercised end to end rather than
// stubbed.
func TestBranch_Acked_RealHTTPSurface(t *testing.T) {
	var seenPath, seenAccount string
	api := httpOrderAPI(t, func(w http.ResponseWriter, req *http.Request) {
		seenPath = req.URL.Path
		seenAccount = req.Header.Get("X-Tossinvest-Account")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"ord-http","symbol":"005930","status":"FILLED",
			"quantity":"10","execution":{"filledQuantity":"10","averageFilledPrice":"1000"}}}`))
	})

	db, path := openStore(t)
	sw := newSwitch(t, db, defaultKillswitchConfig())
	r := newRigWith(t, db, path, sw)
	r.rec.api = api // swap in the real client over httptest

	seedAcked(t, r.db, "i-http", "005930", "ord-http")
	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if seenPath != "/api/v1/orders/ord-http" {
		t.Fatalf("looked up %q, want the orderId detail path", seenPath)
	}
	if seenAccount != "4242" {
		t.Fatalf("account header = %q, want the configured accountSeq", seenAccount)
	}
	assertResolution(t, r.path, "i-http", ResolutionFilled)
}

// --- branch 3: ambiguous ----------------------------------------------------

// TestBranch_Ambiguous_BlocksSymbolAndIsNeverGuessed is the core ADR-0003
// point 3 assertion: an ambiguous submit is blocked locally, never demoted to
// ABSENT, and never auto-acked from a payload match.
func TestBranch_Ambiguous_BlocksSymbolAndIsNeverGuessed(t *testing.T) {
	r := newRig(t, withThreshold(5))
	seedSubmitAttempted(t, r.db, "i-amb", "005930")
	r.pastSettle()

	if err := r.boot(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Never demoted to ABSENT (i.e. never closed as aborted-before-submit) and
	// never auto-acked.
	assertUnresolved(t, r.path, "i-amb")
	row, _ := readIntentRow(t, r.path, "i-amb")
	if row.resolution != "" {
		t.Fatalf("ambiguous intent carries resolution %q; it must never be closed by the reconciler", row.resolution)
	}
	if row.fullyAudited {
		t.Fatal("an unresolved ambiguous intent must never become prune-eligible")
	}

	// The symbol is blocked (unconditional floor), globally everything else runs.
	if allowed, reason := r.canSubmit("005930"); allowed {
		t.Fatal("one ambiguous submit must block its symbol immediately")
	} else if reason != "symbol-blocked" {
		t.Fatalf("blocked for %q, want the per-symbol floor rather than a global halt", reason)
	}
	if allowed, _ := r.canSubmit("000660"); !allowed {
		t.Fatal("a single ambiguous submit must not stop the whole bot below the backlog threshold")
	}
	if haltPhase(t, r.db) != "none" {
		t.Fatal("a single ambiguous submit must not trip the durable global halt")
	}

	// No order lookup is even possible: there is no orderId handle. This is the
	// structural reason the reconciler cannot auto-resolve an ambiguous intent.
	if r.api.callCount("") != 0 {
		t.Fatal("an ambiguous intent has no orderId handle; no lookup may be attempted")
	}
}

// TestBranch_Ambiguous_SettleWindowBoundary pins the boundary with an injected
// clock: before the window the intent is still settling (a POST may be in
// flight), at the window it becomes ambiguous.
func TestBranch_Ambiguous_SettleWindowBoundary(t *testing.T) {
	const window = 30 * time.Second
	r := newRig(t, withSettleWindow(window), withThreshold(5))
	seedSubmitAttempted(t, r.db, "i-boundary", "005930")

	// Step to exactly one tick before the window elapses, measured from the
	// durable marker the code itself branches on.
	seededAt := markerTimeOf(t, r.db, "i-boundary", store.MarkerSubmitAttempted)
	r.clock.Set(seededAt.Add(window - time.Nanosecond))
	if err := r.boot(); err != nil { // boot also opens the replay gate
		t.Fatalf("boot: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); !allowed {
		t.Fatal("the symbol was blocked BEFORE the settle window elapsed")
	}

	r.clock.Set(seededAt.Add(window))
	if err := r.cycle(); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if allowed, _ := r.canSubmit("005930"); allowed {
		t.Fatal("the symbol was not blocked once the settle window elapsed (inclusive boundary)")
	}
}

// markerTimeOf reads a durable marker's journal timestamp, so a window-boundary
// assertion is made against the timestamp the code actually branches on rather
// than an assumed one (the store stamps markers with wall time).
func markerTimeOf(t *testing.T, db *store.DB, intentID string, kind store.MarkerKind) time.Time {
	t.Helper()
	intents, err := db.LoadUnresolvedIntents(context.Background())
	if err != nil {
		t.Fatalf("load unresolved: %v", err)
	}
	for _, in := range intents {
		if in.IntentID != intentID {
			continue
		}
		for _, m := range in.Markers {
			if m.Kind == kind {
				return m.At
			}
		}
	}
	t.Fatalf("no %s marker for %q", kind, intentID)
	return time.Time{}
}

func ptr[T any](v T) *T { return &v }

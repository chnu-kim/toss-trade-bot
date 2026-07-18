package reconciler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// class is the ADR-0003 marker branch of an unresolved intent. The branch is a
// pure function of the journal state plus the clock, which is exactly why the
// per-symbol ambiguous block does not need to be persisted: a restart re-derives
// it (ADR-0004 point 4 restart asymmetry).
type class int

const (
	// classPreparedOnly has no submit-attempted marker, so the POST provably never
	// happened and the intent closes as aborted-before-submit.
	classPreparedOnly class = iota
	// classAcked carries an orderId — the one truth handle — so GET
	// /orders/{orderId} establishes what really happened.
	classAcked
	// classSettling has submit-attempted but no orderId yet and is still inside the
	// settle window: the submit path may be between its POST and its acked commit
	// right now. It is not ambiguous yet, but its truth IS undetermined, so it
	// participates in the success-reset ordering guard.
	classSettling
	// classAmbiguous has submit-attempted, no orderId, and an elapsed settle
	// window. There is no handle to look it up with, so it can never be
	// auto-resolved: it is blocked locally and escalated by backlog, and only a
	// human can retire it (ADR-0003 point 3).
	classAmbiguous
)

func (c class) String() string {
	switch c {
	case classPreparedOnly:
		return "prepared-only"
	case classAcked:
		return "acked"
	case classSettling:
		return "settling"
	case classAmbiguous:
		return "unresolved-ambiguous"
	default:
		return "unknown"
	}
}

// intentView is one unresolved intent reduced to what the reconciler branches on.
type intentView struct {
	intent store.Intent
	// class is the marker branch at the moment of the scan.
	class class
	// orderID is the acked handle, empty for every other class.
	orderID string
	// submitAttemptedAt is the journal time of the submit-attempted marker; it is
	// the occurredAt this package reports for ambiguous evidence and the ordering
	// key for the success-reset guard. Zero for a prepared-only intent.
	submitAttemptedAt time.Time
	// preparedAt is the journal time of the prepared marker (falling back to the
	// intent creation time), which ages a prepared-only intent for abandonment.
	preparedAt time.Time
	// symbol is decoded from the intent payload; symbolErr is non-nil when it
	// could not be recovered (see symbolOf).
	symbol    string
	symbolErr error
}

// classify reduces an unresolved intent to its marker branch at time now.
//
// The acked branch requires a NON-EMPTY orderId: an acked marker without one
// carries no truth handle, so treating it as acked would mean "look up the order
// with an empty id". It is deliberately folded into the ambiguous branch instead
// — no handle means no auto-resolution, which is the fail-closed direction.
func classify(in store.Intent, now time.Time, settleWindow time.Duration) intentView {
	v := intentView{intent: in, preparedAt: in.CreatedAt}
	var submitAttempted bool
	for _, m := range in.Markers {
		switch m.Kind {
		case store.MarkerPrepared:
			if !m.At.IsZero() {
				v.preparedAt = m.At
			}
		case store.MarkerSubmitAttempted:
			submitAttempted = true
			// Markers load in durable append order; keep the first submit-attempted
			// time, which is when the POST could first have reached the server.
			if v.submitAttemptedAt.IsZero() {
				v.submitAttemptedAt = m.At
			}
		case store.MarkerAcked:
			if m.OrderID != "" {
				v.orderID = m.OrderID
			}
		}
	}
	v.symbol, v.symbolErr = symbolOf(in)

	switch {
	case v.orderID != "":
		v.class = classAcked
	case !submitAttempted:
		v.class = classPreparedOnly
	case now.Sub(v.submitAttemptedAt) >= settleWindow:
		v.class = classAmbiguous
	default:
		v.class = classSettling
	}
	return v
}

// symbolOf recovers the instrument from the intent payload, which the submit path
// wrote as json.Marshal(order.OrderRequest) (#34). The symbol is what a per-symbol
// Trip/ClearSymbol addresses, so failing to recover it is not cosmetic: the local
// floor could not be applied. Callers escalate that case globally rather than
// leaving an ambiguous intent unblocked.
func symbolOf(in store.Intent) (string, error) {
	if len(in.Payload) == 0 {
		return "", fmt.Errorf("reconciler: intent %q has an empty payload", in.IntentID)
	}
	var req order.OrderRequest
	if err := json.Unmarshal(in.Payload, &req); err != nil {
		return "", fmt.Errorf("reconciler: decode payload of intent %q: %w", in.IntentID, err)
	}
	if req.Symbol == "" {
		return "", fmt.Errorf("reconciler: intent %q payload carries no symbol", in.IntentID)
	}
	return req.Symbol, nil
}

// verdict is what a single GetOrder classification established about an acked
// intent.
type verdict int

const (
	// verdictLookupFailed means GET /orders/{orderId} did not answer (the toss
	// client's own bounded backoff was exhausted, or the API returned an error).
	// Nothing is resolved — guessing a terminal state for a possibly-live order is
	// exactly the money-unsafe move ADR-0003 forbids — so the intent stays
	// unresolved for a later re-drive (ADR-0014 Decision 10) and counts as in-doubt
	// for the success-reset ordering guard. It does NOT block the symbol: a
	// transient lookup failure is a delay, not evidence of an unidentified order.
	verdictLookupFailed verdict = iota
	// verdictUnknownStatus means the lookup answered with a status this build
	// cannot classify as open or terminal (a code newer than this build, or
	// REPLACED, where the exposure moved to an orderId this bot never recorded).
	// Unlike a transient lookup failure this is a standing "we do not know what
	// this order is doing", so it blocks the symbol fail-closed as well as staying
	// unresolved (ADR-0003: preserve and block what cannot be established).
	verdictUnknownStatus
	// verdictOpen means the order is still working. Truth IS established (an open
	// order is not a rejection), so it does not hold back a success reset; it is
	// tracked non-blockingly for fill deltas.
	verdictOpen
	// verdictFilled is a terminal fill — the success signal that resets the
	// consecutive-failure counter, subject to the ordering guard.
	verdictFilled
	// verdictCanceled is terminal but is NEITHER a failure nor a success: a cancel
	// is not an order failure, so it must not increment the failure counter, and it
	// is not a fill, so it must not reset the streak either.
	verdictCanceled
	// verdictRejected is a terminal failure — the count-before-resolve path.
	verdictRejected
)

// undetermined reports whether the verdict left the intent's truth unestablished,
// which is what holds back a later fill's success reset (ADR-0014 Decision 8).
func (v verdict) undetermined() bool {
	return v == verdictLookupFailed || v == verdictUnknownStatus
}

// classifyStatus maps a Toss OrderStatus onto a verdict.
//
// Only FILLED/CANCELED/REJECTED are treated as terminal. Everything else — the
// pending states, PARTIAL_FILLED, and the cancel/replace-rejected states, which
// leave the underlying order working — is open. REPLACED and any status code
// newer than this build fall through to verdictUnknownStatus rather than being
// guessed: REPLACED means the exposure moved to an orderId this bot never
// recorded, so closing the intent would silently drop a live order from the
// journal.
func classifyStatus(s order.OrderStatus) verdict {
	switch s {
	case order.OrderStatusFilled:
		return verdictFilled
	case order.OrderStatusCanceled:
		return verdictCanceled
	case order.OrderStatusRejected:
		return verdictRejected
	case order.OrderStatusPending, order.OrderStatusPendingCancel, order.OrderStatusPendingReplace,
		order.OrderStatusPartialFilled, order.OrderStatusCancelRejected, order.OrderStatusReplaceRejected:
		return verdictOpen
	default:
		return verdictUnknownStatus
	}
}

// resolutionFor maps a terminal verdict to its journal resolution string.
func resolutionFor(v verdict) (string, bool) {
	switch v {
	case verdictFilled:
		return ResolutionFilled, true
	case verdictCanceled:
		return ResolutionCanceled, true
	case verdictRejected:
		return ResolutionRejected, true
	default:
		return "", false
	}
}

// snapshotOf converts an order execution into the audit fill snapshot, carrying
// every financial field as the raw API decimal string so the audit digest stays
// exact (ADR-0006). A nil (JSON null) field becomes the empty string, which stays
// distinct from a real "0".
func snapshotOf(ex order.OrderExecution) audit.FillSnapshot {
	return audit.FillSnapshot{
		FilledQuantity:     ex.FilledQuantity,
		AverageFilledPrice: deref(ex.AverageFilledPrice),
		FilledAmount:       deref(ex.FilledAmount),
		Commission:         deref(ex.Commission),
		Tax:                deref(ex.Tax),
		SettlementDate:     deref(ex.SettlementDate),
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// isZeroDecimal reports whether a raw API decimal string represents zero, without
// ever converting it to a float (a float round-trip would make the audit digest
// lossy — ADR-0006). Empty, "0", "0.00" and "+0" are all zero; anything with a
// non-zero digit is not.
func isZeroDecimal(s string) bool {
	for _, c := range s {
		if c >= '1' && c <= '9' {
			return false
		}
	}
	return true
}

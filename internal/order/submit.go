package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// This file implements the intent submit path (#34): the fail-closed, idempotent,
// write-ahead sequence that turns a strategy intent into at most one real POST
// /api/v1/orders. The governing decisions are ADR-0002 (2-marker journal),
// ADR-0003 (no inline verify; ambiguous submit fail-closed), ADR-0004 (kill-switch
// guard on the new-exposure edge, with a TOCTOU final re-check), ADR-0005 (each
// marker is its own durable commit), ADR-0006 (synchronous durable audit per
// transition; fail-closed escalates to a global halt), and ADR-0012 (TripTx-free;
// order does not resolve order-failures — that is the reconciler's #35 job).
//
// The four seams below are deliberately NARROW, and that narrowness is itself a
// safety invariant (AC "count-first 불변식 가드"):
//
//   - journal has no ResolveIntent and no Atomically, so order CANNOT resolve an
//     intent (order-failure resolution + count-first ordering belong to the
//     reconciler #35, ADR-0012 Decision 3) and CANNOT bind a kill-switch write
//     into an order transaction (TripTx-free, ADR-0012 Decision 2).
//   - guard has no ReportOrderFailure, so order CANNOT report an order-failure —
//     in particular an ambiguous submit is never mis-wired to the order-failure
//     counter (that is a separate trigger owned by the reconciler; double-counting
//     is thereby structurally impossible, ADR-0012 #34 함의 c).

// Compile-time proof that the concrete production types satisfy the narrow seams
// the submit path depends on. This is the wiring contract (#36 injects exactly
// these) and catches an upstream signature drift at build time rather than at
// wire-up. *Client is this package's own #33 POST wrapper.
var (
	_ journal   = (*store.DB)(nil)
	_ auditSink = (*audit.Writer)(nil)
	_ guard     = (*killswitch.Switch)(nil)
	_ submitAPI = (*Client)(nil)
)

// journal is the write-ahead outbox seam order writes (ADR-0002/0005). It is the
// narrow slice of store.Store the submit path needs: append the prepared intent,
// append the later markers, and scan the live set for idempotency. It exposes
// neither ResolveIntent nor Atomically — see the invariant note above. *store.DB
// satisfies it.
type journal interface {
	AppendIntent(ctx context.Context, in store.Intent) error
	AppendMarker(ctx context.Context, intentID string, kind store.MarkerKind, orderID string) error
	LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error)
}

// auditSink is the synchronous-durable audit seam (ADR-0006). *audit.Writer
// satisfies it; a FailClosedError from EmitOrderLifecycle is the non-durable
// signal order escalates on. EmitError durably preserves an orphaned orderId when
// the journal itself fails (a diagnostic record on the audit's INDEPENDENT durable
// medium — ADR-0006 point 6 — so the sole truth handle is not lost outright).
type auditSink interface {
	EmitOrderLifecycle(ctx context.Context, ev audit.OrderLifecycleEvent) (audit.Ack, error)
	EmitError(ctx context.Context, ev audit.ErrorEvent) (audit.Ack, error)
}

// guard is the fail-closed kill-switch seam (ADR-0004). CanSubmit gates the edge
// before the prepared append; Reserve+Reconfirm is the TOCTOU-safe final re-check
// immediately before the irreversible submit; Trip escalates a global halt when
// audit goes fail-closed. It exposes no ReportOrderFailure — see the invariant
// note above. *killswitch.Switch satisfies it.
type guard interface {
	CanSubmit(symbol string) (allowed bool, reason string)
	Reserve(symbol string) killswitch.Reservation
	Reconfirm(r killswitch.Reservation) (allowed bool, reason string)
	Trip(ctx context.Context, scope killswitch.Scope, symbol, reason string, occurredAt time.Time) error
}

// submitAPI is the single-POST order wrapper seam (#33). *order.Client satisfies
// it. SubmitOrder issues exactly one POST and never auto-retries; a returned
// error is AMBIGUOUS (the order may already have reached the server) and must
// never be read as "safe to resubmit".
type submitAPI interface {
	SubmitOrder(ctx context.Context, accountSeq int64, req OrderRequest) (OrderResponse, error)
}

// WakeFunc wakes the reconciler (#35) after the submit path leaves an intent in
// an unresolved state whose truth only the reconciler can establish (ADR-0003
// point 1: no inline synchronous verify — the submit goroutine must not block on
// truth recovery). It must be non-blocking. order does not import reconciler; the
// concrete wake channel is wired at main (#36).
type WakeFunc func()

// Intent is a strategy-issued order intent: the strategy-owned unique intentId
// (ADR-0002 point 2) plus the order to place. order derives the clientOrderId
// from intentId and overwrites Request.ClientOrderID, so the caller need not (and
// should not) set it. order never imports strategy — this is the contract the
// strategy fulfils, expressed in order's own types.
type Intent struct {
	IntentID string
	Request  OrderRequest
}

// Status is the terminal disposition of a SubmitIntent call.
type Status int

const (
	// StatusUnknown is the zero value; never returned deliberately.
	StatusUnknown Status = iota
	// StatusAcked means the POST returned an orderId and the acked marker is
	// durable — the order is placed and its handle recorded.
	StatusAcked
	// StatusUnresolved means the POST outcome is unknown (error/timeout, or a
	// post-POST bookkeeping failure). The intent is left unresolved for the
	// reconciler (#35); the wake seam has been fired. NOT safe to resubmit.
	StatusUnresolved
	// StatusBlocked means the kill-switch blocked the submit — either at the
	// initial CanSubmit (nothing written) or at the final Reconfirm (prepared-only,
	// left for the reconciler to close as aborted-before-submit).
	StatusBlocked
	// StatusDuplicate means an intent with this intentId already exists in the
	// journal; the existing state is returned and NO second POST was made (ADR-0002
	// point 2 idempotency).
	StatusDuplicate
)

func (s Status) String() string {
	switch s {
	case StatusAcked:
		return "acked"
	case StatusUnresolved:
		return "unresolved"
	case StatusBlocked:
		return "blocked"
	case StatusDuplicate:
		return "duplicate"
	default:
		return "unknown"
	}
}

// Outcome is the non-error result of SubmitIntent. Reason carries a block reason
// or an ambiguous-submit cause for logging. OrderID is set on StatusAcked (and on
// StatusDuplicate when the existing intent had already been acked).
type Outcome struct {
	Status   Status
	IntentID string
	OrderID  string
	Reason   string
}

// SubmitterConfig wires a Submitter's dependencies. Journal/Audit/Guard/API are
// required (NewSubmitter rejects a nil one rather than nil-panicking mid-submit —
// unattended safety). Now defaults to time.Now when nil. Wake may be nil (the
// reconciler's periodic scan still reclaims unresolved intents), but production
// wiring (#36) should provide it.
type SubmitterConfig struct {
	Journal    journal
	Audit      auditSink
	Guard      guard
	API        submitAPI
	AccountSeq int64
	Wake       WakeFunc
	Now        func() time.Time
}

// Submitter runs the intent submit path. It holds no mutable state of its own:
// every submit's durable truth lives in the journal, so a Submitter is safe for
// concurrent use as far as its own fields go (the underlying store serializes
// writes).
type Submitter struct {
	journal    journal
	audit      auditSink
	guard      guard
	api        submitAPI
	accountSeq int64
	wake       WakeFunc
	now        func() time.Time
}

// NewSubmitter validates cfg and builds a Submitter.
func NewSubmitter(cfg SubmitterConfig) (*Submitter, error) {
	if cfg.Journal == nil {
		return nil, fmt.Errorf("order: NewSubmitter requires a Journal")
	}
	if cfg.Audit == nil {
		return nil, fmt.Errorf("order: NewSubmitter requires an Audit sink")
	}
	if cfg.Guard == nil {
		return nil, fmt.Errorf("order: NewSubmitter requires a Guard (kill-switch)")
	}
	if cfg.API == nil {
		return nil, fmt.Errorf("order: NewSubmitter requires an API")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Submitter{
		journal:    cfg.Journal,
		audit:      cfg.Audit,
		guard:      cfg.Guard,
		api:        cfg.API,
		accountSeq: cfg.AccountSeq,
		wake:       cfg.Wake,
		now:        now,
	}, nil
}

// durablePersistTimeout bounds every detached "must persist despite caller
// cancellation" operation on the submit path: recording an accepted order's orderId
// (the acked marker), each mandatory lifecycle-audit emit, the orphan-orderId
// preservation when the store fails, and the global-halt escalation trip. Each runs
// on context.WithoutCancel so a cancelled caller cannot drop it, but bounded so a
// wedged medium cannot hang the submit goroutine forever (unattended safety). A
// local fsync-durable write finishes well within this bound; it only caps a
// pathological stall.
const durablePersistTimeout = 30 * time.Second

// SubmitIntent runs the fail-closed 2-marker write-ahead submit sequence for one
// strategy intent, making at most one POST. The sequence (ADR-0002/0003/0004/
// 0005/0006):
//
//  0. Idempotent replay — an intent already in the journal returns its current
//     state with no second POST (ADR-0002 point 2).
//  1. CanSubmit(symbol) BEFORE the prepared append; blocked ⇒ nothing written.
//  2. prepared (AppendIntent, its own durable commit) + durable audit.
//  3. Final fail-closed Reconfirm immediately before the irreversible submit;
//     blocked ⇒ abort prepared-only (ADR-0004 point 1 TOCTOU).
//  4. submit-attempted (its own durable commit) + durable audit.
//  5. exactly one POST (#33) — never auto-retried.
//  6. success ⇒ acked+orderId (its own durable commit) + durable audit;
//     error/timeout/ambiguous ⇒ leave unresolved + wake the reconciler (#35). No
//     resubmit, no ResolveIntent, no ReportOrderFailure (delegated to #35).
//
// A returned error signals a structural failure (encode, a durable journal write,
// or an audit fail-closed that also tripped the global halt); the normal
// delegated dispositions (blocked, unresolved, duplicate) come back as an Outcome
// with a nil error.
func (s *Submitter) SubmitIntent(ctx context.Context, intent Intent) (Outcome, error) {
	if intent.IntentID == "" {
		return Outcome{}, fmt.Errorf("order: SubmitIntent requires a non-empty intentId (strategy contract, ADR-0002 point 2)")
	}

	// clientOrderId is derived deterministically from intentId (ADR-0002 point 4)
	// and overwrites any caller-provided value — order owns this derivation. Assemble
	// the exact request we will POST now so it can be structurally validated before
	// anything durable happens.
	clientOrderID := DeriveClientOrderID(intent.IntentID)
	req := intent.Request
	req.ClientOrderID = clientOrderID

	// Structural validation gate — BEFORE any journal write, idempotency scan, or
	// guard check. A request that fails req.validate() (missing symbol/side/
	// orderType, or the quantity/orderAmount oneOf) is rejected by Client.SubmitOrder
	// *before* the network POST, so the POST could never happen. Writing any marker
	// for it — above all submit-attempted, which by ADR-0002 means "POST may have
	// happened" — would forge a false ambiguous state the reconciler can never
	// resolve against Toss. A contract-violating request must therefore never enter
	// the journal: reject it here so nothing is written, no POST, no wake, no trip.
	if err := req.validate(); err != nil {
		return Outcome{}, fmt.Errorf("order: intent %q has a structurally invalid order request: %w", intent.IntentID, err)
	}

	symbol := req.Symbol

	// 0. Idempotent replay. order never resolves intents, so any prior call for
	// this intentId is still in the unresolved set — a replay finds it here and
	// returns its state without a second POST. (The intent_id PRIMARY KEY is the
	// ultimate barrier; this scan also lets us return the existing state.)
	//
	// KNOWN LIMITATION (idempotency completeness, Finding B): lookup scans only the
	// UNRESOLVED set. Once the reconciler (#35) begins resolving intents, a resolved
	// intent's PK row remains, so re-submitting a resolved intentId is missed by both
	// this preflight scan and the post-AppendIntent fallback — it surfaces as a PK
	// collision hard error instead of returning the prior outcome. This is NOT a
	// money-safety gap: the PK barrier still guarantees no duplicate POST; it is only
	// the "return existing state" contract that is incomplete, and only after #35
	// exists AND a strategy violates its contract by reusing a resolved intentId.
	// Full idempotency (returning the prior outcome) needs a single-intent store read
	// that includes resolved rows — a #35/store follow-up, out of #34's store-no-edit
	// scope.
	if existing, found, err := s.lookup(ctx, intent.IntentID); err != nil {
		return Outcome{}, err
	} else if found {
		return duplicateOutcome(existing), nil
	}

	// 1. Kill-switch guard on the new-exposure edge, BEFORE the prepared append
	// (ADR-0004 point 1). Blocked ⇒ write nothing at all.
	if allowed, reason := s.guard.CanSubmit(symbol); !allowed {
		return Outcome{Status: StatusBlocked, IntentID: intent.IntentID, Reason: reason}, nil
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("order: encode intent %q payload: %w", intent.IntentID, err)
	}

	// 2. prepared — its own durable commit (ADR-0005 point 3; AppendIntent records
	// the prepared marker atomically with the intent row).
	rec := store.Intent{IntentID: intent.IntentID, ClientOrderID: clientOrderID, Payload: payload}
	if err := s.journal.AppendIntent(ctx, rec); err != nil {
		// A concurrent submit for the same intentId may have won the PRIMARY-KEY
		// race and created the row first. If the intent now exists, this is an
		// idempotent replay (return existing state, no POST, no trip).
		if existing, found, lerr := s.lookup(ctx, intent.IntentID); lerr == nil && found {
			return duplicateOutcome(existing), nil
		}
		if isCallerCanceled(ctx, err) {
			// The caller cancelled/timed out before anything was POSTed — a clean
			// abort, not a durability-medium failure. Nothing durable was recorded;
			// return the error without a halt (ADR-0005 point 6 escalates the medium
			// failing, not the caller walking away).
			return Outcome{}, fmt.Errorf("order: prepared append aborted for %q: %w", intent.IntentID, err)
		}
		// Otherwise the prepared append GENUINELY failed to durably record. If we
		// cannot journal the intent we cannot safely submit — fail-closed (ADR-0005
		// point 6), symmetric to the audit-failure escalation. Nothing was POSTed.
		return Outcome{}, s.tripOnStoreFailure(ctx, store.MarkerPrepared, err)
	}
	if err := s.emit(ctx, intent.IntentID, "", store.MarkerPrepared); err != nil {
		// audit fail-closed already tripped the global halt inside emit; the
		// prepared-only intent is left for the reconciler.
		return Outcome{Status: StatusUnresolved, IntentID: intent.IntentID, Reason: "audit-fail-closed"}, err
	}

	// 3. Final fail-closed re-check immediately before the irreversible submit
	// (ADR-0004 point 1 TOCTOU). Blocked ⇒ abort prepared-only; the reconciler
	// closes it as aborted-before-submit (no new ambiguity). order does NOT resolve
	// it here (that is #35's job — count-first invariant).
	reservation := s.guard.Reserve(symbol)
	if allowed, reason := s.guard.Reconfirm(reservation); !allowed {
		return Outcome{Status: StatusBlocked, IntentID: intent.IntentID, Reason: reason}, nil
	}

	// 4. submit-attempted — its own durable commit. Its presence means "POST may
	// have happened"; its absence means "POST certainly did not" (ADR-0002).
	if err := s.journal.AppendMarker(ctx, intent.IntentID, store.MarkerSubmitAttempted, ""); err != nil {
		// The marker is not durable, so the POST must not proceed (that would be a
		// submit with no "POST may have happened" evidence). Leave prepared-only.
		if isCallerCanceled(ctx, err) {
			// Caller cancellation before the irreversible submit is a clean abort, not
			// a durability-medium failure — no halt (ADR-0005 point 6).
			return Outcome{}, fmt.Errorf("order: submit-attempted append aborted for %q: %w", intent.IntentID, err)
		}
		// A genuine store durability failure → fail-closed: trip the global halt and
		// return a hard error (ADR-0005 point 6). No POST happened.
		return Outcome{}, s.tripOnStoreFailure(ctx, store.MarkerSubmitAttempted, err)
	}
	if err := s.emit(ctx, intent.IntentID, "", store.MarkerSubmitAttempted); err != nil {
		return Outcome{Status: StatusUnresolved, IntentID: intent.IntentID, Reason: "audit-fail-closed"}, err
	}

	// 5. The single POST. Never auto-retried (duplicate-fill risk — CLAUDE.md /
	// ADR-0003 point 4).
	resp, postErr := s.api.SubmitOrder(ctx, s.accountSeq, req)
	if postErr != nil {
		// Ambiguous/failed submit: the order may already have reached the server
		// (ADR-0003). Do NOT resubmit, do NOT resolve, do NOT ReportOrderFailure
		// (ambiguous submit is a SEPARATE trigger the reconciler owns — wiring it to
		// the order-failure counter would double-count, ADR-0012 #34 함의 c). Leave
		// it unresolved and wake the reconciler.
		s.wakeReconciler()
		return Outcome{Status: StatusUnresolved, IntentID: intent.IntentID, Reason: postErr.Error()}, nil
	}

	// 6. acked — its own durable commit — with the orderId that becomes the truth
	// handle (ADR-0002 point 3).
	//
	// The POST was ACCEPTED: the order is irreversible (money moved) and
	// resp.OrderID is its ONLY durable truth handle — clientOrderId is not
	// queryable on the list/detail endpoints (ADR-0002 point 4). Recording that
	// handle therefore must NOT be at the mercy of the caller's ctx: if the caller
	// cancelled or its deadline elapsed during the POST, reusing that ctx here would
	// drop the orderId and, on a crash right after, demote a real live order to an
	// unresolvable ambiguous submit (the reconciler could never confirm its truth).
	// So detach from caller cancellation and give this critical section its own
	// bounded deadline. Everything up to and including the POST used the caller ctx
	// (those steps are still cancellable — nothing irreversible had happened yet);
	// the detach begins only now, once the irreversible act is done.
	ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), durablePersistTimeout)
	defer cancelAck()
	if err := s.journal.AppendMarker(ackCtx, intent.IntentID, store.MarkerAcked, resp.OrderID); err != nil {
		// A GENUINE durable failure (disk/deadline) — caller cancellation is excluded
		// by the detach above. This is NOT an ADR-0003 ambiguous submit (that is a lost
		// POST *response*): the POST succeeded and it is the store MEDIUM that failed, a
		// system-wide durability problem → fail-closed GLOBAL halt (ADR-0005 point 6),
		// not a per-symbol block. Order of operations:
		//   (a) best-effort preserve resp.OrderID — the order's ONLY truth handle
		//       (ADR-0002 point 3) — on the audit channel, an INDEPENDENT durable medium
		//       (ADR-0006 point 6) that may persist when the store did not, so the live
		//       order's handle is not lost outright;
		//   (b) wake the reconciler to examine the dangling (prepared+submit-attempted)
		//       intent;
		//   (c) trip the global halt and return a HARD error (never nil — a nil here
		//       would keep trading other symbols over a broken durability medium).
		s.preserveOrphanOrderID(ctx, intent.IntentID, resp.OrderID, err)
		s.wakeReconciler()
		return Outcome{Status: StatusUnresolved, IntentID: intent.IntentID, OrderID: resp.OrderID,
			Reason: "store-durable-failure:acked"}, s.tripOnStoreFailure(ctx, store.MarkerAcked, err)
	}
	// emit is self-contained cancellation-immune (it detaches internally), so it
	// takes the plain ctx; the acked MARKER write above needs the explicit ackCtx
	// because its orderId persistence is a separate irreversible-handle concern.
	if err := s.emit(ctx, intent.IntentID, resp.OrderID, store.MarkerAcked); err != nil {
		// The order is placed and acked durably; the global halt tripped inside emit
		// stops further submits. Surface the fail-closed error but report the orderId.
		return Outcome{Status: StatusAcked, IntentID: intent.IntentID, OrderID: resp.OrderID,
			Reason: "audit-fail-closed"}, err
	}

	return Outcome{Status: StatusAcked, IntentID: intent.IntentID, OrderID: resp.OrderID}, nil
}

// lookup scans the live (unresolved) journal for intentID. Because order never
// resolves an intent, any prior submit for this intentId is still unresolved and
// found here. The scan is O(live-intents); the live unresolved set is bounded (the
// reconciler closes intents) and store exposes no single-intent read within this
// issue's scope, so this is the available — and adequate — idempotency primitive.
func (s *Submitter) lookup(ctx context.Context, intentID string) (store.Intent, bool, error) {
	intents, err := s.journal.LoadUnresolvedIntents(ctx)
	if err != nil {
		return store.Intent{}, false, fmt.Errorf("order: load intents for idempotency check: %w", err)
	}
	for _, in := range intents {
		if in.IntentID == intentID {
			return in, true, nil
		}
	}
	return store.Intent{}, false, nil
}

// duplicateOutcome builds the idempotent-replay result from an existing intent,
// surfacing its orderId if it has already been acked.
func duplicateOutcome(in store.Intent) Outcome {
	out := Outcome{Status: StatusDuplicate, IntentID: in.IntentID}
	for _, m := range in.Markers {
		if m.Kind == store.MarkerAcked && m.OrderID != "" {
			out.OrderID = m.OrderID
		}
	}
	return out
}

// emit records one lifecycle transition to the audit sink synchronously
// (ADR-0006 point 4). On a fail-closed durability failure it escalates to a
// GLOBAL kill-switch trip through the kill-switch's OWN durable path (store) —
// never the audit medium that just failed (ADR-0006 point 6 breaks the cycle) and
// never bound to an order transaction (TripTx-free, ADR-0012 Decision 2) — then
// returns the wrapped error. A non-fail-closed emit error (ctx cancel, oversize
// record) is returned WITHOUT a trip: it is not a durability-medium failure.
func (s *Submitter) emit(ctx context.Context, intentID, orderID string, marker store.MarkerKind) error {
	// emit is ALWAYS invoked right after the marker's own durable commit succeeded
	// (prepared←AppendIntent, submit-attempted/acked←AppendMarker). By ADR-0006 the
	// audit record for a committed marker is MANDATORY, and a durable-write failure
	// must fail-closed (point 6). This record must therefore not be skippable by
	// caller cancellation: a committed marker with neither its audit nor a halt would
	// be a fail-open hole. So detach from the caller ctx here — uniformly for all
	// three transitions, mirroring the acked marker's own cancellation-immune
	// persistence — bounded so a wedged disk cannot hang the submit goroutine. Only a
	// genuine durability failure now reaches the fail-closed branch below; a cancelled
	// caller no longer masquerades as a non-durable (silently skipped) emit.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durablePersistTimeout)
	defer cancel()
	_, err := s.audit.EmitOrderLifecycle(auditCtx, audit.OrderLifecycleEvent{
		IntentID:   intentID,
		OrderID:    orderID,
		Marker:     string(marker),
		OccurredAt: s.now(),
	})
	if err == nil {
		return nil
	}
	if audit.IsFailClosed(err) {
		reason := "audit-fail-closed:" + string(marker)
		if tripErr := s.guard.Trip(auditCtx, killswitch.ScopeGlobal, "", reason, s.now()); tripErr != nil {
			return fmt.Errorf("order: audit fail-closed at %s AND global trip failed: %w (trip: %v)", marker, err, tripErr)
		}
		return fmt.Errorf("order: audit fail-closed at %s, global halt tripped: %w", marker, err)
	}
	return fmt.Errorf("order: audit emit at %s: %w", marker, err)
}

// isCallerCanceled reports whether a pre-POST marker-write failure is attributable
// to the caller's ctx being cancelled or timed out (those writes run on the caller
// ctx on purpose — a pre-POST cancel is a clean abort with nothing irreversible
// done). Such a failure must NOT trip the global halt: ADR-0005 point 6 escalates a
// durability MEDIUM failure (disk-full, fsync error), not the caller walking away.
// The post-POST acked write is exempt because it runs on a detached ctx, so a caller
// cancel can never reach it — any acked failure there is a genuine durability fault.
func isCallerCanceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// tripOnStoreFailure escalates a store durable-append failure to a global halt
// (fail-closed, ADR-0005 point 6), symmetric to the audit fail-closed escalation in
// emit: if a marker cannot be durably recorded, the submit path cannot safely keep
// running. The trip runs on the kill-switch's OWN durable path (never bound to an
// order transaction — TripTx-free, ADR-0012 Decision 2), on a detached, bounded ctx
// so the halt lands even when the caller ctx was cancelled or the medium is wedged
// (killswitch owns durable-before-visible). It returns a wrapped HARD error — a
// store durability failure is never a nil-error outcome.
func (s *Submitter) tripOnStoreFailure(ctx context.Context, marker store.MarkerKind, cause error) error {
	tripCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durablePersistTimeout)
	defer cancel()
	reason := "store-durable-failure:" + string(marker)
	if tripErr := s.guard.Trip(tripCtx, killswitch.ScopeGlobal, "", reason, s.now()); tripErr != nil {
		return fmt.Errorf("order: durable %s write failed AND global trip failed: %w (trip error: %v)", marker, cause, tripErr)
	}
	return fmt.Errorf("order: durable %s write failed, global halt tripped (fail-closed): %w", marker, cause)
}

// preserveOrphanOrderID best-effort records an accepted order's orderId to the
// audit channel when the journal could not durably record its acked marker. The
// audit sink is an INDEPENDENT durable medium (ADR-0006 point 6), so it may persist
// the sole truth handle (ADR-0002 point 3) even when the store medium failed —
// keeping a real live order recoverable (by an operator or a future tool) instead of
// losing its handle outright. It is best-effort: detached and bounded, and its own
// failure is swallowed (the global trip that follows is the hard stop). It is
// deliberately an ERROR/diagnostic record, not a forged lifecycle "acked" marker,
// so it never fabricates journal-backed lifecycle state the reconciler would trust.
func (s *Submitter) preserveOrphanOrderID(ctx context.Context, intentID, orderID string, cause error) {
	emitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durablePersistTimeout)
	defer cancel()
	_, _ = s.audit.EmitError(emitCtx, audit.ErrorEvent{
		IntentID:   intentID,
		OrderID:    orderID,
		Operation:  "order.submit.acked",
		ErrorClass: "store-durable-failure",
		Message:    fmt.Sprintf("acked marker write failed; orderId %q preserved here as the store journal could not record it: %v", orderID, cause),
		OccurredAt: s.now(),
	})
}

// wakeReconciler fires the wake seam inside a recover boundary: a panicking or
// misbehaving wake callback must never break the submit path (unattended safety).
func (s *Submitter) wakeReconciler() {
	if s.wake == nil {
		return
	}
	defer func() { _ = recover() }()
	s.wake()
}

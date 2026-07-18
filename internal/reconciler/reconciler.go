package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/audit"
	"github.com/chnu-kim/toss-trade-bot/internal/killswitch"
	"github.com/chnu-kim/toss-trade-bot/internal/order"
	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Compile-time proof that the concrete production types satisfy the narrow seams
// this package depends on. This is the wiring contract (#36 injects exactly
// these) and turns an upstream signature drift into a build failure rather than a
// wire-up surprise.
var (
	_ Journal   = (*store.DB)(nil)
	_ Guard     = (*killswitch.Switch)(nil)
	_ OrderAPI  = (*order.Client)(nil)
	_ AuditSink = (*audit.Writer)(nil)
)

// Journal is the write-ahead journal seam the reconciler reads truth candidates
// from and terminally closes intents through. It is the narrow slice of
// store.Store this package needs.
//
// It deliberately exposes NO AppendIntent/AppendMarker (the reconciler must never
// forge a marker or bind a guessed orderId — ADR-0003 point 3), and NO
// Halt/Atomically (ADR-0014 Decision 8 no-guard: resolve/finalize must not be
// gated on any halt carrier, and a kill-switch write must never be bound into a
// reconciler transaction — TripTx-free, ADR-0012 Decision 2). *store.DB satisfies
// it.
type Journal interface {
	// LoadUnresolvedIntents is the restart/live scan of still-open intents — the
	// source of the marker branching (ADR-0003).
	LoadUnresolvedIntents(ctx context.Context) ([]store.Intent, error)
	// LoadNotFullyAuditedIntents is the audit recovery-candidate scan: it also
	// returns resolved-but-unfinalized crash orphans that have left the unresolved
	// set (ADR-0006 point 4).
	LoadNotFullyAuditedIntents(ctx context.Context) ([]store.Intent, error)
	// ResolveIntent terminally closes an intent. A conflicting re-resolution comes
	// back as store.ErrResolutionConflict, which this package escalates rather
	// than swallows (#28).
	ResolveIntent(ctx context.Context, intentID, resolution string) error
	// UnackedLifecycleRecords is #20's deterministic reconstruction: the lifecycle
	// records that still need a durable audit ack.
	UnackedLifecycleRecords(ctx context.Context, intentID string) ([]store.LifecycleRecord, error)
	// RecordAuditAck records one lifecycle record's durable ack.
	RecordAuditAck(ctx context.Context, intentID, recordKey string) error
	// FinalizeFullyAudited sets the prune gate once every lifecycle record is
	// acked. It requires the intent to be resolved, which is what keeps an
	// unresolved ambiguous intent structurally un-prunable (ADR-0014 Decision 8).
	FinalizeFullyAudited(ctx context.Context, intentID string) (bool, error)
}

// Guard is the kill-switch seam. The reconciler only ever reports evidence and
// trips; it never clears a global halt (human-only, ADR-0004 point 6) and never
// reads a halt carrier to gate its own work (ADR-0014 Decision 8).
// *killswitch.Switch satisfies it.
type Guard interface {
	// Trip blocks a symbol (memory-only floor) or trips the durable global halt.
	Trip(ctx context.Context, scope killswitch.Scope, symbol, reason string, occurredAt time.Time) error
	// ClearSymbol releases a per-symbol block. Because it is a boolean delete and
	// NOT a refcount, the caller must confirm the symbol has zero residual blocking
	// evidence first (ADR-0014 Decision 4).
	ClearSymbol(symbol string)
	// NotifyScanComplete opens the replay gate. Every re-derived block must be
	// injected before this call (ADR-0004 point 3).
	NotifyScanComplete()
	// ReportOrderFailure durably counts one order failure, tripping at threshold.
	// It must commit BEFORE the intent is resolved (count-before-resolve).
	ReportOrderFailure(ctx context.Context, reason string, occurredAt time.Time) error
	// ReportOrderSuccess resets the consecutive-failure counter. It is outside the
	// count-ordering contract, so the caller owns ordering (Decision 8 guard).
	ReportOrderSuccess(ctx context.Context) error
	// BootHalt is the in-memory, infallible conservative block used when the
	// reconciler cannot sustain itself (Decision 12 fail-closed promotion).
	BootHalt()
}

// OrderAPI is the truth lookup seam. It exposes ONLY GetOrder: the reconciler
// structurally cannot submit (ADR-0003 point 4). *order.Client satisfies it; the
// underlying toss client bounds its own retries per call (maxRetries=4,
// backoffCap=5s), so a lookup is bounded and this package adds no retry loop of
// its own — the ticker paces re-drives instead.
type OrderAPI interface {
	GetOrder(ctx context.Context, accountSeq int64, orderID string) (order.Order, error)
}

// AuditSink is the synchronous-durable audit seam (ADR-0006). A FailClosedError
// means the record is NOT durable and escalates to a global trip (ADR-0006
// point 6). *audit.Writer satisfies it.
type AuditSink interface {
	EmitOrderLifecycle(ctx context.Context, ev audit.OrderLifecycleEvent) (audit.Ack, error)
	EmitFill(ctx context.Context, ev audit.FillEvent) (audit.Ack, error)
	EmitError(ctx context.Context, ev audit.ErrorEvent) (audit.Ack, error)
}

// Terminal resolution strings written to the journal. aborted-before-submit is
// fixed by ADR-0003; the closed-order verdicts mirror the Toss OrderStatus that
// established them.
const (
	ResolutionAbortedBeforeSubmit = "aborted-before-submit"
	ResolutionFilled              = "filled"
	ResolutionCanceled            = "canceled"
	ResolutionRejected            = "rejected"
)

// maxConsecutiveCycleFailures is how many consecutive reconciliation cycles may
// fail structurally (panic, journal load failure) before the reconciler promotes
// itself to a fail-closed halt (ADR-0014 Decision 12). The bounded-ness of all
// three delayed-halt windows rests on this loop continuing to run, so a loop that
// cannot run must stop the bot from creating new exposure rather than quietly
// leaving those windows unbounded.
const maxConsecutiveCycleFailures = 3

// minPreparedAbandonWindow is the floor on how long a prepared-only intent must
// have sat untouched before the reconciler closes it as aborted-before-submit.
//
// This is a money-safety bound, not tidiness. On a LIVE cycle the submit path can
// be running concurrently and sitting between its prepared commit and its
// submit-attempted commit; its mandatory durable audit emit alone is allowed tens
// of seconds there (order's durable-persist budget is 30s per step). Closing that
// intent would terminally resolve an order that is about to be POSTed for real,
// leaving a live order outside the unresolved set and therefore outside all later
// reconciliation. The floor is set comfortably above that budget so a slow but
// perfectly healthy submitter can never be mistaken for an abandoned intent.
//
// It floors ONLY the prepared-only branch. The ambiguity branch keeps using the
// configured settle window unchanged, so an operator can still declare an
// ambiguous submit quickly (that direction is fail-closed — it blocks a symbol,
// it never closes an intent).
const minPreparedAbandonWindow = 60 * time.Second

// Config wires a Reconciler. Journal/Guard/API/Audit are required; the three
// duration/threshold knobs are validated fail-closed (a zero would silently mean
// "never escalates", which is the fail-open New must refuse — the twin of
// killswitch Config.validate, ADR-0014 twin-artifact note).
type Config struct {
	Journal    Journal
	Guard      Guard
	API        OrderAPI
	Audit      AuditSink
	AccountSeq int64

	// AmbiguousBacklogThreshold is the number of unresolved-ambiguous intents at
	// which the reconciler trips the GLOBAL halt. The comparison is inclusive
	// (backlog >= threshold), i.e. threshold N means "halt on the Nth" — matching
	// killswitch's own >= thresholds. Reading it as strict > would weaken the
	// configured threshold by one, which is a fail-open (ADR-0014 Decision 1.2).
	//
	// The measure is the CURRENT unresolved backlog, not a time-windowed rate:
	// an ambiguous submit's hazard (an unidentified live order) does not age, so a
	// rate would fall to zero while the hazard stands (ADR-0014 Decision 1.2).
	AmbiguousBacklogThreshold int

	// SettleWindow is how long a submit-attempted intent with no orderId may stay
	// in flight before it is declared unresolved-ambiguous. It absorbs the normal
	// gap between the submit-attempted commit and the acked commit.
	SettleWindow time.Duration

	// ReevalInterval is the supervised live re-evaluation cadence (ADR-0014
	// Decision 11). It also paces GetOrder re-drives.
	ReevalInterval time.Duration

	// Now defaults to time.Now. Injected in tests to drive settle-window and
	// escalation boundaries deterministically.
	Now func() time.Time

	// Ticks, when non-nil, replaces the internal time.Ticker so a test can step
	// the live cadence deterministically. Production leaves it nil.
	Ticks <-chan time.Time

	// Logger defaults to slog.Default(). Unattended operation makes the log the
	// only post-hoc diagnosis surface, so every swallowed-looking path logs.
	Logger *slog.Logger
}

// Reconciler is the single truth-establishing engine. Its own state is only the
// per-symbol blocks it has published and the last fill snapshot it has emitted;
// everything load-bearing lives in the journal, so a restart re-derives it.
type Reconciler struct {
	journal    Journal
	guard      Guard
	api        OrderAPI
	audit      AuditSink
	accountSeq int64

	backlogThreshold int
	settleWindow     time.Duration
	// preparedAbandonAfter is the settle window floored by
	// minPreparedAbandonWindow — see that constant for why the prepared-only
	// branch needs a stricter bound than the ambiguity branch.
	preparedAbandonAfter time.Duration
	reevalInterval       time.Duration

	now    func() time.Time
	ticks  <-chan time.Time
	logger *slog.Logger

	// wake is the non-blocking submit-path wake seam (#34 WakeFunc). Capacity 1
	// collapses a burst into one pending cycle.
	wake chan struct{}

	mu sync.Mutex
	// blocked is the set of symbols THIS reconciler has tripped. Auto-clear only
	// ever releases a symbol in this set, so it can never open a block some other
	// component established.
	blocked map[string]struct{}
	// lastFill is the last cumulative execution snapshot emitted per orderId, so a
	// re-drive of an unchanged open order does not append a duplicate fill record
	// every tick. It is memory-only: after a restart one duplicate re-emit is
	// merged by the audit idempotency key (at-least-once, ADR-0006 point 3).
	lastFill map[string]audit.FillSnapshot
	// scanComplete records that the restart scan has succeeded once and the replay
	// gate has been opened. Until then the loop keeps retrying the scan instead of
	// running ordinary live cycles, because the gate is opened by nothing else (see
	// cycle).
	scanComplete bool
	// consecutiveFailures counts structurally failed cycles for Decision 12.
	consecutiveFailures int
	// promoted latches the fail-closed promotion so it is logged/tripped once.
	promoted bool
}

// New validates cfg and builds a Reconciler. Validation is fail-closed: a nil
// dependency or a non-positive threshold/interval is rejected at construction
// rather than degrading into a silently non-escalating reconciler.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Journal == nil {
		return nil, fmt.Errorf("reconciler: New requires a Journal")
	}
	if cfg.Guard == nil {
		return nil, fmt.Errorf("reconciler: New requires a Guard (kill-switch)")
	}
	if cfg.API == nil {
		return nil, fmt.Errorf("reconciler: New requires an API")
	}
	if cfg.Audit == nil {
		return nil, fmt.Errorf("reconciler: New requires an Audit sink")
	}
	// Zero-guard, the twin of killswitch Config.validate (ADR-0014 twin-artifact):
	// a zero ambiguous backlog threshold would make the global escalation trip on
	// an empty backlog or (read the other way) never trip at all — either way the
	// configured escalation is not the one that runs. Refuse it.
	if cfg.AmbiguousBacklogThreshold <= 0 {
		return nil, fmt.Errorf("reconciler: AmbiguousBacklogThreshold must be > 0, got %d", cfg.AmbiguousBacklogThreshold)
	}
	// A zero settle window would declare every in-flight submit ambiguous the
	// instant its marker lands (blocking the symbol on every normal submit); a
	// negative one is nonsense. Both are configuration that does not express the
	// intended policy, so fail closed at construction.
	if cfg.SettleWindow <= 0 {
		return nil, fmt.Errorf("reconciler: SettleWindow must be > 0, got %s", cfg.SettleWindow)
	}
	// A zero reevaluation interval would spin the live loop (or panic in
	// time.NewTicker) and, with it, the bounded-ness of the delayed-halt windows.
	if cfg.ReevalInterval <= 0 {
		return nil, fmt.Errorf("reconciler: ReevalInterval must be > 0, got %s", cfg.ReevalInterval)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	preparedAbandonAfter := cfg.SettleWindow
	if preparedAbandonAfter < minPreparedAbandonWindow {
		preparedAbandonAfter = minPreparedAbandonWindow
	}

	return &Reconciler{
		journal:              cfg.Journal,
		guard:                cfg.Guard,
		api:                  cfg.API,
		audit:                cfg.Audit,
		accountSeq:           cfg.AccountSeq,
		backlogThreshold:     cfg.AmbiguousBacklogThreshold,
		settleWindow:         cfg.SettleWindow,
		preparedAbandonAfter: preparedAbandonAfter,
		reevalInterval:       cfg.ReevalInterval,
		now:                  now,
		ticks:                cfg.Ticks,
		logger:               logger,
		wake:                 make(chan struct{}, 1),
		blocked:              make(map[string]struct{}),
		lastFill:             make(map[string]audit.FillSnapshot),
	}, nil
}

// Wake is the submit path's wake seam (order.WakeFunc): it asks for one
// reconciliation cycle as soon as the loop is free. It is non-blocking and safe
// to call from any goroutine — a wake that arrives while a cycle is already
// pending is collapsed into it, never dropped in a way that loses evidence (the
// journal is the evidence; the ticker re-scans regardless).
func (r *Reconciler) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

package killswitch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Store counter names for the reconstruction-resistant escalation signals
// (ADR-0004 point 7) and the durability fences. Exported so operators and
// tests can inspect them.
const (
	// CounterTokenRefreshFailures counts consecutive token-refresh failures.
	CounterTokenRefreshFailures = "killswitch.token-refresh-failures"
	// CounterOrderFailures counts consecutive order failures.
	CounterOrderFailures = "killswitch.order-failures"
	// CounterLiveAuthorization is the durable provenance marker for the
	// initial live-trading authorization (ADR-0007). It is written (value 1)
	// in the same transaction as every successful ClearGlobalHalt — the
	// human clear IS the authorization event — and read at boot: a store
	// whose halt state is clear but that carries no authorization marker is
	// either a fresh deployment or a store whose provenance was lost, and
	// both must boot halted (ADR-0007 points 3/7).
	CounterLiveAuthorization = "killswitch.live-authorization"
	// CounterHaltEpoch is the durable monotonic count of global-halt
	// transitions. Every persisted global TripHalt bumps it in the SAME
	// transaction; ClearGlobalHalt snapshots it and, inside its own clear
	// transaction, only commits halt=0 if the durable epoch is unchanged —
	// the write-connection serialization makes the "no global trip raced"
	// check and the clear atomic (a conditional single transaction). This is
	// what closes the clear/trip crash window without a two-transaction
	// repair. Per-symbol trips do NOT bump it (they stay memory-only —
	// ADR-0004 point 4), and CanSubmit never reads it (hot path stays on the
	// mirror — ADR-0004 point 5).
	CounterHaltEpoch = "killswitch.halt-epoch"
)

// ReasonAwaitingInitialAuthorization is the halt reason a never-authorized
// (or provenance-lost) deployment boots with (ADR-0007 point 3). Only the
// explicit human ClearGlobalHalt starts the first live trading.
const ReasonAwaitingInitialAuthorization = "awaiting-initial-authorization"

// Default escalation parameters. They are deliberately conservative starting
// points; tune via Config (zero values fall back to these defaults).
const (
	// DefaultAmbiguousTripThreshold is how many symbol-scope trips within
	// DefaultAmbiguousWindow escalate to a global halt.
	DefaultAmbiguousTripThreshold = 3
	// DefaultAmbiguousWindow is the sliding window for symbol-trip frequency.
	DefaultAmbiguousWindow = 15 * time.Minute
	// DefaultOrderFailureThreshold is how many consecutive order failures
	// trip the global halt.
	DefaultOrderFailureThreshold = 5
	// DefaultTokenRefreshFailureThreshold is how many consecutive
	// token-refresh failures trip the global halt.
	DefaultTokenRefreshFailureThreshold = 3
)

// Config tunes escalation thresholds and injects the clock seam for tests.
// The zero value is usable: every field falls back to its default.
type Config struct {
	// AmbiguousTripThreshold: symbol-scope trips within AmbiguousWindow that
	// escalate to a global halt. <=0 means DefaultAmbiguousTripThreshold.
	AmbiguousTripThreshold int
	// AmbiguousWindow is the sliding window for symbol-trip frequency.
	// <=0 means DefaultAmbiguousWindow.
	AmbiguousWindow time.Duration
	// OrderFailureThreshold: consecutive order failures that trip the global
	// halt. <=0 means DefaultOrderFailureThreshold.
	OrderFailureThreshold int
	// TokenRefreshFailureThreshold: consecutive token-refresh failures that
	// trip the global halt. <=0 means DefaultTokenRefreshFailureThreshold.
	TokenRefreshFailureThreshold int
	// Now is the clock used for window pruning. nil means time.Now.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.AmbiguousTripThreshold <= 0 {
		c.AmbiguousTripThreshold = DefaultAmbiguousTripThreshold
	}
	if c.AmbiguousWindow <= 0 {
		c.AmbiguousWindow = DefaultAmbiguousWindow
	}
	if c.OrderFailureThreshold <= 0 {
		c.OrderFailureThreshold = DefaultOrderFailureThreshold
	}
	if c.TokenRefreshFailureThreshold <= 0 {
		c.TokenRefreshFailureThreshold = DefaultTokenRefreshFailureThreshold
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Notifier is the alerting seam (ADR-0004 point 8): it is called when the
// global halt trips. Concrete channels (Slack, Telegram, ...) are out of
// scope here. Implementations may block briefly but should not; a panicking
// notifier is contained and never kills the trip path.
type Notifier interface {
	HaltTripped(reason string, occurredAt time.Time)
}

// Scope selects what a Trip blocks: everything (Global) or one symbol.
type Scope struct {
	global bool
	symbol string
}

// Global returns the scope that halts all new-exposure submission.
func Global() Scope { return Scope{global: true} }

// Symbol returns the scope that blocks new-exposure submission for one
// symbol. Symbol-scope trips are memory-only: after a restart the reconciler
// re-derives them from the journal scan (ADR-0004 point 4).
func Symbol(symbol string) Scope { return Scope{symbol: symbol} }

// Decision is the answer to CanSubmit plus the generation token that
// Reconfirm uses to contain the check/submit TOCTOU window (ADR-0004
// point 1). The zero value is blocked and can never be reconfirmed.
type Decision struct {
	Allowed bool
	Reason  string

	symbol string
	gen    uint64
}

// Guard is the fail-closed kill switch. Create it with New, open the startup
// replay gate with MarkReplayComplete once the reconciler's unresolved-intent
// scan is done, and consult CanSubmit/Reconfirm on the submit edge.
//
// The store is the durable truth; Guard keeps a cheap in-process mirror for
// the hot read path (ADR-0004 point 5). All mirror mutations happen before
// the corresponding durable write, so a failed write leaves the guard
// blocking (fail-closed), never passing.
type Guard struct {
	st       store.Store
	notifier Notifier
	cfg      Config
	now      func() time.Time

	mu sync.RWMutex
	// gen increments on EVERY trip (global or per-symbol). An outstanding
	// Decision from before any trip fails Reconfirm — conservative TOCTOU
	// containment (ADR-0004 point 1).
	gen uint64
	// haltGen increments only on a GLOBAL-halt transition (a global trip or a
	// threshold escalation). It is the in-memory mirror of the durable
	// CounterHaltEpoch and is the fence ClearGlobalHalt checks: because only
	// global trips move it, a below-threshold per-symbol trip never makes a
	// clear abort or leaves the store and mirror inconsistent. A global trip
	// bumps haltGen (mirror) BEFORE its durable epoch commit, so
	// haltGen != durable epoch means "a global trip is in flight".
	haltGen  uint64
	gateOpen bool
	halted   bool
	// haltReason is the mirror's halt reason (first trip wins in-process).
	haltReason string
	// recoveryFailed marks that boot could not load halt/epoch/counter state.
	// The mirror is halted (fail-closed) but its haltGen may not match the
	// durable epoch, so ClearGlobalHalt takes a resync path.
	recoveryFailed bool
	symbolBlocks   map[string]string
	// symbolTrips holds occurredAt of recent symbol-scope trips for the
	// ambiguous-frequency escalation window. Memory-only: the reconciler
	// re-injects trips (with original occurredAt) on restart, so stale
	// entries age out naturally instead of re-escalating.
	symbolTrips []time.Time
	orderFail   failureCounter
	tokenFail   failureCounter
}

// failureCounter is the in-process mirror of one reconstruction-resistant
// escalation counter. epoch bumps on every accepted mutation so a durable
// reset can detect that a failure raced it (failures win over resets — a
// raced reset is abandoned rather than erasing streak progress).
type failureCounter struct {
	count int64
	epoch uint64
}

// failureCounterRef resolves the mirror for a persisted counter name.
// Callers hold g.mu; the returned pointer is a stable field address.
func (g *Guard) failureCounterRef(name string) *failureCounter {
	if name == CounterOrderFailures {
		return &g.orderFail
	}
	return &g.tokenFail
}

// New loads persisted state and returns a usable Guard. It never fails open:
// if the halt state, the halt epoch, or a persisted counter cannot be loaded,
// the returned guard starts halted with a boot-recovery reason (ADR-0004
// point 3) and only ClearGlobalHalt — which re-reads and resyncs the store —
// can resume it. A store that was never explicitly authorized for live
// trading (fresh deployment, or provenance lost) boots halted with
// ReasonAwaitingInitialAuthorization (ADR-0007 points 3/7); the first
// successful ClearGlobalHalt records the authorization durably.
//
// The startup replay gate starts closed; call MarkReplayComplete after the
// reconciler finished re-deriving per-symbol blocks from the journal.
func New(ctx context.Context, st store.Store, notifier Notifier, cfg Config) *Guard {
	g := &Guard{
		st:           st,
		notifier:     notifier,
		cfg:          cfg.withDefaults(),
		gen:          1, // start above zero so a zero-value Decision never matches
		symbolBlocks: make(map[string]string),
	}
	g.now = g.cfg.Now

	type pending struct {
		reason string
		at     time.Time
	}
	var notifications []pending
	var awaitingPlan tripPlan
	var awaiting bool

	g.mu.Lock()
	// Initialise the in-memory halt-epoch mirror from durable truth first, so
	// a booted-halted guard's haltGen matches the durable epoch and clears
	// fence correctly.
	epoch, eerr := st.Counter(ctx, CounterHaltEpoch)
	if eerr != nil {
		g.recoveryFailed = true
		g.haltMirrorFailClosedLocked(fmt.Sprintf("boot recovery failed: counter %s load: %v", CounterHaltEpoch, eerr))
		notifications = append(notifications, pending{g.haltReason, g.now()})
	} else {
		g.haltGen = uint64(epoch.Value)
	}

	if !g.recoveryFailed {
		halt, err := st.Halt(ctx)
		switch {
		case err != nil:
			g.recoveryFailed = true
			g.haltMirrorFailClosedLocked(fmt.Sprintf("boot recovery failed: halt state load: %v", err))
			notifications = append(notifications, pending{g.haltReason, g.now()})
		case halt.Halted:
			// Booting into a previously persisted halt is not a new transition:
			// it was notified when it tripped. haltGen already equals the
			// durable epoch (set above), so a clear fences correctly.
			g.halted = true
			g.haltReason = halt.Reason
		default:
			// The halt state reads clear — but has live trading ever been
			// explicitly authorized on this store? A fresh deployment and a
			// store whose provenance was lost are both indistinguishable from
			// "never authorized", and both must boot halted until the human
			// clear (ADR-0007 points 3/7: absence of evidence is not safety).
			auth, aerr := st.Counter(ctx, CounterLiveAuthorization)
			switch {
			case aerr != nil:
				g.recoveryFailed = true
				g.haltMirrorFailClosedLocked(fmt.Sprintf(
					"boot recovery failed: counter %s load: %v", CounterLiveAuthorization, aerr))
				notifications = append(notifications, pending{g.haltReason, g.now()})
			case auth.Value == 0:
				awaitingPlan = g.haltLocked(ReasonAwaitingInitialAuthorization)
				awaiting = true
				notifications = append(notifications, pending{awaitingPlan.notifyReason, g.now()})
			}
		}
	}

	for _, c := range []struct {
		name string
		dst  *failureCounter
	}{
		{CounterTokenRefreshFailures, &g.tokenFail},
		{CounterOrderFailures, &g.orderFail},
	} {
		rec, err := st.Counter(ctx, c.name)
		if err != nil {
			// Escalation progress could not be recovered: treat as halted,
			// not as "no evidence" (ADR-0004 point 7).
			g.recoveryFailed = true
			g.haltMirrorFailClosedLocked(fmt.Sprintf("boot recovery failed: counter %s load: %v", c.name, err))
			notifications = append(notifications, pending{g.haltReason, g.now()})
			continue
		}
		c.dst.count = rec.Value
	}
	g.mu.Unlock()

	if awaiting {
		// Persist the awaiting-initial-authorization halt AND its epoch so a
		// reboot before the first clear boots straight into it (ADR-0007
		// point 3) and the mirror haltGen matches the durable epoch. If the
		// write fails the mirror is already halted (fail-closed); mark
		// recoveryFailed so a later clear resyncs the epoch it could not
		// persist.
		if err := st.Atomically(ctx, func(tx store.Tx) error {
			if err := tx.TripHalt(ctx, awaitingPlan.persistReason); err != nil {
				return err
			}
			return bumpHaltEpoch(ctx, tx, awaitingPlan.haltGen)
		}); err != nil {
			g.mu.Lock()
			g.recoveryFailed = true
			g.mu.Unlock()
		}
	}

	for _, n := range notifications {
		g.notify(n.reason, n.at)
	}
	return g
}

// bumpHaltEpoch raises the durable halt epoch to target if it is behind,
// inside the caller's transaction. It is monotonic (never lowers) so
// concurrent global trips whose transactions commit out of order still leave
// the durable epoch at the highest haltGen.
func bumpHaltEpoch(ctx context.Context, tx store.Tx, target uint64) error {
	cur, err := tx.Counter(ctx, CounterHaltEpoch)
	if err != nil {
		return err
	}
	if int64(target) > cur.Value {
		return tx.SetCounter(ctx, store.Counter{Name: CounterHaltEpoch, Value: int64(target)})
	}
	return nil
}

// CanSubmit is the synchronous fail-closed predicate on the new-exposure
// submit edge (ADR-0004 point 1). It reads only the in-process mirror (hot
// path) and blocks on: global halt, per-symbol block, closed startup replay
// gate, or unrecovered boot state. The returned Decision must be passed to
// Reconfirm immediately before the irreversible submit.
func (g *Guard) CanSubmit(symbol string) Decision {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.evaluateLocked(symbol)
}

func (g *Guard) evaluateLocked(symbol string) Decision {
	if g.halted {
		return Decision{Reason: "global halt: " + g.haltReason, symbol: symbol}
	}
	if !g.gateOpen {
		return Decision{
			Reason: "startup replay gate closed: unresolved-intent scan not complete",
			symbol: symbol,
		}
	}
	if reason, ok := g.symbolBlocks[symbol]; ok {
		return Decision{Reason: fmt.Sprintf("symbol %s blocked: %s", symbol, reason), symbol: symbol}
	}
	return Decision{Allowed: true, symbol: symbol, gen: g.gen}
}

// Reconfirm is the final fail-closed recheck of the submit critical section
// (ADR-0004 point 1): call it after CanSubmit passed and immediately before
// appending the submit-attempted marker. It blocks if anything currently
// blocks, or if ANY trip happened since the decision was issued (generation
// mismatch) — deliberately conservative: even a trip on an unrelated symbol,
// or a trip that was already cleared again, invalidates outstanding
// decisions. Trips are rare; an aborted intent is cleanly closed by the
// reconciler as aborted-before-submit.
func (g *Guard) Reconfirm(d Decision) Decision {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !d.Allowed {
		return Decision{Reason: "reconfirm: initial decision was not allowed", symbol: d.symbol}
	}
	cur := g.evaluateLocked(d.symbol)
	if !cur.Allowed {
		return cur
	}
	if cur.gen != d.gen {
		return Decision{
			Reason: "reconfirm: kill-switch state changed since the initial check",
			symbol: d.symbol,
		}
	}
	return cur
}

// MarkReplayComplete opens the startup replay gate. The reconciler calls it
// once the unresolved-intent scan finished and per-symbol blocks are
// re-derived (ADR-0004 point 3). Idempotent.
func (g *Guard) MarkReplayComplete() {
	g.mu.Lock()
	g.gateOpen = true
	g.mu.Unlock()
}

// Halted reports the mirror's global-halt state and reason (observability;
// per-symbol blocks and the replay gate are not reflected here).
func (g *Guard) Halted() (bool, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.halted, g.haltReason
}

// tripPlan is what a locked mirror mutation asks the unlocked caller to do.
type tripPlan struct {
	// persistReason non-empty => TripHalt(persistReason) must be written.
	persistReason string
	// notifyReason non-empty => the notifier fires (new halted transition).
	notifyReason string
	// haltGen is the global-halt generation this plan must persist as the
	// durable epoch (in the SAME transaction as the TripHalt).
	haltGen uint64
}

// haltLocked transitions the mirror toward a global halt and returns the
// persistence/notification plan. It bumps gen (so outstanding Decisions fail
// Reconfirm — a threshold escalation is a trip too) and haltGen (the
// durable-epoch mirror) so that (a) a clear in flight aborts and (b) the
// durable epoch advances in lockstep. The reason follows first-wins: a
// coalescing trip keeps the existing reason but still advances haltGen/epoch
// (a fresh global danger during a clear must invalidate that clear). Callers
// hold g.mu.
func (g *Guard) haltLocked(reason string) tripPlan {
	g.gen++
	g.haltGen++
	if !g.halted {
		g.halted = true
		g.haltReason = reason
		return tripPlan{persistReason: reason, notifyReason: reason, haltGen: g.haltGen}
	}
	return tripPlan{persistReason: g.haltReason, haltGen: g.haltGen}
}

// haltMirrorFailClosedLocked halts the mirror WITHOUT advancing haltGen — for
// boot-recovery failures where nothing durable backs the halt. Keeping
// haltGen == durable epoch lets a later ClearGlobalHalt fence correctly; the
// recoveryFailed flag routes that clear through a resync. CanSubmit blocks on
// g.halted regardless. Callers hold g.mu.
func (g *Guard) haltMirrorFailClosedLocked(reason string) {
	if !g.halted {
		g.halted = true
		g.haltReason = reason
	}
}

// applyTrip applies the in-memory effects of a trip and returns the
// persistence/notification plan. Every trip bumps gen (Reconfirm TOCTOU —
// via haltLocked for global/escalating, or directly for a per-symbol block);
// only a global-scope trip or a threshold escalation bumps haltGen.
func (g *Guard) applyTrip(scope Scope, reason string, occurredAt time.Time) tripPlan {
	g.mu.Lock()
	defer g.mu.Unlock()
	if scope.global {
		return g.haltLocked(reason)
	}

	// Per-symbol: memory only (re-derived from the journal on restart,
	// ADR-0004 point 4). First reason wins while the block is active.
	if _, ok := g.symbolBlocks[scope.symbol]; !ok {
		g.symbolBlocks[scope.symbol] = reason
	}
	g.symbolTrips = append(g.symbolTrips, occurredAt)
	g.pruneTripsLocked()
	if len(g.symbolTrips) >= g.cfg.AmbiguousTripThreshold {
		return g.haltLocked(fmt.Sprintf(
			"symbol-trip frequency: %d trips within %s (threshold %d), last: %s",
			len(g.symbolTrips), g.cfg.AmbiguousWindow, g.cfg.AmbiguousTripThreshold, reason,
		))
	}
	// A non-escalating per-symbol trip still invalidates outstanding Decisions.
	g.gen++
	return tripPlan{}
}

func (g *Guard) pruneTripsLocked() {
	cutoff := g.now().Add(-g.cfg.AmbiguousWindow)
	kept := g.symbolTrips[:0]
	for _, at := range g.symbolTrips {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	g.symbolTrips = kept
}

// Trip records a kill-switch trip. reason is free-form (the trigger API stays
// generic — ADR-0004 point 7); occurredAt is when the underlying event
// happened, which matters for re-injected trips during the restart scan.
//
// Global scope: mirror halt, then durable TripHalt + halt-epoch bump in one
// transaction, then notifier. Symbol scope: memory-only block plus the
// frequency window; if the window crosses the threshold the trip escalates to
// a persisted global halt. On a persist failure the mirror stays halted and
// the error is returned.
func (g *Guard) Trip(ctx context.Context, scope Scope, reason string, occurredAt time.Time) error {
	plan := g.applyTrip(scope, reason, occurredAt)
	var persistErr error
	if plan.persistReason != "" {
		persistErr = g.st.Atomically(ctx, func(tx store.Tx) error {
			if err := tx.TripHalt(ctx, plan.persistReason); err != nil {
				return err
			}
			return bumpHaltEpoch(ctx, tx, plan.haltGen)
		})
	}
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
	}
	return persistErr
}

// TripTx is Trip participating in the caller's transaction (ADR-0005
// point 3): the caller owns the atomic coupling of a journal write and the
// halt trip, e.g. "record ambiguous marker AND trip halt" in one commit. The
// halt-epoch bump joins the same transaction.
//
// The mirror updates immediately: if the caller's transaction later rolls
// back, the durable halt is gone but the in-process guard keeps blocking —
// the divergence falls on the safe side (fail-closed). The notifier also
// fires immediately, which is accurate for the in-process state.
func (g *Guard) TripTx(ctx context.Context, tx store.Tx, scope Scope, reason string, occurredAt time.Time) error {
	plan := g.applyTrip(scope, reason, occurredAt)
	var persistErr error
	if plan.persistReason != "" {
		if persistErr = tx.TripHalt(ctx, plan.persistReason); persistErr == nil {
			persistErr = bumpHaltEpoch(ctx, tx, plan.haltGen)
		}
	}
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
	}
	return persistErr
}

// ReportOrderFailure records one order failure toward the consecutive-failure
// escalation (counting lives here and only here — ADR-0004 point 7). The
// counter is reconstruction-resistant, so it persists in the same transaction
// as any halt it triggers; a persist failure fails closed (mirror halt).
func (g *Guard) ReportOrderFailure(ctx context.Context, occurredAt time.Time) error {
	return g.reportFailure(ctx, nil, CounterOrderFailures, occurredAt)
}

// ReportOrderFailureTx is ReportOrderFailure participating in the caller's
// transaction, for when the failure report is one logical event with a
// journal write (ADR-0005 point 3).
func (g *Guard) ReportOrderFailureTx(ctx context.Context, tx store.Tx, occurredAt time.Time) error {
	return g.reportFailure(ctx, tx, CounterOrderFailures, occurredAt)
}

// ReportOrderSuccess resets the consecutive order-failure streak. It never
// clears a halt (no auto-resume — ADR-0004 point 6). The reset is durable
// before it is visible: if the durable write fails or a failure races the
// reset, the streak is kept on both sides and an error is returned — always
// the conservative direction.
func (g *Guard) ReportOrderSuccess(ctx context.Context) error {
	return g.resetFailures(ctx, CounterOrderFailures)
}

// ReportTokenRefreshFailure records one token-refresh failure toward the
// escalation threshold. Same persistence contract as ReportOrderFailure.
func (g *Guard) ReportTokenRefreshFailure(ctx context.Context, occurredAt time.Time) error {
	return g.reportFailure(ctx, nil, CounterTokenRefreshFailures, occurredAt)
}

// ReportTokenRefreshSuccess resets the token-refresh failure streak. It never
// clears a halt (ADR-0004 point 6).
func (g *Guard) ReportTokenRefreshSuccess(ctx context.Context) error {
	return g.resetFailures(ctx, CounterTokenRefreshFailures)
}

func (g *Guard) reportFailure(ctx context.Context, tx store.Tx, name string, occurredAt time.Time) error {
	g.mu.Lock()
	var n int64
	var plan tripPlan
	switch name {
	case CounterOrderFailures:
		c := g.failureCounterRef(name)
		c.count++
		c.epoch++
		n = c.count
		if n >= int64(g.cfg.OrderFailureThreshold) {
			plan = g.haltLocked(fmt.Sprintf(
				"consecutive order failures: %d (threshold %d)", n, g.cfg.OrderFailureThreshold))
		}
	case CounterTokenRefreshFailures:
		c := g.failureCounterRef(name)
		c.count++
		c.epoch++
		n = c.count
		if n >= int64(g.cfg.TokenRefreshFailureThreshold) {
			plan = g.haltLocked(fmt.Sprintf(
				"consecutive token refresh failures: %d (threshold %d)", n, g.cfg.TokenRefreshFailureThreshold))
		}
	default:
		g.mu.Unlock()
		return fmt.Errorf("killswitch: unknown failure counter %q", name)
	}
	g.mu.Unlock()

	// Counter write and any halt trip are one logical event: one transaction
	// (ADR-0005 point 3). The monotonic guard keeps a racing older increment
	// from overwriting a newer persisted value; a threshold escalation also
	// bumps the durable halt epoch in the same transaction.
	write := func(tx store.Tx) error {
		cur, err := tx.Counter(ctx, name)
		if err != nil {
			return err
		}
		if n > cur.Value {
			if err := tx.SetCounter(ctx, store.Counter{Name: name, Value: n, UpdatedAt: occurredAt}); err != nil {
				return err
			}
		}
		if plan.persistReason != "" {
			if err := tx.TripHalt(ctx, plan.persistReason); err != nil {
				return err
			}
			return bumpHaltEpoch(ctx, tx, plan.haltGen)
		}
		return nil
	}
	var err error
	if tx != nil {
		err = write(tx)
	} else {
		err = g.st.Atomically(ctx, write)
	}
	if err != nil {
		// The reconstruction-resistant signal could not be persisted: a
		// restart would silently lose escalation progress, so fail closed
		// now (mirror halt). If the threshold trip above already halted the
		// mirror, just surface its (unfired) notification.
		if plan.notifyReason != "" {
			g.notify(plan.notifyReason, occurredAt)
		} else {
			g.forcePersistFailureHalt(err, occurredAt)
		}
		return err
	}
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
	}
	return nil
}

// errResetRaced reports that a failure was recorded while a counter reset
// was in flight: the reset is abandoned and the streak kept (failures win —
// erasing progress would delay escalation).
var errResetRaced = errors.New("killswitch: counter reset abandoned, a failure raced the reset — streak kept")

// resetFailures durably resets one failure streak, fail-safe in the
// conservative direction (codex adversarial on PR #57):
//
//   - The mirror is zeroed only AFTER the durable reset committed, so a
//     failed durable write never leaves the live guard undercounting.
//   - The durable write re-checks the counter epoch inside the transaction;
//     if a failure raced ahead, the reset aborts without touching the store.
//     If a failure lands after the commit instead, its own read-modify-write
//     persist runs behind the reset on the single write connection, sees the
//     committed zero, and re-persists the streak — so no interleaving leaves
//     the durable counter behind the mirror.
func (g *Guard) resetFailures(ctx context.Context, name string) error {
	g.mu.RLock()
	c := g.failureCounterRef(name)
	prev := c.count
	epochBefore := c.epoch
	g.mu.RUnlock()
	if prev == 0 {
		return nil
	}

	err := g.st.Atomically(ctx, func(tx store.Tx) error {
		g.mu.RLock()
		raced := g.failureCounterRef(name).epoch != epochBefore
		g.mu.RUnlock()
		if raced {
			return errResetRaced
		}
		return tx.SetCounter(ctx, store.Counter{Name: name, UpdatedAt: g.now()})
	})
	switch {
	case errors.Is(err, errResetRaced):
		return errResetRaced
	case err != nil:
		return fmt.Errorf("killswitch: durable counter reset failed, keeping streak: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	c = g.failureCounterRef(name)
	if c.epoch != epochBefore {
		// A failure raced between the commit and here; it re-persists its
		// count against the committed zero, so both sides keep the streak.
		return errResetRaced
	}
	c.count = 0
	c.epoch++
	return nil
}

// ClearSymbol removes one per-symbol block: the auto-clear path the
// reconciler uses once it resolves the ambiguity (ADR-0004 point 6). It does
// not erase the symbol-trip frequency window — resolving one ambiguity does
// not un-happen the frequency evidence.
func (g *Guard) ClearSymbol(symbol string) {
	g.mu.Lock()
	delete(g.symbolBlocks, symbol)
	g.mu.Unlock()
}

// errClearRaced reports that a GLOBAL trip raced ClearGlobalHalt. The clear
// did not release the halt (it aborted before or without committing halt=0,
// or a global trip re-persisted the halt) and the mirror stays halted, so
// this is the SAFE outcome; the operator reviews and retries.
var errClearRaced = errors.New("killswitch: clear aborted, a global trip raced the clear — review and retry")

// ClearGlobalHalt is the ONLY path that releases the global halt (explicit
// human reset — ADR-0004 point 6). It is a fail-closed conditional
// single-transaction:
//
//   - A clear on an un-halted guard is a no-op.
//   - It snapshots the durable halt epoch E, then, INSIDE one store
//     transaction, re-reads the durable epoch and only commits ClearHalt
//     (plus the ADR-0007 authorization marker) if it is still E and the
//     in-memory haltGen still equals E. The store's single write connection
//     serializes that transaction against every global TripHalt, so "no
//     global trip raced" and "halt=0" commit atomically — there is NO second
//     transaction and therefore NO clear/repersist crash window (the defect
//     the orchestrator's panel found in the two-transaction repair).
//   - A per-symbol trip never bumps haltGen or the durable epoch, so it never
//     makes a clear abort and never leaves store and mirror inconsistent —
//     the global halt it does not affect is released cleanly and the symbol
//     block simply remains in the mirror.
//   - If a global trip applies to the mirror AFTER the in-tx fence but before
//     the mirror flip, the clear keeps the mirror halted and returns
//     errClearRaced. No self-repersist is needed: only a global trip bumps
//     haltGen, and a global trip ALWAYS persists halt=1 + a higher epoch
//     itself, so the store converges to halted.
//
// Known residual (booked): the clear's halt=0 commit and a concurrently
// racing global trip's halt=1 commit are two independent events. If the clear
// legitimately committed halt=0 (no global trip had applied at the in-tx
// fence) and a global trip arrives in the same instant and the process
// crashes strictly between the two commits, the store is momentarily halt=0.
// This is NOT the eliminated two-tx repair window and NOT the symbol-trip
// defect: it requires a real global danger to coincide to the instruction
// with an operator clear plus a crash in the sub-microsecond gap, and the
// condition that raised the global trip re-manifests on restart (re-halt).
// Making even this atomic would require the store to conditionally commit on
// an in-memory value, which store's API (rightly) does not expose.
//
// Escalation counters are NOT reset: if the root cause persists, the next
// failure report re-trips.
func (g *Guard) ClearGlobalHalt(ctx context.Context) error {
	g.mu.Lock()
	if !g.halted {
		g.mu.Unlock()
		return nil
	}
	needReload := g.recoveryFailed
	hgBefore := g.haltGen
	tokenEpochBefore := g.tokenFail.epoch
	orderEpochBefore := g.orderFail.epoch
	g.mu.Unlock()

	var tokenN, orderN int64
	if needReload {
		// The clear may only resume what it can read: if boot could not load
		// the halt/counter state, refuse until the store serves it again
		// (ADR-0004 point 3). The reload also resyncs the halt epoch below.
		if _, err := g.st.Halt(ctx); err != nil {
			return fmt.Errorf("killswitch: clear refused, halt state still unreadable: %w", err)
		}
		tc, err := g.st.Counter(ctx, CounterTokenRefreshFailures)
		if err != nil {
			return fmt.Errorf("killswitch: clear refused, counter recovery still failing: %w", err)
		}
		oc, err := g.st.Counter(ctx, CounterOrderFailures)
		if err != nil {
			return fmt.Errorf("killswitch: clear refused, counter recovery still failing: %w", err)
		}
		tokenN, orderN = tc.Value, oc.Value
	}

	epochRec, err := g.st.Counter(ctx, CounterHaltEpoch)
	if err != nil {
		return fmt.Errorf("killswitch: clear refused, halt epoch unreadable: %w", err)
	}
	E := epochRec.Value

	err = g.st.Atomically(ctx, func(tx store.Tx) error {
		// Durable fence: has a global halt trip committed since we snapshotted
		// E? Serialized on the single write connection against every TripHalt.
		cur, err := tx.Counter(ctx, CounterHaltEpoch)
		if err != nil {
			return err
		}
		if cur.Value != E {
			return errClearRaced
		}
		if !needReload {
			// In-memory fence: a global trip bumps haltGen BEFORE its durable
			// epoch commit, so haltGen != E means a global trip is in flight
			// even though its durable write has not landed yet. Taking g.mu
			// inside the write transaction is safe: no path holds g.mu while
			// waiting on the write connection.
			g.mu.RLock()
			hg := g.haltGen
			g.mu.RUnlock()
			if hg != uint64(E) {
				return errClearRaced
			}
		}
		if err := tx.ClearHalt(ctx); err != nil {
			return err
		}
		// Record the authorization in the same transaction (ADR-0007
		// point 4): "explicitly cleared at least once" is the durable
		// provenance that lets subsequent boots start unhalted. Every
		// successful clear is a human authorization, so this is idempotent.
		return tx.SetCounter(ctx, store.Counter{
			Name:      CounterLiveAuthorization,
			Value:     1,
			UpdatedAt: g.now(),
		})
	})
	switch {
	case errors.Is(err, errClearRaced):
		return errClearRaced
	case err != nil:
		return fmt.Errorf("killswitch: durable clear failed, staying halted: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.haltGen != hgBefore {
		// A global trip applied to the mirror after the in-tx fence. It
		// re-persists halt=1 + a higher epoch itself (only global trips move
		// haltGen, and they always persist), so keep the mirror halted; the
		// store converges to halted. No self-repersist here.
		return errClearRaced
	}
	if needReload {
		// Preserve any failure increment that raced the recovery reload
		// (round 4 P2): only assign the reloaded snapshot when the counter
		// epoch is unchanged. Resync the halt epoch mirror to the durable
		// value we just fenced on.
		if g.tokenFail.epoch == tokenEpochBefore {
			g.tokenFail = failureCounter{count: tokenN, epoch: g.tokenFail.epoch + 1}
		}
		if g.orderFail.epoch == orderEpochBefore {
			g.orderFail = failureCounter{count: orderN, epoch: g.orderFail.epoch + 1}
		}
		g.haltGen = uint64(E)
		g.recoveryFailed = false
	}
	g.halted = false
	g.haltReason = ""
	return nil
}

// forcePersistFailureHalt halts the mirror after the guard failed to persist
// its own state (fail-closed: cannot record safety state => cannot submit
// safely). It does NOT advance haltGen: the persist failed, so no durable
// epoch backs this halt, and keeping haltGen == durable epoch lets a later
// ClearGlobalHalt still fence correctly. The startup replay gate and the
// surfaced error cover the restart window.
func (g *Guard) forcePersistFailureHalt(cause error, occurredAt time.Time) {
	g.mu.Lock()
	var notifyReason string
	if !g.halted {
		g.gen++
		g.halted = true
		g.haltReason = fmt.Sprintf("kill-switch state persist failed: %v", cause)
		notifyReason = g.haltReason
	}
	g.mu.Unlock()
	if notifyReason != "" {
		g.notify(notifyReason, occurredAt)
	}
}

// notify fires the notifier seam behind a recover boundary: alerting must
// never kill the trip path (unattended constraint). Called outside g.mu so a
// slow notifier cannot stall the hot path.
func (g *Guard) notify(reason string, occurredAt time.Time) {
	if g.notifier == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	g.notifier.HaltTripped(reason, occurredAt)
}

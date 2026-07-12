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
// (ADR-0004 point 7). Exported so operators and tests can inspect them.
const (
	// CounterTokenRefreshFailures counts consecutive token-refresh failures.
	CounterTokenRefreshFailures = "killswitch.token-refresh-failures"
	// CounterOrderFailures counts consecutive order failures.
	CounterOrderFailures = "killswitch.order-failures"
)

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
	// gen increments on every trip. An outstanding Decision from before any
	// trip fails Reconfirm, even if the trip was cleared in between.
	gen      uint64
	gateOpen bool
	halted   bool
	// haltReason is the mirror's halt reason (first trip wins in-process).
	haltReason string
	// haltDurable records whether the current mirror halt is known to be
	// persisted. TripTx never sets it (the caller's tx may still roll back);
	// the only cost of a false negative is a redundant re-persist.
	haltDurable bool
	// recoveryFailed marks that boot could not load halt/counter state.
	// ClearGlobalHalt refuses to resume until it can reload that state.
	recoveryFailed bool
	symbolBlocks   map[string]string
	// symbolTrips holds occurredAt of recent symbol-scope trips for the
	// ambiguous-frequency escalation window. Memory-only: the reconciler
	// re-injects trips (with original occurredAt) on restart, so stale
	// entries age out naturally instead of re-escalating.
	symbolTrips   []time.Time
	orderFailures int64
	tokenFailures int64
}

// New loads persisted state and returns a usable Guard. It never fails open:
// if the halt state or a persisted counter cannot be loaded, the returned
// guard starts halted with a boot-recovery reason (ADR-0004 point 3) and only
// ClearGlobalHalt — which re-checks the store — can resume it.
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

	g.mu.Lock()
	halt, err := st.Halt(ctx)
	switch {
	case err != nil:
		g.recoveryFailed = true
		plan := g.haltLocked(fmt.Sprintf("boot recovery failed: halt state load: %v", err))
		if plan.notifyReason != "" {
			notifications = append(notifications, pending{plan.notifyReason, g.now()})
		}
	case halt.Halted:
		// Booting into a previously persisted halt is not a new transition:
		// it was notified when it tripped, so no re-notification here.
		g.halted = true
		g.haltReason = halt.Reason
		g.haltDurable = true
	}

	for _, c := range []struct {
		name string
		dst  *int64
	}{
		{CounterTokenRefreshFailures, &g.tokenFailures},
		{CounterOrderFailures, &g.orderFailures},
	} {
		rec, err := st.Counter(ctx, c.name)
		if err != nil {
			// Escalation progress could not be recovered: treat as halted,
			// not as "no evidence" (ADR-0004 point 7).
			g.recoveryFailed = true
			plan := g.haltLocked(fmt.Sprintf("boot recovery failed: counter %s load: %v", c.name, err))
			if plan.notifyReason != "" {
				notifications = append(notifications, pending{plan.notifyReason, g.now()})
			}
			continue
		}
		*c.dst = rec.Value
	}
	g.mu.Unlock()

	for _, n := range notifications {
		g.notify(n.reason, n.at)
	}
	return g
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
}

// haltLocked transitions the mirror to halted. Mirror first, durability
// second: even if the caller's durable write then fails, the in-process
// guard is already blocking (fail-closed). If already durably halted it
// keeps the first reason and plans nothing; if halted but not known durable
// it plans a re-persist.
func (g *Guard) haltLocked(reason string) tripPlan {
	if g.halted {
		if g.haltDurable {
			return tripPlan{}
		}
		return tripPlan{persistReason: g.haltReason}
	}
	g.halted = true
	g.haltReason = reason
	g.haltDurable = false
	g.gen++
	return tripPlan{persistReason: reason, notifyReason: reason}
}

// applyTrip applies the in-memory effects of a trip and returns the
// persistence/notification plan. Every trip bumps the generation, so all
// outstanding Decisions fail Reconfirm (conservative TOCTOU containment).
func (g *Guard) applyTrip(scope Scope, reason string, occurredAt time.Time) tripPlan {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gen++
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
// Global scope: mirror halt, durable TripHalt in its own transaction, then
// notifier. Symbol scope: memory-only block plus the frequency window; if the
// window crosses the threshold the trip escalates to a persisted global halt.
// On a persist failure the mirror stays halted and the error is returned.
func (g *Guard) Trip(ctx context.Context, scope Scope, reason string, occurredAt time.Time) error {
	plan := g.applyTrip(scope, reason, occurredAt)
	var persistErr error
	if plan.persistReason != "" {
		persistErr = g.st.Atomically(ctx, func(tx store.Tx) error {
			return tx.TripHalt(ctx, plan.persistReason)
		})
		if persistErr == nil {
			g.markHaltDurable()
		}
	}
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
	}
	return persistErr
}

// TripTx is Trip participating in the caller's transaction (ADR-0005
// point 3): the caller owns the atomic coupling of a journal write and the
// halt trip, e.g. "record ambiguous marker AND trip halt" in one commit.
//
// The mirror updates immediately: if the caller's transaction later rolls
// back, the durable halt is gone but the in-process guard keeps blocking —
// the divergence falls on the safe side (fail-closed). The notifier also
// fires immediately, which is accurate for the in-process state.
func (g *Guard) TripTx(ctx context.Context, tx store.Tx, scope Scope, reason string, occurredAt time.Time) error {
	plan := g.applyTrip(scope, reason, occurredAt)
	var persistErr error
	if plan.persistReason != "" {
		persistErr = tx.TripHalt(ctx, plan.persistReason)
		// haltDurable deliberately not set: commit is the caller's call.
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
// clears a halt (no auto-resume — ADR-0004 point 6). A failed durable reset
// is returned but not escalated: the stale persisted value errs on the
// conservative side.
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
		g.orderFailures++
		n = g.orderFailures
		if n >= int64(g.cfg.OrderFailureThreshold) {
			plan = g.haltLocked(fmt.Sprintf(
				"consecutive order failures: %d (threshold %d)", n, g.cfg.OrderFailureThreshold))
		}
	case CounterTokenRefreshFailures:
		g.tokenFailures++
		n = g.tokenFailures
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
	// from overwriting a newer persisted value.
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
			return tx.TripHalt(ctx, plan.persistReason)
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
	if tx == nil && plan.persistReason != "" {
		g.markHaltDurable()
	}
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
	}
	return nil
}

func (g *Guard) resetFailures(ctx context.Context, name string) error {
	g.mu.Lock()
	var prev int64
	switch name {
	case CounterOrderFailures:
		prev, g.orderFailures = g.orderFailures, 0
	case CounterTokenRefreshFailures:
		prev, g.tokenFailures = g.tokenFailures, 0
	}
	g.mu.Unlock()
	if prev == 0 {
		return nil
	}
	return g.st.SetCounter(ctx, store.Counter{Name: name, UpdatedAt: g.now()})
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

// errClearRaced reports that a trip landed while ClearGlobalHalt was in
// flight; the clear keeps the fresher halt and the operator must review and
// retry.
var errClearRaced = errors.New("killswitch: clear aborted, a trip raced the clear — review and retry")

// ClearGlobalHalt is the ONLY path that releases the global halt (explicit
// human reset — ADR-0004 point 6). It is fail-closed end to end:
//
//   - A clear on an un-halted guard is a no-op, so a spurious clear cannot
//     race a fresh trip's durable write.
//   - If boot recovery had failed it first reloads the persisted counters,
//     refusing to resume while the store still fails.
//   - The durable clear is conditional: it runs inside one store transaction
//     and re-checks the trip generation first. The store's single write
//     connection serializes that transaction against every TripHalt, so a
//     trip that already raced ahead aborts the clear before it touches the
//     store (no wiping a fresher persisted halt — codex P1 on PR #57).
//   - The durable flag is dropped up front, so a trip that lands after the
//     clear committed coalesces into the mirror halt WITH a re-persist plan:
//     its TripHalt queues behind the clear transaction and restores the
//     durable halt; the mirror flip below then aborts on the generation
//     check. Either ordering leaves store and mirror blocking.
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
	genBefore := g.gen
	// Force any racing trip to plan a re-persist of the halt it coalesces
	// into (haltLocked re-persists when the halt is not known durable).
	g.haltDurable = false
	g.mu.Unlock()

	var tokenN, orderN int64
	if needReload {
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

	err := g.st.Atomically(ctx, func(tx store.Tx) error {
		// Conditional clear: abort before writing if any trip invalidated
		// the state the operator observed. Taking g.mu inside the write
		// transaction is safe because no code path holds g.mu while waiting
		// on the write connection (mirror phases release the lock before
		// any store I/O).
		g.mu.RLock()
		genNow := g.gen
		g.mu.RUnlock()
		if genNow != genBefore {
			return errClearRaced
		}
		return tx.ClearHalt(ctx)
	})
	switch {
	case errors.Is(err, errClearRaced):
		return errClearRaced
	case err != nil:
		return fmt.Errorf("killswitch: durable clear failed, staying halted: %w", err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gen != genBefore {
		// A trip landed between the commit and here: keep the fresher halt.
		// Its re-persist (planned because haltDurable was dropped above)
		// restores the durable state right behind the cleared row.
		return errClearRaced
	}
	if needReload {
		g.tokenFailures, g.orderFailures = tokenN, orderN
		g.recoveryFailed = false
	}
	g.halted = false
	g.haltReason = ""
	g.haltDurable = false
	return nil
}

func (g *Guard) markHaltDurable() {
	g.mu.Lock()
	if g.halted {
		g.haltDurable = true
	}
	g.mu.Unlock()
}

// forcePersistFailureHalt trips the mirror after the guard failed to persist
// its own state (fail-closed: cannot record safety state => cannot submit
// safely). Not durable by definition; the startup replay gate and the
// surfaced error cover the restart window.
func (g *Guard) forcePersistFailureHalt(cause error, occurredAt time.Time) {
	g.mu.Lock()
	plan := g.haltLocked(fmt.Sprintf("kill-switch state persist failed: %v", cause))
	g.mu.Unlock()
	if plan.notifyReason != "" {
		g.notify(plan.notifyReason, occurredAt)
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

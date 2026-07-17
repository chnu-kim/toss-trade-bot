package killswitch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Durable counter names. Kept internal so the persistence layout is owned in one
// place; callers never name-address a counter.
const (
	counterOrderFailure = "killswitch.order_failure_streak"
	counterTokenRefresh = "killswitch.token_refresh_failures"

	reasonTokenRefresh = "token-refresh-failure"
)

// Config holds the escalation thresholds. All fields are required and validated
// by New; a zero threshold or window is rejected rather than silently defaulting
// to "never trips" (that would be a fail-open).
type Config struct {
	// OrderFailureThreshold is the consecutive order-failure count that trips the
	// global halt. Reset by ReportOrderSuccess (ADR-0012 point 4).
	OrderFailureThreshold int
	// TokenRefreshThreshold is the token-refresh-failure count within
	// TokenRefreshWindow that trips the global halt.
	TokenRefreshThreshold int
	// TokenRefreshWindow is the sliding window for TokenRefreshThreshold.
	TokenRefreshWindow time.Duration
}

func (c Config) validate() error {
	if c.OrderFailureThreshold <= 0 {
		return fmt.Errorf("killswitch: OrderFailureThreshold must be > 0, got %d", c.OrderFailureThreshold)
	}
	if c.TokenRefreshThreshold <= 0 {
		return fmt.Errorf("killswitch: TokenRefreshThreshold must be > 0, got %d", c.TokenRefreshThreshold)
	}
	if c.TokenRefreshWindow <= 0 {
		return fmt.Errorf("killswitch: TokenRefreshWindow must be > 0, got %s", c.TokenRefreshWindow)
	}
	return nil
}

// Switch is the fail-closed submit guard (ADR-0004/0012/0013). It is safe for
// concurrent use: the hot path reads a single mu snapshot, durable transitions
// serialize on haltMu.
type Switch struct {
	store    Store
	cfg      Config
	notifier Notifier

	// haltMu serializes durable transitions (I4). It is held across the slow
	// store writes; the hot path never touches it.
	haltMu sync.Mutex

	// mu guards the hot-path snapshot below (I5). Every field is read together
	// under one lock so CanSubmit can never observe a torn read. inflightTrips is
	// a plain int under mu, NOT an atomic — the consistent snapshot is the point.
	mu                 sync.Mutex
	durableHalt        store.HaltPhase // carrier 1: mirror of store HaltState.Phase
	unpersistedPending bool            // carrier 2: sticky latch (with haltReason)
	haltReason         string
	inflightTrips      int             // carrier 3: monotone in-flight trip block
	scanComplete       bool            // replay-gate: closed until NotifyScanComplete
	bootHalt           bool            // conservative boot/panic halt (#36), memory-only
	perSymbolBlocked   map[string]bool // memory-only per-symbol blocks (ADR-0004 point 4)
}

// Option configures a Switch at construction.
type Option func(*Switch)

// WithNotifier installs the halt-notification seam (ADR-0004 point 8).
func WithNotifier(n Notifier) Option {
	return func(k *Switch) { k.notifier = n }
}

// New builds a Switch and loads the persisted global halt (persistence-wins).
// The replay gate starts closed, so CanSubmit is false until NotifyScanComplete
// (ADR-0004 point 3). If the store halt load fails, the returned Switch is
// fail-closed (boot-halted, blocked until a manual ClearHalt) and the error is
// returned alongside it so the caller can log it — the guard is safe either way.
func New(ctx context.Context, st Store, cfg Config, opts ...Option) (*Switch, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	k := &Switch{
		store: st,
		cfg:   cfg,
		// Default to none explicitly: the zero value of HaltPhase is "" which is
		// != HaltNone, so it would read as a global halt in the predicate.
		durableHalt:      store.HaltNone,
		perSymbolBlocked: make(map[string]bool),
	}
	for _, o := range opts {
		o(k)
	}

	hs, err := st.Halt(ctx)
	if err != nil {
		// Store load failure is treated as blocked, not "no evidence"
		// (ADR-0004 point 3). Boot-halted until a human clear.
		k.bootHalt = true
		return k, fmt.Errorf("killswitch: load halt state (fail-closed, boot-halted): %w", err)
	}
	switch hs.Phase {
	case store.HaltPending, store.HaltHalted:
		// persistence-wins: an interrupted (pending) or completed (halted) durable
		// trip boots halted (ADR-0012 Decision 1(c)).
		k.durableHalt = hs.Phase
	default:
		k.durableHalt = store.HaltNone
	}
	return k, nil
}

// CanSubmit is the hot-path predicate on the new-exposure submission edge. It
// reports whether a new-exposure order for symbol may be submitted, and a reason
// when blocked. It reads one mu snapshot (I5) — no store round-trip.
func (k *Switch) CanSubmit(symbol string) (allowed bool, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.canSubmitLocked(symbol)
}

// canSubmitLocked is the disjoint-carrier predicate (ADR-0013). Caller holds mu.
func (k *Switch) canSubmitLocked(symbol string) (bool, string) {
	switch {
	case k.durableHalt != store.HaltNone:
		return false, "global-halt:" + string(k.durableHalt)
	case k.inflightTrips > 0:
		return false, "trip-in-flight"
	case k.unpersistedPending:
		return false, "unpersisted-pending-halt"
	case !k.scanComplete:
		return false, "replay-gate-closed"
	case k.bootHalt:
		return false, "boot-halt"
	case k.perSymbolBlocked[symbol]:
		return false, "symbol-blocked"
	default:
		return true, ""
	}
}

// Reservation is the token returned by Reserve. It carries only the symbol —
// there is NO generation field (ADR-0013): the counter carries no-clobber, and
// Reconfirm re-evaluates the level predicate.
type Reservation struct {
	symbol string
}

// Reserve opens the submit critical section for symbol. It captures nothing
// (level semantics) — the guarantee comes from Reconfirm re-reading the live
// predicate, not from a snapshot taken here.
func (k *Switch) Reserve(symbol string) Reservation {
	return Reservation{symbol: symbol}
}

// Reconfirm is the fail-closed final re-check immediately before the irreversible
// submit (ADR-0004 point 1). It re-evaluates the CanSubmit predicate under mu.
// A trip that landed after Reserve aborts here; a trip-then-clear inside the
// window is let through (the operator cleared — level semantics, ADR-0013).
func (k *Switch) Reconfirm(r Reservation) (allowed bool, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.canSubmitLocked(r.symbol)
}

// NotifyScanComplete opens the replay gate after the restart reconciler scan has
// re-derived per-symbol blocks (ADR-0004 point 3). It always records the scan as
// complete; a boot-halt still blocks CanSubmit through the predicate until a
// manual clear, so the gate effectively stays shut while boot-halted.
func (k *Switch) NotifyScanComplete() {
	k.mu.Lock()
	k.scanComplete = true
	k.mu.Unlock()
}

// BootHalt marks an in-memory conservative halt (#36 initial-authorization gate,
// ADR-0012 Decision 1(c)). No durable write; held until a manual ClearHalt. It
// is reused for the panic-span promotion of count-first order-failure (W-B).
func (k *Switch) BootHalt() {
	k.mu.Lock()
	k.bootHalt = true
	k.mu.Unlock()
}

// ClearSymbol auto-clears a per-symbol block once the reconciler resolves the
// ambiguity (ADR-0004 point 6). Per-symbol state is memory-only.
func (k *Switch) ClearSymbol(symbol string) {
	k.mu.Lock()
	delete(k.perSymbolBlocked, symbol)
	k.mu.Unlock()
}

// HasUnpersistedPendingHalt reports the in-memory-only halts a graceful shutdown
// must durably finalize (or refuse to write a clean sentinel over) — the sticky
// latch and bootHalt (ADR-0013). Omitting bootHalt would let a bootHalt-only run
// certify itself clean and reopen on restart, so both are included. It is false
// once the latch's halt is durable (durableHalt != none covers it via the store).
func (k *Switch) HasUnpersistedPendingHalt() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return (k.unpersistedPending && k.durableHalt == store.HaltNone) || k.bootHalt
}

// latch sets the sticky unpersisted-pending carrier (carrier 2). It is set for a
// non-reconstructable trip whose durable state was lost, and only ever cleared
// by FinalizePendingHalt or ClearHalt — never by a counter decrement (I2).
func (k *Switch) latch(reason string) {
	k.mu.Lock()
	if !k.unpersistedPending {
		k.unpersistedPending = true
		k.haltReason = reason
	}
	k.mu.Unlock()
}

// notify fires the halt-notification seam inside a recover boundary so a
// panicking notifier cannot break a trip. Callers invoke it after the block is
// published. The Notifier contract requires HaltTripped to be non-blocking.
func (k *Switch) notify(reason string) {
	if k.notifier == nil {
		return
	}
	defer func() { _ = recover() }()
	k.notifier.HaltTripped(reason)
}

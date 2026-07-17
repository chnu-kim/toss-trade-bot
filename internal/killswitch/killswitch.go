package killswitch

import (
	"context"
	"sync"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/store"
)

// Store is the durable substrate the guard owns writes into — the subset of
// store.Store the killswitch consumes (ADR-0005 point 2/3). *store.DB satisfies
// it. All durable halt/counter writes go through Atomically because the guard
// owns the commit boundary (ADR-0012 Decision 2); Halt is read once at
// construction to boot fail-closed from persisted state.
type Store interface {
	// Atomically runs fn in one write transaction, committing iff fn returns nil.
	Atomically(ctx context.Context, fn func(tx store.Tx) error) error
	// Halt reads the persisted global halt phase (none/pending/halted).
	Halt(ctx context.Context) (store.HaltState, error)
}

// Notifier is the seam the guard calls when the global halt trips. It is
// best-effort: the guard treats a panicking or blocking notifier as a fault it
// isolates (unattended-safety), so implementations should not block. Concrete
// channels (Slack, email, …) are out of scope for this package (ADR-0004 point 8).
type Notifier interface {
	HaltTripped(reason string, at time.Time)
}

// Scope selects which halt a Trip targets: the global persisted halt or a single
// per-symbol memory-only block (ADR-0004 point 4). Build one with Global or Symbol.
type Scope struct {
	global bool
	symbol string
}

// Global targets the persisted, restart-surviving global halt.
func Global() Scope { return Scope{global: true} }

// Symbol targets a memory-only block for one symbol (reconciler re-derives it on
// restart by re-Trip'ing from the journal scan — ADR-0004 point 4).
func Symbol(sym string) Scope { return Scope{symbol: sym} }

// Reservation is the opaque token Reserve returns and Reconfirm consumes to close
// the submit-critical-section TOCTOU window (ADR-0004 point 1). It captures the
// guard generation at reservation time; Reconfirm fails closed if the guard now
// blocks the symbol OR the generation advanced (a global trip happened).
type Reservation struct {
	symbol string
	gen    uint64
}

// Snapshot is a read-only view of the guard for observability/logging (the only
// post-hoc diagnosis surface in unattended operation) and for #36's boot
// decisions. It is a copy; mutating it does nothing.
type Snapshot struct {
	Halted         bool
	Pending        bool
	Reason         string
	GateOpen       bool
	BlockedSymbols int
}

// Config tunes the escalation thresholds. Zero fields take conservative defaults
// (withDefaults). #36 may override them at wiring time.
type Config struct {
	// OrderFailureThreshold is the consecutive order-submission failures that trip
	// the global halt. Reset by ReportOrderSuccess.
	OrderFailureThreshold int
	// TokenRefreshFailureThreshold is the token-refresh failures within
	// TokenRefreshWindow that trip the global halt.
	TokenRefreshFailureThreshold int
	// TokenRefreshWindow bounds the token-refresh failure count (ADR-0004 point 7
	// counter+window).
	TokenRefreshWindow time.Duration
	// AmbiguousThreshold is the number of per-symbol ambiguous trips within
	// AmbiguousWindow that escalate to a global halt (ADR-0004 point 7).
	AmbiguousThreshold int
	// AmbiguousWindow bounds the ambiguous-frequency count.
	AmbiguousWindow time.Duration
}

func (c Config) withDefaults() Config {
	if c.OrderFailureThreshold <= 0 {
		c.OrderFailureThreshold = 3
	}
	if c.TokenRefreshFailureThreshold <= 0 {
		c.TokenRefreshFailureThreshold = 3
	}
	if c.TokenRefreshWindow <= 0 {
		c.TokenRefreshWindow = time.Hour
	}
	if c.AmbiguousThreshold <= 0 {
		c.AmbiguousThreshold = 5
	}
	if c.AmbiguousWindow <= 0 {
		c.AmbiguousWindow = 10 * time.Minute
	}
	return c
}

// mirrorPhase is the in-process exposure of the global halt CanSubmit reads on
// the hot path. It is the value the mirror shows, which trails the durable phase
// only in the safe direction (durable-before-visible, ADR-0012 Decision 1).
type mirrorPhase int

const (
	phaseNone mirrorPhase = iota // no global halt
	// phasePending: a trip has begun and the mirror already blocks (fail-closed),
	// but the durable TripHalt has not been observed to commit. Blocks CanSubmit
	// exactly like halted; distinguished so the graceful-shutdown query can find
	// an in-memory pending the store does not yet reflect.
	phasePending
	phaseHalted // durable halt observed (or a conservative/boot halt)
)

// counter names (namespaced so they never collide with other store counters).
const (
	counterOrderFailures = "killswitch:order-consecutive-failures"
	counterTokenRefresh  = "killswitch:token-refresh-failures"
)

// human-facing block reasons (surfaced to callers/logs).
const (
	reasonStartupGate       = "startup replay gate closed"
	reasonStateUnknown      = "halt state unknown at boot (fail-closed)"
	reasonGlobalHalted      = "global halt in effect"
	reasonGlobalPending     = "global halt pending (durable write in flight)"
	reasonSymbolBlocked     = "symbol blocked"
	reasonOrderFailures     = "consecutive order-submission failures over threshold"
	reasonTokenRefresh      = "token refresh failing over threshold"
	reasonFrequentAmbiguous = "ambiguous order outcomes frequent over threshold"
	reasonGenerationChanged = "guard state changed since reservation (fail-closed)"
)

// Guard is the fail-closed submit guard. Its zero value is not usable; build one
// with New. All exported methods are safe for concurrent use.
type Guard struct {
	store    Store
	notifier Notifier
	cfg      Config

	mu             sync.Mutex
	mirrorPhase    mirrorPhase       // global halt as exposed by the mirror (hot path)
	haltReason     string            // reason backing the current global halt
	durablePhase   store.HaltPhase   // last phase the guard observed committed to store
	blockedSymbols map[string]string // memory-only per-symbol blocks → reason
	scanComplete   bool              // reconciler replay scan reported complete
	gen            uint64            // bumped on additive global-halt transitions (TOCTOU)
	ambiguous      []time.Time       // in-memory (non-persisted) ambiguous-trip timestamps
}

// New builds a guard and boots it fail-closed from persisted state (ADR-0004
// point 3, ADR-0012 Decision 1(c)):
//
//   - store read fails ⇒ the guard boots halted (state unknown) and notifies.
//   - durable phase is pending OR halted ⇒ boots halted (persistence-wins).
//   - durable phase is none ⇒ boots un-halted, but the replay gate is still
//     closed, so CanSubmit stays blocked until NotifyScanComplete.
//
// New never returns an error: the correct response to a store read failure is to
// boot halted, not to abort — the returned guard is always usable and fail-closed.
func New(ctx context.Context, st Store, notifier Notifier, cfg Config) *Guard {
	g := &Guard{
		store:          st,
		notifier:       notifier,
		cfg:            cfg.withDefaults(),
		blockedSymbols: make(map[string]string),
		mirrorPhase:    phaseNone,
		durablePhase:   store.HaltNone,
	}

	hs, err := st.Halt(ctx)
	if err != nil {
		// Cannot read the persisted halt: treat as halted (ADR-0004 point 3). The
		// store may simply be unreadable, so we do not know the durable phase —
		// leave durablePhase none; the graceful-shutdown query only fires for a
		// *pending* mirror, so a halted boot never falsely reports an unpersisted
		// pending.
		g.mirrorPhase = phaseHalted
		g.haltReason = reasonStateUnknown
		g.notify(reasonStateUnknown, time.Now())
		return g
	}

	switch hs.Phase {
	case store.HaltPending, store.HaltHalted:
		// persistence-wins: a durably-initiated (pending) or completed (halted)
		// trip both boot halted (ADR-0012 Decision 1(c)).
		g.mirrorPhase = phaseHalted
		g.haltReason = hs.Reason
		g.durablePhase = hs.Phase
	default:
		g.mirrorPhase = phaseNone
		g.durablePhase = store.HaltNone
	}
	return g
}

// Snapshot returns a read-only view of the guard (observability / #36 boot).
func (g *Guard) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Snapshot{
		Halted:         g.mirrorPhase == phaseHalted,
		Pending:        g.mirrorPhase == phasePending,
		Reason:         g.haltReason,
		GateOpen:       g.scanComplete && g.mirrorPhase == phaseNone,
		BlockedSymbols: len(g.blockedSymbols),
	}
}

// evaluateLocked is the fail-closed blocking predicate. Caller holds g.mu.
// Blocking order (union — any true blocks; order only picks the reason):
// global halt → per-symbol block → startup replay gate.
func (g *Guard) evaluateLocked(symbol string) (blocked bool, reason string) {
	switch g.mirrorPhase {
	case phaseHalted:
		if g.haltReason != "" {
			return true, g.haltReason
		}
		return true, reasonGlobalHalted
	case phasePending:
		return true, reasonGlobalPending
	}
	if r, ok := g.blockedSymbols[symbol]; ok {
		if r != "" {
			return true, reasonSymbolBlocked + ": " + r
		}
		return true, reasonSymbolBlocked
	}
	// gate = scanComplete && no global halt. Reaching here means no global halt,
	// so the gate is open iff the replay scan has completed.
	if !g.scanComplete {
		return true, reasonStartupGate
	}
	return false, ""
}

// notify calls the notifier under a recover boundary so a misbehaving notifier
// can never crash the guard (unattended-safety). A nil notifier is a no-op.
func (g *Guard) notify(reason string, at time.Time) {
	if g.notifier == nil {
		return
	}
	defer func() { _ = recover() }()
	g.notifier.HaltTripped(reason, at)
}

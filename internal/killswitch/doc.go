// Package killswitch is the fail-closed submit guard that sits on the single
// new-exposure submission edge (ADR-0004). It answers CanSubmit on the hot
// order path, persists the global halt durable-before-visible (ADR-0012), and
// owns the escalation counters that trip that halt.
//
// It is a leaf (ADR-0004 point 8): it imports internal/store and the standard
// library only, never order/strategy/reconciler, so nothing here can create a
// cycle. Callers (#34 order, #35 reconciler, #36 cmd/bot) depend on this
// package; it depends on none of them. It consumes the store's #60 halt seam
// (MarkHaltPending/TripHalt/ClearHalt/Halt/SetCounter/Counter/Atomically)
// through the narrow Store interface in seam.go and never modifies store.
//
// # Mirror consistency (ADR-0013 — three disjoint block-carriers)
//
// CanSubmit must never fail open. The mirror the hot path reads is kept
// consistent with the durable truth by three *disjoint* block-carriers, each
// with one owner and one role (the earlier single-phase mirror + generation
// reconciliation of PR #62 is discarded — there is NO generation here):
//
//  1. durableHalt ∈ {none, pending, halted} — the in-process mirror of the
//     store's HaltState.Phase. It rises to halted only after a TripHalt commit
//     succeeds (durable-before-visible) and falls to none only after a ClearHalt
//     commit succeeds (manual only). Owner of the fall: ClearHalt.
//  2. unpersistedPending — a sticky latch (bool + haltReason) for a trip whose
//     durable state was lost to a store error and cannot be re-derived on
//     restart (store down at the halt decision, or a non-reconstructable
//     counter increment). It is the "memory-only pending" the store read cannot
//     see. Owner of the fall: FinalizePendingHalt success or manual ClearHalt —
//     never a counter decrement.
//  3. inflightTrips — a monotone counter that carries an in-flight trip's block
//     disjointly: each trip-triggering path owns exactly one +1/-1. It is read
//     inside the same mu snapshot as the other two (NOT lock-free — a
//     consistent snapshot is what closes the torn read).
//
// The hot-path predicate reads all state under one mu snapshot, so a torn read
// (durableHalt falling while inflightTrips rises, seen at two instants) is
// structurally impossible:
//
//	CanSubmit(sym) is blocked iff
//	  durableHalt != none || inflightTrips > 0 || unpersistedPending
//	  || !scanComplete || bootHalt || perSymbolBlocked(sym)
//
// Reserve captures nothing (level semantics); Reconfirm re-evaluates this
// predicate under mu. There is no generation — the counter carries no-clobber,
// and a trip-then-clear inside the Reserve~Reconfirm window is correct to let
// through (the operator cleared).
//
// # Invariants (regression guards — see interleaving_test.go)
//
//   - I1 fail-closed immediacy: inflightTrips++ happens before any slow wait
//     (haltMu, store) so the hot path blocks the instant a trip starts.
//   - I2 no-clobber (structural): the three carriers are disjoint — durableHalt
//     is lowered only by ClearHalt, the latch only by Finalize/ClearHalt,
//     inflightTrips only by each owner's own ±1.
//   - I3 durable-before-visible: durableHalt=halted is published only after the
//     TripHalt commit succeeds.
//   - I4 transition order: durable transitions are serialized by haltMu.
//   - I5 single snapshot: the hot path reads mu-only, one consistent snapshot
//     (NOT lock-free — required to close the torn read).
//   - I6 non-halting non-interference: ReportOrderSuccess touches neither
//     haltMu, nor inflightTrips, nor the mirror — only its own counter tx.
//   - I7 publish-before-decrement: inflightTrips-- is each trip's final step,
//     after its block-carrier is published. It is a single deferred decrement
//     registered right after the increment; no early explicit decrement.
//
// # Fail-closed contract (ADR-0004 point 3)
//
// State unknown, store load failure, durable write failure, and the boot
// replay-gate window are all blocked. The global halt clears only by an
// explicit manual ClearHalt (ADR-0004 point 6) — there is no auto-resume path.
// Per-symbol blocks are memory-only (ADR-0004 point 4) and auto-clear.
package killswitch

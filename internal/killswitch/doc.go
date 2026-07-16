// Package killswitch is the fail-closed guard on the single edge where new
// exposure is created: order submission (ADR-0004). The order layer asks
// CanSubmit(symbol) before starting a submission and Reconfirm(decision)
// immediately before the irreversible POST; everything else (reconciler,
// token refresh, all queries) deliberately never consults this guard — truth
// finding must keep running while halted.
//
// Design contract (do not relax):
//
//   - Fail-closed everywhere. Unknown state, a failed halt/counter load at
//     boot, a failed durable write of the guard's own state, and the startup
//     replay window all read as "blocked" (ADR-0004 point 3). The guard never
//     fails open.
//   - Two scopes with asymmetric restart durability (ADR-0004 point 4):
//     the global halt is persisted through the store and the guard boots
//     halted after a restart; per-symbol blocks are memory-only and are
//     re-derived by the reconciler, which re-Trips them from the journal scan
//     with their original occurredAt. The startup replay gate (closed until
//     MarkReplayComplete) covers the window before that re-derivation.
//   - Store = durable truth, in-process mirror = cheap read (ADR-0004
//     point 5). CanSubmit is the hot path and reads only the mirror. Trips
//     update the mirror first and persist second, so even a failed durable
//     write leaves the guard blocking (the divergence falls on the safe side).
//   - TOCTOU containment (ADR-0004 point 1): CanSubmit returns a Decision
//     carrying a state generation. Reconfirm re-evaluates the predicate AND
//     rejects the decision if any trip happened since it was issued — even a
//     trip-then-clear cycle cannot slip a stale decision through. Any trip
//     invalidates all outstanding decisions (conservative by design; trips
//     are rare).
//   - Threshold escalation is counted here and only here (ADR-0004 point 7).
//     Signals that the journal can reconstruct (symbol-trip/ambiguous
//     frequency) live in memory and are rebuilt by the reconciler's re-Trips.
//     Signals the journal cannot reconstruct (consecutive order failures,
//     token-refresh failures) are persisted as store counters so a crash loop
//     cannot reset escalation progress; failing to recover them at boot is
//     treated as halted, not as "no evidence".
//   - No auto-resume for the global halt (ADR-0004 point 6): only the
//     explicit ClearGlobalHalt call resumes submission. Per-symbol blocks
//     auto-clear via ClearSymbol when the reconciler resolves the ambiguity.
//   - ClearGlobalHalt is a conditional single transaction: it commits halt=0
//     only if the durable halt epoch (CounterHaltEpoch, bumped by every
//     global TripHalt in the same transaction) and the in-memory haltGen are
//     both unchanged since the clear started. Because only a global trip
//     moves the epoch/haltGen, a per-symbol trip never aborts the clear and
//     never leaves store and mirror inconsistent, and there is no separate
//     halt=1 repair write (hence no clear/repersist crash window).
//     haltGen tracks global-halt transitions; the any-trip generation above
//     stays the Reconfirm token.
//   - Initial authorization is a halt, not a separate mechanism (ADR-0007):
//     a store that has never seen an explicit clear boots halted with
//     ReasonAwaitingInitialAuthorization, so deploying is never the event
//     that starts live trading — the human clear is. The authorization is
//     recorded durably in the same transaction as the clear; losing the
//     store (or its provenance) re-arms the initial halt.
//   - Callers own atomic coupling (ADR-0005 point 3): when a halt trip or an
//     order-failure report is part of one logical event with a journal write,
//     the caller opens store.Atomically and uses TripTx /
//     ReportOrderFailureTx inside it. The guard's mirror still updates even
//     if the caller's transaction later rolls back — again the safe side.
//   - Trip reasons are free-form strings. The trigger API stays generic so
//     new signal sources (for example a runtime supervisor escalating
//     repeated goroutine panics) can trip the switch without an API change.
//   - Leaf package: killswitch imports only the store seam and the standard
//     library. order/strategy/reconciler import killswitch, never the
//     reverse (ADR-0004 point 8).
//
// Out of scope here (wired by other issues): the callers that emit signals
// (order #34, reconciler #35, token manager #36), reducing-order validation
// (ADR-0004 point 2), and any concrete notifier channel — only the Notifier
// seam is defined.
package killswitch

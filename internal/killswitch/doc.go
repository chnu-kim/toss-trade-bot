// Package killswitch is the fail-closed submit guard for the unattended trading
// bot. It is the single synchronous predicate the order path (#34) asks before
// it begins any new-exposure order submission, and the single place that decides
// when to halt (ADR-0004, ADR-0012).
//
// # What it guarantees
//
//   - Fail-closed. CanSubmit blocks whenever the answer is not a confident yes:
//     a global halt (halted OR an in-flight pending trip), a per-symbol block,
//     the startup replay window (before the reconciler scan completes), or an
//     unknown halt state (store read failed at boot). fail-open never happens
//     (ADR-0004 point 3).
//   - Durable-before-visible. The in-process mirror that CanSubmit reads exposes
//     a completed halt only AFTER the durable write that backs it has committed;
//     a durable write that fails (SQLite lock, disk) keeps the mirror blocked
//     (pending/halted), it never reverts to unhalted. There is therefore no
//     crash window where the mirror and the store disagree in the unsafe
//     direction (ADR-0012 Decision 1). mirror-first (expose then persist) is
//     forbidden.
//   - Restart-safe. The global halt is a durable none→pending→halted→none
//     2-phase lifecycle. A boot that reads pending OR halted comes up halted
//     (persistence-wins); a boot that cannot read the halt at all comes up
//     halted (ADR-0012 Decision 1(c)).
//   - TOCTOU-closed. Between the cheap CanSubmit and the irreversible POST the
//     caller re-checks via Reserve/Reconfirm; a trip in that window (global or
//     the reserved symbol) aborts the submission fail-closed (ADR-0004 point 1).
//
// # Ownership boundary
//
// killswitch owns every durable halt write through its own store.Atomically
// (TripTx removed — ADR-0012 Decision 2): risk sources (order #34, reconciler
// #35, token manager #36) only *report* signals, they do not couple a halt into
// their own transaction. Consecutive order-failure counting is count-before-
// resolve (ADR-0012 Decision 3): ReportOrderFailure durably commits the
// counter++ (and the threshold TripHalt, in the same killswitch tx) BEFORE the
// caller resolves the intent, so a crash can only over-count (over-halt = safe),
// never permanently under-count.
//
// killswitch does NOT read the clean-shutdown sentinel: that judgment is #36's
// (cmd/bot). killswitch instead exposes the affordances #36 drives — a
// conservative boot-halt, a query for an in-memory pending halt the store does
// not yet reflect, and a finalize entry point to persist it at shutdown.
//
// # Layout
//
// Leaf package (ADR-0004 point 8): killswitch imports store and stdlib only. It
// must never import order/strategy/reconciler — those import killswitch. The
// notifier is a seam (interface) only; concrete channels are out of scope.
package killswitch

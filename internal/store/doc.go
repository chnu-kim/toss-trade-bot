// Package store is the durable substrate for the unattended trading bot: a
// single transactional persistence layer over an embedded, pure-Go SQLite
// database (ADR-0005). It holds the areas of live, actionable state in one
// store and one transaction boundary:
//
//   - the write-ahead order journal (outbox) with its 2-marker progression
//     prepared → submit-attempted → acked (ADR-0002),
//   - the global halt as a durable none→pending→halted→none lifecycle that
//     survives restarts (ADR-0004, ADR-0012 Decision 1(c)); pending records a
//     trip durably initiated but not completed so an unclean recovery can treat
//     it as halted,
//   - the clean-shutdown sentinel: a single-row lifecycle value (running|clean)
//     with no coexisting records, so a crash cannot leave a stale clean beside a
//     running (ADR-0012 Decision 1(c) sentinel fail-open #1),
//   - reconstruction-resistant persistent counters (e.g. token-refresh
//     failures) that a restart must not silently reset (ADR-0004 point 7).
//
// store is substrate only: it exposes the halt phase and sentinel value plus the
// atomic transitions, but the judgment logic — when pending becomes halted, when
// a clean is eligible to be written, when an unclean boot should come up
// conservatively halted — lives in the consumers (#32 killswitch / #36 cmd/bot),
// not here.
//
// Design contract (do not relax):
//
//   - Leaf package. store imports no domain-logic package (order, killswitch,
//     reconciler, strategy); those import store. No import cycles.
//   - Substrate, not behaviour. store owns the persistent DTOs
//     (Intent/Marker/HaltState/Counter) and the durable read/write methods. It
//     does NOT decide intent identity, when to halt, or what "terminal" means —
//     that behaviour stays in the domain (ADR-0005 point 2).
//   - Durability is fixed: synchronous=FULL + WAL, fsync-on-commit. Relaxing it
//     (synchronous=OFF/NORMAL, async flush) is forbidden — it would silently
//     void the 2-marker crash-safety (ADR-0005 point 4). TestDurabilityPragmas
//     guards this.
//   - Each 2-marker transition is its own durable commit; they are never
//     batched (ADR-0005 point 3). But a single logical event that touches the
//     journal AND halt/counter state must be one transaction — that is what
//     Atomically exists for.
//   - Single-writer serialization: all writes go through one dedicated write
//     connection so concurrent Atomically callers serialize instead of racing
//     into a spurious SQLITE_BUSY fail-closed (ADR-0005 follow-up).
//
// Out of scope for this package: retention/prune (issue #14, the ack-flag
// column — a later migration), the audit/observability sink (ADR-0005 point 5),
// disk-full → halt wiring (killswitch, ADR-0004), and the durable-before-visible
// judgment/wiring that consumes the halt phase and sentinel (#32/#36).
package store

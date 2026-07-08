// Package audit is the occurrence-time, fsync-durable audit sink for the
// unattended trading bot (ADR-0006). It owns the durable history of irreversible
// money actions — order lifecycle transitions, observed fills, and errors — on a
// local append-only file path it controls, so the CLAUDE.md observability
// invariant ("every order / fill / error persisted") holds across the whole
// stream even through a crash. For reconstruction-resistant errors and
// observation-bound fills this local durable stage is the single source of truth
// (ADR-0006 point 1): if the sink does not durably record them at occurrence
// time, a crash loses them permanently.
//
// Design contract (do not relax):
//
//   - Leaf package. audit imports no domain-logic package (order, reconciler,
//     killswitch, store, strategy, runtime); those import audit to emit events.
//     No import cycles (ADR-0006 point 2). It depends only on the standard
//     library (log/slog is optional and off the durability path).
//   - Consumer seam. order/reconciler/killswitch/token-manager depend on the
//     Sink interface and fake it in their own unit tests; the sink's own fsync /
//     torn-tail / rotation / restart-recovery behaviour is tested with a real
//     writer in a temp dir (ADR-0006 point 2).
//   - Segment-durability protocol (content fsync alone is NOT durable — the
//     POSIX directory-entry trap). A new or rotated segment is written to a temp
//     name, its content fsync'd, atomically renamed to its final name, and then
//     the parent directory is fsync'd so the directory entry itself is durable.
//     Without the directory fsync a freshly created/rotated segment can be lost
//     whole on crash (ADR-0006 point 4).
//   - Torn-tail discipline. Each record is a framed [len][crc32c][payload]. On
//     restart the tail is validated; an uncommitted (torn) trailing record is
//     discarded so the log is left consistent (ADR-0006 point 3). Corruption in
//     a sealed (non-tail) segment is treated as fail-closed, never silently
//     undercounted, because the sequence derives from the committed record count.
//   - Errors are synchronous-durable. EmitError returns success only after the
//     record is fully committed; a failure surfaces as FailClosedError — the
//     fail-closed signal. Residual risk (ADR-0006, explicitly accepted): a crash
//     mid durable-write loses that one record. Errors are not recovered by
//     re-emit.
//   - Write-time size bound. A record whose marshaled JSON payload exceeds
//     maxRecordSize (frame.go, shared with the read-time bound readFrame
//     enforces on recovery) is rejected at commit time, before it is framed or
//     written, with a *RecordTooLargeError — a per-record rejection, not a
//     durability failure. It does not poison the writer (IsFailClosed reports
//     false for it; normal-size records emitted right after still commit),
//     and it must never trip a future killswitch by itself (ADR-0006 point 6
//     — that predicate is IsFailClosed). Because errors are
//     reconstruction-resistant (see below), a rejected ErrorEvent is
//     permanently lost from the audit trail unless the caller truncates and
//     re-emits it (issue #25).
//   - Caller contract on free-form fields. ErrorEvent.Message and
//     OrderLifecycleEvent.Detail are opaque strings the sink stores verbatim
//     and never redacts. Callers MUST NOT put secrets, tokens, or raw
//     request/response bodies into them — the audit trail is a durable local
//     file (and eventually ships to a remote sink, ADR-0006 point 5), so
//     anything written there should be treated as retained, not ephemeral.
//     Callers should also keep these fields well under maxRecordSize (e.g. by
//     truncating an upstream API error body) so a single oversize occurrence
//     does not silently drop from the audit trail (issue #25).
//   - Idempotency keys are synthesized per class (see key.go): order lifecycle
//     reuses intentId/orderId (ADR-0002), fills version by a financial-field
//     digest, errors carry the durable append sequence. The sink is append-only
//     and does no write-time dedup; the key is the merge handle for at-least-once
//     idempotent consumption.
//   - Role separation from runtime.NewLogger (ADR-0006 point 2). The slog JSON
//     logger is best-effort operational diagnostics (stdout); this sink is the
//     durable money-action history. The sink may mirror to slog for visibility,
//     but durability never depends on it.
//
// Out of scope for this issue (deliberately): the ack↔store prune flag wiring,
// prune itself (issue #14, store), async remote delivery (ADR-0006 point 5), and
// the killswitch trigger wiring (ADR-0006 point 6 — only the fail-closed signal
// is exposed here). This package is the local durable stage — the truth medium —
// and nothing more.
package audit

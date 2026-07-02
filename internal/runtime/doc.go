// Package runtime holds the unattended-execution foundation: a structured JSON
// logger and a panic-recover boundary that keeps a single goroutine's crash
// from taking down the whole process.
//
// Two CLAUDE.md invariants for 24/7 unattended operation live here:
//
//   - "죽지 않는다" — every long-lived goroutine runs behind a recover boundary
//     so an isolated panic is logged and contained, never fatal to the process.
//   - "관측성" — logs are the only post-mortem diagnosis surface, so they are
//     structured (JSON) from the start.
//
// Scope boundary: this package is the structured-logging *foundation* only. It
// is NOT the durable audit sink (ADR-0005 point 6: durable ack / retry /
// idempotency for the audit sink are defined in a separate follow-up issue).
// It does NOT touch the transactional store (ADR-0005 point 5 keeps live state
// and audit history separate), and it is NOT a kill switch (ADR-0004 owns the
// halt contract). The recover boundary here deliberately logs-and-continues; it
// never escalates to stopping the process, because a panic-count trip is
// kill-switch territory and out of this issue's scope.
package runtime

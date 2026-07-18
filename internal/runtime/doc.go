// Package runtime is the unattended-execution foundation plus the process
// assembly: the structured JSON logger, the panic-recover supervisor, the
// clean-shutdown sentinel judgment, and the wiring that composes every domain
// package into a runnable bot.
//
// Two CLAUDE.md invariants for 24/7 unattended operation live here:
//
//   - "죽지 않는다" — every long-lived goroutine runs behind a recover boundary
//     so an isolated panic is logged and contained, never fatal to the process.
//   - "관측성" — logs are the only post-mortem diagnosis surface, so they are
//     structured (JSON) from the start.
//
// # Why the assembly lives here and not in main
//
// cmd/bot/main.go must stay thin (CLAUDE.md), but the boot and shutdown ORDER is
// a safety contract, not glue: the sentinel must be read before it is
// overwritten, flipped to running before the replay gate can open, and a clean
// may be written only by a run that earned it (ADR-0012 Decision 1(c)). Those
// rules are only testable if they live in a package — the crash-timing suite in
// sentinel_crash_test.go models process boundaries by reopening a real store on
// disk, which is impossible against a main function.
//
// So this package owns the JUDGMENT that ADR-0012 deliberately kept out of
// store: store exposes an atomic sentinel set/get seam plus the halt phase, and
// runtime decides when a clean is eligible and when an unclean boot must come up
// conservatively halted.
//
// # Scope boundary
//
// It is still NOT the durable audit sink (internal/audit, ADR-0006) and NOT the
// kill switch (internal/killswitch, ADR-0004) — it constructs and sequences
// them, it does not reimplement them. It reads and writes exactly one store
// surface of its own, the lifecycle sentinel, through a seam narrow enough
// (SentinelStore) that it structurally cannot touch the journal, and its guard
// seam (SentinelGuard) exposes no ClearHalt, so shutdown tidying can never
// release a halt that only a human may release (ADR-0004 point 6). The recover
// boundary deliberately logs-and-continues and never escalates to stopping the
// process: a panic-count trip is kill-switch territory.
//
// Nothing here decides WHAT to trade. The submit path is assembled and dormant
// until a strategy exists.
package runtime

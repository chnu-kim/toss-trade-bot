// Package reconciler is the single truth-establishing engine (ADR-0003 point 1,
// ADR-0014). It answers "what actually happened when a submit was ambiguous, or
// when the process died and came back" and escalates what it cannot establish.
//
// It is QUERY-ONLY. It never submits or re-submits an order (ADR-0003 point 4);
// it only reads order truth, closes journal intents, re-emits audit records, and
// reports/trips the kill switch. It keeps running while the bot is halted — a
// halt blocks new exposure, not truth recovery (ADR-0004 point 1).
//
// # The four narrow seams are themselves safety invariants
//
// Like the submit path (#34), the seams this package depends on are deliberately
// narrower than the concrete types behind them, so whole classes of violation are
// structurally impossible rather than merely "not currently written":
//
//   - OrderAPI has no SubmitOrder, so the reconciler CANNOT place or replace an
//     order (ADR-0003 point 4). Truth recovery can never become a re-send.
//   - Journal has no AppendIntent/AppendMarker, so the reconciler CANNOT forge a
//     marker — it can never manufacture an "acked" state and bind a guessed
//     orderId to an intent (ADR-0003 point 3 auto-ack ban).
//   - Journal exposes no Halt/HaltPhase read and Guard exposes no
//     HasUnpersistedPendingHalt, so a halt-phase guard on resolve/finalize CANNOT
//     be wired without widening the seam. This is the twin artifact of ADR-0014
//     Decision 8's no-guard finding: persistence-wins is absorbed by
//     count-before-resolve plus ambiguous-is-structurally-unresolvable, and a
//     halt-phase belt would freeze finalization for the lifetime of a
//     human-clear-only halt or a bootHalt (codex R2/R3) for zero safety gain.
//   - Guard has no ClearHalt/FinalizePendingHalt, so the reconciler CANNOT clear
//     a global halt (ADR-0004 point 6: global clear is human-only). It re-fires
//     from live backlog evidence instead. It also has no
//     ReportTokenRefreshFailure: token escalation is killswitch's own
//     (ADR-0014 Decision 7).
//
// # What it does
//
// Boot runs two sequential passes in one goroutine (ADR-0014 Decision 9):
//
//	pass 1  marker branching over store.LoadUnresolvedIntents —
//	        prepared-only            → resolve aborted-before-submit
//	        acked (orderId present)  → ONE GetOrder classification; closed ⇒ record
//	                                   + resolve, open ⇒ hand to the live tracker
//	                                   (never polled to completion inline)
//	        submit-attempted, no orderId, settle window elapsed
//	                                 → unresolved-ambiguous: per-symbol Trip floor,
//	                                   and a global Trip once the backlog reaches
//	                                   the threshold. Never demoted to ABSENT,
//	                                   never auto-acked from an OPEN payload match.
//	        …then, and only then, NotifyScanComplete opens the replay gate, so the
//	        gate never opens over a kill switch that has not yet been re-derived.
//	pass 2  audit re-emit driver — every not-fully-audited intent's un-acked
//	        lifecycle records are re-emitted and acked, converging the prune gate
//	        (ADR-0006 point 4, #20's reconstruction function gets its driver here).
//
// After boot, a supervised bounded ticker re-runs the same reconciliation on
// reevalInterval (ADR-0014 Decision 11) so the escalation windows stay bounded in
// a quiet market where no submit ever fires the wake seam; the tick body runs
// inside a recover boundary and sustained failure promotes to a fail-closed halt
// (Decision 12) rather than silently leaving those windows unbounded.
//
// # Ordering contracts that are fail-open the moment they are broken
//
//   - count-before-resolve (ADR-0012 Decision 3 / ADR-0014 Decision 8):
//     ReportOrderFailure must durably commit BEFORE ResolveIntent("rejected").
//     Both failure arms of ReportOrderFailure return non-nil, so this ordering is
//     exactly what keeps the intent unresolved as re-count evidence. Overcount
//     (re-counting after a crash between the two) is the safe direction.
//   - success-reset ordering guard (ADR-0014 Decision 8): ReportOrderSuccess
//     resets the consecutive-failure counter unconditionally and is explicitly
//     outside the count-ordering contract, so ordering is the CALLER's duty. A
//     FILL's reset is withheld while any equally-old-or-older intent's truth is
//     still undetermined, otherwise a late-confirmed REJECT is not merely delayed
//     but erased from the streak.
//   - resolve-before-reset on the success path — the exact opposite of the failure
//     path, for the same reason. A reset that ran before its own resolve stays
//     replayable while that resolve keeps failing, so a NEWER rejection counted in
//     between would be erased on the next cycle. Closing the intent first makes the
//     reset at-most-once. On top of that, a reset is dropped as stale whenever a
//     NEWER failure has already been counted — the in-doubt guard only orders a
//     fill against OLDER intents, so a slow lookup could otherwise let an older
//     fill zero a streak a newer rejection had already contributed to. Every
//     skipped reset only leaves the counter high, which over-halts — the safe
//     direction ADR-0012 point 4 sanctions. Across a RESTART the same question
//     cannot be answered at all — a resolved intent has left the journal, and the
//     streak is durable precisely so a restart cannot reset it (ADR-0004 point 7) —
//     so a fill that predates this process's first scan withholds its reset
//     entirely and self-corrects at the next fill this process orders end to end.
package reconciler

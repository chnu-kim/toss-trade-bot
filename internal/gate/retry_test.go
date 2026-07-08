package gate

import "testing"

func TestShouldProduceVerdict_NewSHA_NoEscalation_Produces(t *testing.T) {
	d := ShouldProduceVerdict(VerdictHistory{}, DefaultLimits())
	if !d.Produce {
		t.Fatalf("ShouldProduceVerdict() = %+v, want Produce=true for a fresh (PR, SHA) with no history", d)
	}
}

func TestShouldProduceVerdict_ExistingApproveOnSHA_Sticky_NoReproduce(t *testing.T) {
	h := VerdictHistory{SHAOutcomeRecorded: true, ExistingSHAOutcome: OutcomeApprove}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — an approved SHA must not be re-verdicted")
	}
}

func TestShouldProduceVerdict_ExistingRejectOnSHA_StickyRegardlessOfSignals(t *testing.T) {
	h := VerdictHistory{
		SHAOutcomeRecorded: true,
		ExistingSHAOutcome: OutcomeReject,
		PRIntervention:     PRReviewSubmittedByOwner(),
	}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — reject is sticky to the SHA even with a PR intervention signal")
	}
}

func TestShouldProduceVerdict_ExistingIndeterminate_NoSignal_Sticky(t *testing.T) {
	h := VerdictHistory{SHAOutcomeRecorded: true, ExistingSHAOutcome: OutcomeIndeterminate}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — indeterminate is sticky without an intervention signal")
	}
}

func TestShouldProduceVerdict_ExistingIndeterminate_WithPRSignal_Reprocesses(t *testing.T) {
	h := VerdictHistory{
		SHAOutcomeRecorded: true,
		ExistingSHAOutcome: OutcomeIndeterminate,
		PRIntervention:     PRReviewSubmittedByOwner(),
	}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = false, want true — indeterminate + chnu-kim review signal allows same-SHA reprocessing")
	}
}

func TestShouldProduceVerdict_PRStreakAtLimit_Escalates(t *testing.T) {
	h := VerdictHistory{PRNonApproveStreak: 3}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — PR streak reached N, must escalate")
	}
}

func TestShouldProduceVerdict_PRStreakBelowLimit_Produces(t *testing.T) {
	h := VerdictHistory{PRNonApproveStreak: 2}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = false, want true — PR streak below N")
	}
}

func TestShouldProduceVerdict_PRStreakEscalated_ClearedByOwnerReview(t *testing.T) {
	h := VerdictHistory{PRNonApproveStreak: 5, PRIntervention: PRReviewSubmittedByOwner()}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = false, want true — chnu-kim PR review clears PR-level escalation")
	}
}

func TestShouldProduceVerdict_GlobalCountExceedsLimit_Escalates(t *testing.T) {
	h := VerdictHistory{GlobalNonApproveCount: 10}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — global count exceeds M, must escalate repo-wide")
	}
}

func TestShouldProduceVerdict_GlobalCountAtLimit_StillAllowed(t *testing.T) {
	// "넘으면" (exceeds) the cap escalates — reaching exactly M does not yet.
	h := VerdictHistory{GlobalNonApproveCount: GlobalNonApproveLimit}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = false, want true — count == M is the boundary, not yet over it")
	}
}

func TestShouldProduceVerdict_GlobalEscalation_ClearedByOwnerWorkflowDispatch(t *testing.T) {
	h := VerdictHistory{GlobalNonApproveCount: 20, GlobalIntervention: GlobalWorkflowDispatchByOwner()}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = false, want true — chnu-kim workflow_dispatch clears global escalation")
	}
}

func TestShouldProduceVerdict_GlobalEscalation_PRSignalDoesNotClearIt(t *testing.T) {
	// The two signals are on independent axes — a PR-level review must not
	// substitute for the global chnu-kim workflow_dispatch clearance.
	h := VerdictHistory{GlobalNonApproveCount: 20, PRIntervention: PRReviewSubmittedByOwner()}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — a PR-level review must not clear the global escalation")
	}
}

func TestShouldProduceVerdict_GlobalEscalationCheckedBeforePRStreak(t *testing.T) {
	// Global escalation is repo-wide and must win even if this particular
	// PR's own streak looks fine.
	h := VerdictHistory{GlobalNonApproveCount: 20, PRNonApproveStreak: 0}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — global escalation applies even to a PR with a clean streak")
	}
}

func TestShouldProduceVerdict_ExposesGlobalEscalationStateEvenForStickyApprovedSHA(t *testing.T) {
	// codex:adversarial-review [high] finding on an earlier fix: a caller
	// (the workflow's "Determine leg outcome" step) that only checks
	// SHAOutcomeRecorded to decide whether to re-affirm an existing
	// approve verdict has no way to tell "SHA-sticky approve" apart from
	// "blocked by an active global M-cap escalation that happens to also
	// have a prior approve on this SHA" — re-affirming the old approve in
	// the latter case would let a repo-wide halt be silently bypassed by
	// re-dispatching any previously-approved SHA. RetryDecision must expose
	// the global-escalation state independently of which branch produced
	// the Produce/Reason result, so callers can gate re-affirmation on it.
	h := VerdictHistory{
		SHAOutcomeRecorded:    true,
		ExistingSHAOutcome:    OutcomeApprove,
		GlobalNonApproveCount: 20, // over the default M=9, no clear signal
	}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if !d.GloballyEscalated {
		t.Fatal("ShouldProduceVerdict().GloballyEscalated = false, want true — a caller must be able to tell global escalation is active even when a prior approve exists for this SHA")
	}
}

func TestShouldProduceVerdict_GloballyEscalatedFalseWhenUnderLimit(t *testing.T) {
	h := VerdictHistory{SHAOutcomeRecorded: true, ExistingSHAOutcome: OutcomeApprove, GlobalNonApproveCount: 2}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.GloballyEscalated {
		t.Fatal("ShouldProduceVerdict().GloballyEscalated = true, want false when the global count is well under M")
	}
}

func TestShouldProduceVerdict_GlobalEscalationBlocksIndeterminateSHAReprocessEvenWithPRSignal(t *testing.T) {
	// codex:review [P1] finding: the SHA-sticky indeterminate+PR-signal
	// reprocess branch previously returned Produce=true before the global
	// cap was ever checked, so a PR-level chnu-kim review on ONE PR could
	// bypass a repo-wide M-cap escalation that is supposed to require the
	// separate, chnu-kim-actor global workflow_dispatch clear signal —
	// exactly the axis-independence ADR-0011 point 4(e)(iv) requires
	// ("전역 해제 신호는 PR-단위 신호... 와 별개").
	h := VerdictHistory{
		SHAOutcomeRecorded:    true,
		ExistingSHAOutcome:    OutcomeIndeterminate,
		PRIntervention:        PRReviewSubmittedByOwner(),
		GlobalNonApproveCount: 20, // well over the default M=9, no clear signal
	}
	d := ShouldProduceVerdict(h, DefaultLimits())
	if d.Produce {
		t.Fatal("ShouldProduceVerdict() Produce = true, want false — an active global escalation must block same-SHA reprocessing even with a PR-level intervention signal")
	}
}

func TestInterventionSignal_ZeroValueIsAbsent(t *testing.T) {
	var s InterventionSignal
	if s.Present() {
		t.Fatal("InterventionSignal zero value Present() = true, want false")
	}
}

func TestCombineLegOutcomes_SingleLegPassthrough(t *testing.T) {
	if got := CombineLegOutcomes([]Outcome{OutcomeApprove}); got != OutcomeApprove {
		t.Errorf("CombineLegOutcomes(single approve) = %v, want approve", got)
	}
}

func TestCombineLegOutcomes_RejectBeatsApprove(t *testing.T) {
	got := CombineLegOutcomes([]Outcome{OutcomeApprove, OutcomeReject})
	if got != OutcomeReject {
		t.Errorf("CombineLegOutcomes(approve, reject) = %v, want reject", got)
	}
}

func TestCombineLegOutcomes_IndeterminateBeatsApprove(t *testing.T) {
	got := CombineLegOutcomes([]Outcome{OutcomeApprove, OutcomeIndeterminate})
	if got != OutcomeIndeterminate {
		t.Errorf("CombineLegOutcomes(approve, indeterminate) = %v, want indeterminate", got)
	}
}

func TestCombineLegOutcomes_RejectBeatsIndeterminate(t *testing.T) {
	got := CombineLegOutcomes([]Outcome{OutcomeIndeterminate, OutcomeReject})
	if got != OutcomeReject {
		t.Errorf("CombineLegOutcomes(indeterminate, reject) = %v, want reject", got)
	}
}

func TestCombineLegOutcomes_AllApprove(t *testing.T) {
	got := CombineLegOutcomes([]Outcome{OutcomeApprove, OutcomeApprove})
	if got != OutcomeApprove {
		t.Errorf("CombineLegOutcomes(approve, approve) = %v, want approve", got)
	}
}

func TestCombineLegOutcomes_EmptyIsIndeterminateNotSilentApprove(t *testing.T) {
	// An empty leg-outcome slice is a caller programming error (every
	// verdict-check regime requires at least one leg — ADR-0011 point
	// 4(g)); this must never silently resolve to Approve.
	got := CombineLegOutcomes(nil)
	if got != OutcomeIndeterminate {
		t.Errorf("CombineLegOutcomes(nil) = %v, want indeterminate (fail-closed on a programming error)", got)
	}
}

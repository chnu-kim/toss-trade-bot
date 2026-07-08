package gate

// InterventionSignal is a verified, loop-unforgeable human-intervention
// signal (ADR-0011 point 4(e)(iv)). Its zero value means "no signal
// present". There is no exported way to construct a non-zero
// InterventionSignal other than the two functions below, and each names the
// one legitimate source its axis (PR-level, global) permits — a label, an
// issue comment, or a repository_dispatch payload cannot be turned into a
// valid InterventionSignal by a caller, correctly or by mistake, because no
// constructor accepts an arbitrary source string (ADR-0011 point 4(e)(iv):
// "라벨·이슈 코멘트·repository_dispatch를 해제 신호로 읽는 구현은 금지").
// See TestInterventionSignal_ZeroValueIsAbsent and the package-level
// constructor inventory this comment documents for the regression this
// closes.
type InterventionSignal struct {
	present bool
	source  string
}

// PRReviewSubmittedByOwner is the only valid PR-level unstick signal: the
// caller must have already confirmed, via the GitHub API (never via a label
// or comment), that chnu-kim submitted a review on this exact PR (ADR-0011
// point 4(e)(iv)).
func PRReviewSubmittedByOwner() InterventionSignal {
	return InterventionSignal{present: true, source: "pr-review:chnu-kim"}
}

// GlobalWorkflowDispatchByOwner is the only valid global unstick signal: the
// caller must have already confirmed the triggering workflow_dispatch's
// actor is chnu-kim (ADR-0011 point 4(e)(iv): "chnu-kim이 직접 실행하는
// workflow_dispatch(main 정의, actor == chnu-kim 검사)").
func GlobalWorkflowDispatchByOwner() InterventionSignal {
	return InterventionSignal{present: true, source: "workflow_dispatch:chnu-kim"}
}

// Present reports whether this InterventionSignal represents an actually
// verified signal (as opposed to the zero value, "no signal").
func (s InterventionSignal) Present() bool { return s.present }

// VerdictHistory is the loop-unforgeable state a privileged verdict-gate job
// must read — from check-run / privileged job execution history only
// (ADR-0011 point 4(e)(iv)) — before deciding whether to produce a new
// verdict for a given (PR, head SHA). Every field here is data the caller is
// responsible for having sourced correctly; this package only implements
// the decision the ADR specifies over that data.
type VerdictHistory struct {
	// SHAOutcomeRecorded and ExistingSHAOutcome describe an
	// already-produced verdict-check outcome for the exact (PR, head SHA)
	// pair under evaluation, if any.
	SHAOutcomeRecorded bool
	ExistingSHAOutcome Outcome

	// PRNonApproveStreak is the current consecutive non-approve count for
	// this PR at the verdict-check level (ADR-0011 point 4(g): aggregated
	// per verdict-check, not per leg — see CombineLegOutcomes).
	PRNonApproveStreak int

	// GlobalNonApproveCount is the repo-wide cumulative non-approve count,
	// across every PR, since the last global escalation clearance.
	GlobalNonApproveCount int

	// PRIntervention is the PR-level unstick signal, if present.
	PRIntervention InterventionSignal
	// GlobalIntervention is the global unstick signal, if present.
	GlobalIntervention InterventionSignal
}

// RetryDecision is ShouldProduceVerdict's result. Reason is always populated
// (whether Produce is true or false) so an unattended run leaves a
// diagnosable trail of exactly why a verdict was or was not produced.
type RetryDecision struct {
	Produce bool
	Reason  string
}

// ShouldProduceVerdict implements ADR-0011 point 4(e)(iv)'s full retry
// policy for one (PR, head SHA) evaluation: SHA-sticky first, then the
// repo-wide cap M, then the PR-level streak cap N — each fail-closed (skip
// producing a new verdict) unless its own specific intervention signal is
// present. Global escalation is checked before the PR-level streak because
// it is a repo-wide condition that must hold regardless of this particular
// PR's own history (ADR-0011 point 4(e)(iv), round 8).
func ShouldProduceVerdict(h VerdictHistory, limits Limits) RetryDecision {
	if h.SHAOutcomeRecorded {
		switch h.ExistingSHAOutcome {
		case OutcomeIndeterminate:
			if h.PRIntervention.Present() {
				return RetryDecision{Produce: true, Reason: "동일 head SHA의 판정 불능 기록을 chnu-kim PR 리뷰 신호로 재산출"}
			}
			return RetryDecision{Produce: false, Reason: "동일 head SHA에 판정 불능 기록이 이미 존재 — sticky(사람-개입 신호 없음)"}
		default: // Approve, Reject
			return RetryDecision{Produce: false, Reason: "동일 head SHA에 verdict가 이미 기록됨 — sticky"}
		}
	}

	if h.GlobalNonApproveCount > limits.GlobalNonApprove && !h.GlobalIntervention.Present() {
		return RetryDecision{Produce: false, Reason: "전역 비-approve 누적이 상한 M을 초과 — 레포-전역 sticky 에스컬레이션"}
	}

	if h.PRNonApproveStreak >= limits.PRNonApprove && !h.PRIntervention.Present() {
		return RetryDecision{Produce: false, Reason: "PR 연속 비-approve가 상한 N에 도달 — PR 단위 sticky 에스컬레이션"}
	}

	return RetryDecision{Produce: true, Reason: "새 head SHA, 상한 미초과"}
}

// CombineLegOutcomes implements ADR-0011 point 4(g): a verdict-check may be
// backed by one leg (codex only, for non-critical paths) or two (codex +
// the independent Claude adversarial leg, for risk:critical/unmapped
// paths), but the retry counters this package tracks aggregate at the
// verdict-check level, not per leg. This combines every leg's Outcome for
// one (PR, head SHA) evaluation into the single Outcome that feeds
// VerdictHistory.PRNonApproveStreak / GlobalNonApproveCount: Reject beats
// Indeterminate beats Approve (the most conservative outcome wins). An
// empty legs slice is a caller programming error — every regime requires at
// least one leg — and this function refuses to let that silently resolve to
// Approve, returning Indeterminate instead (fail-closed).
func CombineLegOutcomes(legs []Outcome) Outcome {
	if len(legs) == 0 {
		return OutcomeIndeterminate
	}
	result := OutcomeApprove
	for _, o := range legs {
		if o == OutcomeReject {
			return OutcomeReject
		}
		if o == OutcomeIndeterminate {
			result = OutcomeIndeterminate
		}
	}
	return result
}

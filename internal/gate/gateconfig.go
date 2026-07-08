package gate

// PRNonApproveLimit (N) is the maximum number of consecutive non-approve
// (Reject or Indeterminate) verdict-check outcomes a single PR may
// accumulate before PR-level sticky escalation halts further verdict
// production for that PR until chnu-kim submits a review on it (ADR-0011
// point 4(e)(iv), value confirmed in the implementation issue for #48).
const PRNonApproveLimit = 3

// GlobalNonApproveLimit (M) is the maximum repo-wide cumulative count of
// non-approve verdict-check outcomes, across every PR, since the last
// global escalation clearance, before repo-wide sticky escalation halts
// verdict production for every PR until chnu-kim runs the clearing
// workflow_dispatch (actor-checked) (ADR-0011 point 4(e)(iv), value
// confirmed in the implementation issue for #48).
const GlobalNonApproveLimit = 9

// Limits bundles the two retry caps ShouldProduceVerdict enforces.
type Limits struct {
	PRNonApprove     int
	GlobalNonApprove int
}

// DefaultLimits returns the ADR-confirmed N/M pair. Callers should use this
// rather than constructing a Limits literal directly, so the confirmed
// values stay in exactly one place.
func DefaultLimits() Limits {
	return Limits{PRNonApprove: PRNonApproveLimit, GlobalNonApprove: GlobalNonApproveLimit}
}

package gate

// Outcome is the result of one verdict-check evaluation for a (PR, head SHA)
// pair (ADR-0008 point 1, ADR-0011 point 4(e)(iii)). It is never "pending" —
// evaluation is synchronous and terminal. A verdict-check either produces a
// definitive Approve, a definitive Reject, or falls back to Indeterminate
// because independent sanity verification could not confirm a leg's raw
// Decision (ADR-0011 point 4(e)(iii): schema validity alone is not enough,
// and a failed cross-check is fail-closed, not a reject).
type Outcome int

const (
	OutcomeApprove Outcome = iota
	OutcomeReject
	OutcomeIndeterminate
)

// String renders o for logs and check-run output.
func (o Outcome) String() string {
	switch o {
	case OutcomeApprove:
		return "approve"
	case OutcomeReject:
		return "reject"
	case OutcomeIndeterminate:
		return "indeterminate"
	default:
		return "unknown"
	}
}

// NonApprove reports whether o counts toward the PR-level (N) and repo-wide
// (M) non-approve retry counters (ADR-0011 point 4(e)(iv)) — both Reject and
// Indeterminate count against the caps; only Approve does not.
func (o Outcome) NonApprove() bool {
	return o != OutcomeApprove
}

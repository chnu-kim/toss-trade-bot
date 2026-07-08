package enforcement

// Check names identify which of the three ADR-0009 point 8 pillars a
// CheckResult belongs to.
const (
	CheckNameCodeowners       = "codeowners"
	CheckNameBranchProtection = "branch_protection"
	CheckNameIdentity         = "identity"
)

// CheckResult is the verdict of one independent presence-check pillar. Reason
// is always populated when Satisfied is false, and is meant to be logged
// verbatim so an unattended run leaves a diagnosable trail of exactly which
// pillar was missing and why.
type CheckResult struct {
	Name      string
	Satisfied bool
	Reason    string
}

// metResult builds a satisfied CheckResult for the named pillar.
func metResult(name string) CheckResult {
	return CheckResult{Name: name, Satisfied: true}
}

// unmetResult builds a fail-closed CheckResult for the named pillar. reason
// must explain why the pillar could not be confirmed (missing, mismatched, or
// unverifiable due to an error) — "no evidence" is always reported as unmet,
// never silently upgraded to satisfied.
func unmetResult(name, reason string) CheckResult {
	return CheckResult{Name: name, Satisfied: false, Reason: reason}
}

// Result is the aggregate, fail-closed verdict of all three presence-check
// pillars: Satisfied is true only if every Check is satisfied. Any single
// unmet or unverifiable pillar collapses the whole Result to false, per
// ADR-0009 point 8.
type Result struct {
	Satisfied bool
	Checks    []CheckResult
}

// Reasons returns the Reason of every unsatisfied check, in check order. It is
// empty when Satisfied is true.
func (r Result) Reasons() []string {
	var reasons []string
	for _, c := range r.Checks {
		if !c.Satisfied {
			reasons = append(reasons, c.Name+": "+c.Reason)
		}
	}
	return reasons
}

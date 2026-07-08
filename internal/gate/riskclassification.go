package gate

// RiskClass is the ADR-0008 point 5 risk classification of a set of changed
// paths — RiskCritical requires the codex+independent-Claude N-of-2 regime
// (ADR-0008 point 3), RiskNonCritical allows the codex-only single-leg
// default (ADR-0008 point 2).
type RiskClass string

const (
	RiskCritical    RiskClass = "critical"
	RiskNonCritical RiskClass = "non-critical"
)

// PathRule is one ordered entry in the risk-classification mapping
// (ADR-0008 point 5). Rules are evaluated in file order with last-match-wins
// per path — mirroring CODEOWNERS resolution semantics for consistency —
// so a later, narrower rule can override an earlier, broader one for a
// specific path (e.g. carving a critical subdirectory out of an otherwise
// non-critical parent directory).
type PathRule struct {
	Pattern string
	Class   RiskClass
}

// ClassifyChangedPaths implements ADR-0008 point 5: a PR is RiskCritical
// unless every one of its changed paths resolves (via rules, last match
// wins) to an explicit RiskNonCritical rule. A path with no matching rule at
// all defaults to RiskCritical ("매핑에 없는 경로의 기본값은 critical") —
// this function never returns RiskNonCritical for an unmapped path. An
// empty paths slice is also classified RiskCritical (fail-closed): there is
// no legitimate reason for a verdict-check to run over zero changed paths,
// and treating "nothing to check" as "safe" would be exactly the kind of
// vacuous-pass this gate exists to avoid.
func ClassifyChangedPaths(paths []string, rules []PathRule) RiskClass {
	if len(paths) == 0 {
		return RiskCritical
	}
	for _, p := range paths {
		if classifyPath(p, rules) != RiskNonCritical {
			return RiskCritical
		}
	}
	return RiskNonCritical
}

func classifyPath(path string, rules []PathRule) RiskClass {
	class := RiskCritical
	matched := false
	for _, r := range rules {
		if globMatches(r.Pattern, path) {
			class = r.Class
			matched = true
		}
	}
	if !matched {
		return RiskCritical
	}
	return class
}

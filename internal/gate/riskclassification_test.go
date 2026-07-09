package gate

import "testing"

func TestClassifyChangedPaths_UnmappedPathDefaultsCritical(t *testing.T) {
	rules := []PathRule{{Pattern: "docs/**", Class: RiskNonCritical}}
	got := ClassifyChangedPaths([]string{"some/brand/new/path.go"}, rules)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v for a path with no matching rule", got, RiskCritical)
	}
}

func TestClassifyChangedPaths_NoRulesAtAll_DefaultsCritical(t *testing.T) {
	got := ClassifyChangedPaths([]string{"docs/adr/0011-loop-pr-credential-flow.md"}, nil)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v with an empty mapping", got, RiskCritical)
	}
}

func TestClassifyChangedPaths_EmptyPathList_DefaultsCritical(t *testing.T) {
	rules := []PathRule{{Pattern: "**", Class: RiskNonCritical}}
	got := ClassifyChangedPaths(nil, rules)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths(nil paths) = %v, want %v (fail-closed, not vacuously non-critical)", got, RiskCritical)
	}
}

func TestClassifyChangedPaths_AllPathsExplicitlyNonCritical(t *testing.T) {
	rules := []PathRule{
		{Pattern: "**", Class: RiskCritical},
		{Pattern: "docs/**", Class: RiskNonCritical},
	}
	got := ClassifyChangedPaths([]string{"docs/adr/0000-template.md", "docs/README.md"}, rules)
	if got != RiskNonCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v when every path matches an explicit non-critical rule", got, RiskNonCritical)
	}
}

func TestClassifyChangedPaths_OneCriticalPathMakesWholePRCritical(t *testing.T) {
	rules := []PathRule{
		{Pattern: "**", Class: RiskCritical},
		{Pattern: "docs/**", Class: RiskNonCritical},
		{Pattern: "internal/order/**", Class: RiskCritical},
	}
	got := ClassifyChangedPaths([]string{"docs/README.md", "internal/order/order.go"}, rules)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v when even one changed path is critical", got, RiskCritical)
	}
}

func TestClassifyChangedPaths_LastMatchingRuleWins(t *testing.T) {
	// docs/adr/** is nested under docs/** — a later, narrower rule should
	// override the earlier broader one for paths under docs/adr/.
	rules := []PathRule{
		{Pattern: "docs/**", Class: RiskNonCritical},
		{Pattern: "docs/adr/**", Class: RiskCritical},
	}
	got := ClassifyChangedPaths([]string{"docs/adr/0011-loop-pr-credential-flow.md"}, rules)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v — last matching rule (docs/adr/**) should win over the earlier docs/** rule", got, RiskCritical)
	}

	got = ClassifyChangedPaths([]string{"docs/README.md"}, rules)
	if got != RiskNonCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v for a docs path not covered by the narrower override", got, RiskNonCritical)
	}
}

func TestClassifyChangedPaths_GateOwnArtifactsAreCritical(t *testing.T) {
	// The gate's own logic and mapping file must classify as critical by
	// default (unmapped) — this is a regression guard for the exact
	// self-protection circularity ADR-0011 point 4(b) round 9 describes: a
	// PR that touches the gate's own definition must not be able to
	// classify itself as low-scrutiny.
	got := ClassifyChangedPaths([]string{"internal/gate/riskclassification.go"}, nil)
	if got != RiskCritical {
		t.Fatalf("ClassifyChangedPaths() = %v, want %v for the gate package's own source with no mapping loaded", got, RiskCritical)
	}
}

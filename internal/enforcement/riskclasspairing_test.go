package enforcement

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/gate"
)

// sacred => critical pairing (ADR-0016 point 15 (e), point 12).
//
// sacredRequiredPaths says which files CODEOWNERS must keep owned;
// configs/gate/risk-classification.json says which files the ADR-0008 verdict
// gate scrutinises with the codex+independent-Claude N-of-2 regime instead of
// the codex-only single leg. Those two registrations are hand-mirrored and can
// drift independently: a path can be sacred (owner review required under
// branch protection) yet classified non-critical, so that once Phase B runs
// with required approvals at zero the ONLY automated check standing in front
// of it is a single codex leg. That is exactly the asymmetry ADR-0016 point 15
// found twice (the `.claude/**` carve-out, then the sibling docs/runbooks
// promotion of point 12) and both times an external adversarial reviewer
// caught it, not the repo.
//
// This test fixes the pairing as a check instead of a human discipline: every
// sacredRequiredPaths entry must classify RiskCritical through the real
// classifier. Like adrprotects.go, it is a DRIFT DETECTOR, not an enforcer —
// a green run here grants nothing; it only reports that no sacred path has
// quietly fallen into the single-leg class. Red means some sacred file could be
// autonomously merged on a codex-only verdict.
//
// The mapping is loaded exactly the way cmd/verdict-gate loads it (same JSON
// shape, same gate.PathRule type) and classified by gate.ClassifyChangedPaths,
// so the assertion reflects what the privileged job would actually decide
// rather than a re-implementation of its matching rules.

const riskClassificationPath = "../../configs/gate/risk-classification.json"

// loadRiskClassificationRules reads the real mapping. Fail closed on an
// unreadable or empty rule set: zero rules would make every path "unmapped",
// which ClassifyChangedPaths treats as critical — a vacuous green.
func loadRiskClassificationRules(t *testing.T) []gate.PathRule {
	t.Helper()

	raw, err := os.ReadFile(riskClassificationPath)
	if err != nil {
		t.Fatalf("read %s: %v", riskClassificationPath, err)
	}
	var mapping struct {
		Rules []gate.PathRule `json:"rules"`
	}
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatalf("parse %s: %v", riskClassificationPath, err)
	}
	if len(mapping.Rules) == 0 {
		t.Fatalf("%s has no rules — every path would be classified by the unmapped "+
			"default and this check would pass vacuously", riskClassificationPath)
	}
	return mapping.Rules
}

func TestSacredRequiredPaths_AreAllCriticalInRiskClassification(t *testing.T) {
	rules := loadRiskClassificationRules(t)
	if len(sacredRequiredPaths) == 0 {
		t.Fatalf("sacredRequiredPaths is empty — nothing to pair, the check would pass vacuously")
	}

	for _, p := range sacredRequiredPaths {
		if got := gate.ClassifyChangedPaths([]string{p}, rules); got != gate.RiskCritical {
			t.Errorf("%s is sacred (owner review required) but risk-classification.json "+
				"resolves it to %q — a PR touching it would get only the codex single leg "+
				"(ADR-0016 point 15 (e): sacred => critical)", p, got)
		}
	}
}

func TestRiskClassification_ClaudeDirHasNoNonCriticalCarveOut(t *testing.T) {
	rules := loadRiskClassificationRules(t)

	// Regression guard for ADR-0016 point 15 (d): the whole `.claude/**` carve-out
	// is removed, not narrowed, so the tree falls back to the repo default
	// (critical). Any non-critical rule rooted at .claude would re-open it.
	for _, r := range rules {
		if len(r.Pattern) >= len(".claude") && r.Pattern[:len(".claude")] == ".claude" &&
			r.Class == gate.RiskNonCritical {
			t.Errorf("risk-classification.json carves %q out as %q — the .claude tree holds "+
				"instruction surfaces and project hooks and must stay critical (ADR-0016 point 15 (d))",
				r.Pattern, r.Class)
		}
	}

	// Probe a concrete file the carve-out used to cover: this is what the gate
	// would decide for a PR editing an orchestration skill.
	probe := ".claude/skills/architect/SKILL.md"
	if got := gate.ClassifyChangedPaths([]string{probe}, rules); got != gate.RiskCritical {
		t.Errorf("%s classifies as %q, want %q", probe, got, gate.RiskCritical)
	}
}

package gate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Decision is the raw approve/reject judgment a verdict-generating leg
// (codex or the independent Claude adversarial leg — ADR-0011 point 4(g))
// outputs. Unlike Outcome, Decision has no indeterminate value: a leg is
// only ever prompted to output approve or reject (ADR-0008 point 1).
// Indeterminate is something the sanity layer (sanity.go) derives when it
// cannot trust that output — never something a leg emits directly.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// Leg identifies which verdict generator produced a Verdict (ADR-0011 point
// 4(g): the (b)/(c)/(e)/(f) regime applies leg-by-leg, and retry counters
// aggregate at the verdict-check level across legs, not per leg).
type Leg string

const (
	LegCodex             Leg = "codex"
	LegClaudeAdversarial Leg = "claude-adversarial"
)

// EvidenceRef is one citation a leg's verdict makes into the actual PR diff
// — the mechanical hook the sanity layer (ADR-0011 point 4(e)(iii)) uses to
// confirm a verdict is grounded in the diff it was given, not a hallucinated
// or injected judgment.
type EvidenceRef struct {
	File string `json:"file"`
	Hunk string `json:"hunk"`
}

// Verdict is the schema-enforced structured output every leg must produce
// (ADR-0008 point 1: "기계적 구조화 verdict"). ParseVerdict enforces only
// this schema; it does not by itself make a Verdict trustworthy — the
// independent sanity cross-check in sanity.go is a separate, mandatory
// second gate (ADR-0011 point 4(e)(iii): "approve는 스키마 유효성만으로
// check green이 되지 않는다").
type Verdict struct {
	Leg       Leg           `json:"leg"`
	Decision  Decision      `json:"decision"`
	Rationale string        `json:"rationale"`
	Evidence  []EvidenceRef `json:"evidence"`
}

// ParseVerdict decodes and schema-validates raw leg output. A non-nil error
// here is exactly the malformed-output case ADR-0011 point 4(e)(iii)
// requires callers to treat as Indeterminate (fail-closed) — never as a
// Reject, and never silently coerced into a valid Verdict.
func ParseVerdict(raw []byte) (Verdict, error) {
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return Verdict{}, fmt.Errorf("gate: malformed verdict JSON: %w", err)
	}
	if err := v.validate(); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

func (v Verdict) validate() error {
	switch v.Decision {
	case DecisionApprove, DecisionReject:
	default:
		return fmt.Errorf("gate: verdict decision %q is not one of %q/%q", v.Decision, DecisionApprove, DecisionReject)
	}
	switch v.Leg {
	case LegCodex, LegClaudeAdversarial:
	default:
		return fmt.Errorf("gate: verdict leg %q is not a recognized verdict-generating leg", v.Leg)
	}
	if strings.TrimSpace(v.Rationale) == "" {
		return fmt.Errorf("gate: verdict rationale is empty")
	}
	if v.Decision == DecisionApprove && len(v.Evidence) == 0 {
		return fmt.Errorf("gate: approve verdict cites no evidence")
	}
	return nil
}

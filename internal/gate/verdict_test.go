package gate

import (
	"strings"
	"testing"
)

func TestParseVerdict_ValidApprove(t *testing.T) {
	raw := []byte(`{
		"leg": "codex",
		"decision": "approve",
		"rationale": "diff only touches docs, no behavior change",
		"evidence": [{"file": "docs/adr/0011-loop-pr-credential-flow.md", "hunk": "+ typo fix"}]
	}`)
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict() unexpected error: %v", err)
	}
	if v.Leg != LegCodex || v.Decision != DecisionApprove {
		t.Errorf("ParseVerdict() = %+v, want leg=codex decision=approve", v)
	}
}

func TestParseVerdict_ValidReject_NoEvidenceRequired(t *testing.T) {
	raw := []byte(`{
		"leg": "claude-adversarial",
		"decision": "reject",
		"rationale": "diff removes the kill-switch check without a replacement"
	}`)
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict() unexpected error: %v", err)
	}
	if v.Decision != DecisionReject {
		t.Errorf("ParseVerdict() decision = %v, want reject", v.Decision)
	}
}

func TestParseVerdict_MalformedJSON(t *testing.T) {
	if _, err := ParseVerdict([]byte(`{not json`)); err == nil {
		t.Fatal("ParseVerdict() with malformed JSON: want error, got nil")
	}
}

func TestParseVerdict_UnknownDecisionRejectedAsSchemaInvalid(t *testing.T) {
	raw := []byte(`{"leg": "codex", "decision": "APPROVE!!", "rationale": "x", "evidence": [{"file":"a","hunk":"b"}]}`)
	if _, err := ParseVerdict(raw); err == nil {
		t.Fatal("ParseVerdict() with unrecognized decision: want error, got nil")
	}
}

func TestParseVerdict_UnknownLegRejected(t *testing.T) {
	raw := []byte(`{"leg": "gemini", "decision": "approve", "rationale": "x", "evidence": [{"file":"a","hunk":"b"}]}`)
	if _, err := ParseVerdict(raw); err == nil {
		t.Fatal("ParseVerdict() with unrecognized leg: want error, got nil")
	}
}

func TestParseVerdict_EmptyRationaleRejected(t *testing.T) {
	raw := []byte(`{"leg": "codex", "decision": "reject", "rationale": "   "}`)
	if _, err := ParseVerdict(raw); err == nil {
		t.Fatal("ParseVerdict() with blank rationale: want error, got nil")
	}
}

func TestParseVerdict_ApproveWithoutEvidenceRejected(t *testing.T) {
	raw := []byte(`{"leg": "codex", "decision": "approve", "rationale": "looks fine"}`)
	_, err := ParseVerdict(raw)
	if err == nil {
		t.Fatal("ParseVerdict() approve with no evidence: want error, got nil")
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Errorf("ParseVerdict() error = %q, want it to mention missing evidence", err.Error())
	}
}

func TestParseVerdict_ApproveWithBlankHunkRejected(t *testing.T) {
	// codex:adversarial-review finding: strings.Contains(h, "") is always
	// true, so an evidence entry with an empty (or whitespace-only) Hunk
	// would trivially "match" any hunk in SanityCheck without citing any
	// real diff text. Reject this at the schema layer, not just hope
	// SanityCheck's substring check happens to catch it.
	raw := []byte(`{"leg": "codex", "decision": "approve", "rationale": "x", "evidence": [{"file": "a.go", "hunk": "   "}]}`)
	if _, err := ParseVerdict(raw); err == nil {
		t.Fatal("ParseVerdict() approve with blank-hunk evidence: want error, got nil")
	}
}

func TestParseVerdict_ApproveWithBlankFileRejected(t *testing.T) {
	raw := []byte(`{"leg": "codex", "decision": "approve", "rationale": "x", "evidence": [{"file": "  ", "hunk": "+real change"}]}`)
	if _, err := ParseVerdict(raw); err == nil {
		t.Fatal("ParseVerdict() approve with blank-file evidence: want error, got nil")
	}
}

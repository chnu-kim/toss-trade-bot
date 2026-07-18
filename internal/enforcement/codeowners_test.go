package enforcement

import (
	"os"
	"strings"
	"testing"
)

// validCodeowners mirrors the real .github/CODEOWNERS content (ADR-0009 point
// 3-4): every sacred path explicitly listed with @chnu-kim as owner, including
// the file's self-reference.
const validCodeowners = `# enforcement-integrity sacred invariant (ADR-0009) 의 정적 보호 경로 목록.
#
# brace expansion은 지원되지 않는다 — 각 경로를 개별로 나열한다.

/.github/workflows/ @chnu-kim

/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim

/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim

/.github/CODEOWNERS @chnu-kim
`

func TestCheckCodeowners_AllSacredPathsOwned(t *testing.T) {
	got := CheckCodeowners(validCodeowners)
	if !got.Satisfied {
		t.Fatalf("CheckCodeowners() = %+v, want Satisfied=true", got)
	}
	if got.Name != CheckNameCodeowners {
		t.Fatalf("Name = %q, want %q", got.Name, CheckNameCodeowners)
	}
}

func TestCheckCodeowners_Empty(t *testing.T) {
	got := CheckCodeowners("")
	if got.Satisfied {
		t.Fatal("empty CODEOWNERS must not satisfy the check")
	}
	if got.Reason == "" {
		t.Fatal("unmet result must carry a reason")
	}
}

func TestCheckCodeowners_Missing0011FailsClosed(t *testing.T) {
	// ADR-0011 registered itself under the enforcement-integrity umbrella
	// (ADR-0011 point 11) — a CODEOWNERS that drops its line must fail check
	// (a), otherwise the registration exists only on paper (codex review
	// finding on PR #45: the mechanical check and the CODEOWNERS entry must
	// move together).
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0011) must not satisfy the check")
	}
}

func TestCheckCodeowners_Missing0012FailsClosed(t *testing.T) {
	// ADR-0012 declares protects: [live-execution-human-gate] and joins the
	// sacred set (like ADR-0004). A CODEOWNERS that drops its line must fail
	// check (a) — the twin-artifact rule (codex review finding on PR #59: the
	// protects: declaration and the CODEOWNERS/sacredRequiredPaths registration
	// must move together).
	// Otherwise-VALID sample with only the /docs/adr/0012-*.md line removed, so
	// the check fails specifically because 0012 is missing (not because some
	// other required owner is absent) — this is what actually guards the
	// twin-artifact requirement (codex review [P3] on PR #59).
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0012) must not satisfy the check")
	}
}

func TestCheckCodeowners_Missing0013FailsClosed(t *testing.T) {
	// ADR-0013 declares protects: [live-execution-human-gate] and joins the
	// sacred set (like ADR-0004/0012). A CODEOWNERS that drops its line must fail
	// check (a) — the twin-artifact rule (codex review finding on PR #63: the
	// protects: declaration and the CODEOWNERS/sacredRequiredPaths registration
	// must move together). Otherwise-VALID sample with only the /docs/adr/0013-*.md
	// line removed, so the check fails specifically because 0013 is missing.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0013) must not satisfy the check")
	}
}

func TestCheckCodeowners_Missing0014FailsClosed(t *testing.T) {
	// ADR-0014 declares protects: [live-execution-human-gate] and joins the
	// sacred set (like ADR-0004/0012/0013). A CODEOWNERS that drops its line
	// must fail check (a) — the twin-artifact rule: the protects: declaration
	// and the CODEOWNERS/sacredRequiredPaths registration must move together.
	// Otherwise-VALID sample with only the /docs/adr/0014-*.md line removed.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0014) must not satisfy the check")
	}
}

func TestCheckCodeowners_Missing0015FailsClosed(t *testing.T) {
	// ADR-0015 declares protects: [enforcement-integrity, live-execution-human-gate]
	// and joins the sacred set (ADR-0011 amendment: Phase A/B activation procedure).
	// A CODEOWNERS that drops its line must fail check (a) — the twin-artifact rule:
	// the protects: declaration and the CODEOWNERS/sacredRequiredPaths registration
	// must move together. Otherwise-VALID sample with only the /docs/adr/0015-*.md
	// line removed.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0015) must not satisfy the check")
	}
}

func TestCheckCodeowners_MissingPhaseBRunbookFailsClosed(t *testing.T) {
	// docs/runbooks/phase-b-entry.md holds the EXECUTABLE Phase B activation
	// steps (credential-narrowing order, App-key provisioning, flip-and-verify-
	// or-rollback, bootstrap prechecks) that ADR-0015 governs by reference. Per
	// ADR-0011 point 4(b) ("main에 있음 ≠ 보호됨"), leaving the runbook
	// unprotected while the ADR is protected lets a later loop PR reorder
	// provisioning ahead of narrowing, or drop rollback, without tripping the
	// code-owner gate — the protected ADR would then point operators at a
	// silently weakened procedure (codex adversarial finding on PR #73).
	// Otherwise-VALID sample with only the runbook line removed.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (phase-b-entry runbook) must not satisfy the check")
	}
}

func TestCheckCodeowners_MissingNarrowingScriptFailsClosed(t *testing.T) {
	// scripts/verify-credential-narrowing.sh is the capability-measurement path
	// that decides hard precondition ② ("credential narrowing complete"). If a
	// loop-controlled change can weaken it without a code-owner gate, the loop
	// manufactures its own green for the very check meant to prove it no longer
	// holds admin/approve capability — a false-green on the linchpin of Phase
	// A/B entry ordering (codex adversarial finding on PR #73).
	// Otherwise-VALID sample with only the /scripts/ rule removed.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (verify-credential-narrowing.sh) must not satisfy the check")
	}
}

func TestCheckCodeowners_MissingSacredPath(t *testing.T) {
	// docs/adr/0009-*.md line removed entirely.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("missing sacred path (0009) must not satisfy the check")
	}
}

func TestCheckCodeowners_PathPresentButOwnerStripped(t *testing.T) {
	// A path pattern with no owner listed does NOT enforce review in GitHub's
	// CODEOWNERS semantics — it is functionally equivalent to no protection at
	// all for that path. A naive substring-only check would wrongly pass this.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("sacred path with owner stripped must not satisfy the check")
	}
}

func TestCheckCodeowners_SelfReferenceMissing(t *testing.T) {
	// If CODEOWNERS doesn't own itself, loop could edit it to drop chnu-kim
	// without review (ADR-0009 point 4 explicitly calls this out).
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("CODEOWNERS missing its own self-reference must not satisfy the check")
	}
}

func TestCheckCodeowners_CommentsAndBlankLinesIgnored(t *testing.T) {
	content := `
# comment mentioning docs/adr/0009-*.md but not a real entry

/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if !got.Satisfied {
		t.Fatalf("comment-only mention must not satisfy the requirement on its own: %+v", got)
	}
}

// TestCheckCodeowners_RealFileSatisfies guards against drift between the
// actual .github/CODEOWNERS and the sacred paths ADR-0009 requires it to
// cover — if someone edits the real file and drops a sacred path, this test
// (not just the synthetic fixtures above) fails.
func TestCheckCodeowners_RealFileSatisfies(t *testing.T) {
	content, err := os.ReadFile("../../.github/CODEOWNERS")
	if err != nil {
		t.Fatalf("read real CODEOWNERS: %v", err)
	}
	got := CheckCodeowners(string(content))
	if !got.Satisfied {
		t.Fatalf("real .github/CODEOWNERS failed the presence-check: %+v", got)
	}
}

// --- Regression tests for codex review + adversarial-review findings on the
// initial version of this check: GitHub CODEOWNERS resolves ownership by
// "last matching pattern wins" (entirely, not merged with earlier matches) —
// see https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners.
// A check that merely asks "does ANY entry cover this path with the right
// owner" can be fooled by a later entry that also matches the same path but
// has a different (or no) owner — GitHub would use that later entry, not the
// earlier "protective-looking" one.

func TestCheckCodeowners_LaterOwnerlessEntryOverridesEarlierProtection(t *testing.T) {
	// codex:adversarial-review's exact example: a later, more specific
	// ownerless pattern for a file *inside* an already-"protected" directory
	// silently strips protection for that one file. GitHub uses the LAST
	// matching pattern, so /.github/workflows/release.yml is unowned in
	// reality even though an earlier line looks like it protects the whole
	// directory.
	content := `/.github/workflows/ @chnu-kim
/.github/workflows/ci.yml
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("a later ownerless entry overriding the representative workflows file must not satisfy the check")
	}
}

func TestCheckCodeowners_LaterDifferentOwnerEntryOverridesSelfReference(t *testing.T) {
	// codex:review's exact example: CODEOWNERS "protects" itself on one line,
	// but a later broader pattern re-assigns a different owner to the same
	// file. GitHub's last-match-wins rule means @other-team, not @chnu-kim,
	// actually governs review of CODEOWNERS edits.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
/.github/* @other-team
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("a later broader pattern reassigning the owner must not satisfy the check")
	}
}

func TestCheckCodeowners_LaterEntryWithSameOwnerStillSatisfies(t *testing.T) {
	// The precedence check must not become a blunt "must be the LAST line in
	// the file" rule — a later entry that still lists the required owner (for
	// the same or an overlapping pattern) is fine.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
/.github/workflows/ci.yml @chnu-kim
/.github/workflows/verdict-gate.yml @chnu-kim
`
	got := CheckCodeowners(content)
	if !got.Satisfied {
		t.Fatalf("a later entry that still lists the required owner must satisfy the check: %+v", got)
	}
}

// --- Regression tests for ADR-0011 point 11 / point 4(b): the verdict-gate
// (#48) artifacts that live outside .github/workflows/** (the judgement logic
// in internal/gate, the CLI in cmd/verdict-gate, and the risk-classification
// mapping in configs/gate/) are gate-defining script and data, not ordinary
// application code — "main에 있음 ≠ 보호됨" applies to them exactly as it
// does to the workflow YAML itself, so they must be covered by
// sacredRequiredPaths just like every other enforcement-integrity artifact.

func TestCheckCodeowners_MissingGateLogicPackage(t *testing.T) {
	// Otherwise-complete CODEOWNERS content that predates #48: no entries for
	// internal/gate/, cmd/verdict-gate/, or configs/gate/ at all.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("CODEOWNERS missing the verdict-gate logic package/binary/mapping entries must not satisfy the check")
	}
}

func TestCheckCodeowners_GateArtifactOwnerStripped(t *testing.T) {
	// internal/gate/ listed but with no owner — functionally unprotected,
	// exactly the "path present but owner stripped" failure mode the earlier
	// ADR-0009 sacred paths are already tested against.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/internal/gate/
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("internal/gate/ with owner stripped must not satisfy the check")
	}
}

func TestCheckCodeowners_NarrowerCarveOutOnOneGateFileNotCaught(t *testing.T) {
	// codex:review [P2] finding: sacredRequiredPaths previously sampled only
	// internal/gate/riskclassification.go as the directory's representative
	// file. The privileged workflow compiles and executes every non-test
	// file in internal/gate (cmd/verdict-gate imports the whole package),
	// so a later, narrower CODEOWNERS entry stripping ownership from a
	// DIFFERENT gate file (e.g. sanity.go, which implements the sanity
	// cross-check the whole gate depends on) must also be caught — not just
	// a strip on the one sampled file.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/internal/gate/ @chnu-kim
/internal/gate/sanity.go
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("a narrower ownerless entry stripping protection from internal/gate/sanity.go specifically must not satisfy the check, even though riskclassification.go (and the directory pattern) still look protected")
	}
}

func TestCheckCodeowners_PRCreationWorkflowCarveOutCaught(t *testing.T) {
	// Twin-artifact rule for issue #49: presence-check check (c-1) verifies
	// that .github/workflows/pr-creation.yml EXISTS on main, so check (a)
	// must verify it stays CODEOWNERS-protected — the same file being
	// verifiable but strippable would let a later, narrower ownerless entry
	// silently remove code-owner review from the one workflow that authors
	// loop PRs (ADR-0011 point 3: the PR-creation workflow definition is
	// main-pinned and gate-defining). The directory rule still matching is
	// not enough: GitHub resolves last-match-wins entirely.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/docs/adr/0014-*.md @chnu-kim
/docs/adr/0015-*.md @chnu-kim
/docs/runbooks/phase-b-entry.md @chnu-kim
/scripts/ @chnu-kim
/.github/workflows/pr-creation.yml
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.claude/skills/opensource-maintainer/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("a narrower ownerless entry stripping protection from pr-creation.yml specifically must not satisfy the check, even though the directory pattern still looks protective")
	}
}

func TestCheckCodeowners_MissingSecretScannerFailsClosed(t *testing.T) {
	// The commit-time/CI secret scanner is enforcement code: ci.yml runs
	// scan.sh against the PR checkout to block leaks (#27). If it is not
	// CODEOWNERS-protected, a PR can neuter the scanner (drop patterns, exit
	// 0) or append an allowlist entry in the same change and ship the leak the
	// gate exists to stop — the same "main에 있음 ≠ 보호됨" hole ADR-0011
	// point 4(b) closed for internal/gate (codex adversarial-review on PR #72).
	//
	// Otherwise-VALID sample with only the scanner line removed, so the check
	// fails specifically because the scanner is unprotected — that is what
	// actually guards the twin-artifact requirement.
	content := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0009-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/docs/adr/0011-*.md @chnu-kim
/docs/adr/0012-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/internal/gate/ @chnu-kim
/cmd/verdict-gate/ @chnu-kim
/configs/gate/ @chnu-kim
/.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if got.Satisfied {
		t.Fatal("unprotected secret scanner must not satisfy the check — a PR could disable the gate it is scanned by")
	}
	for _, want := range []string{
		".claude/skills/opensource-maintainer/scripts/scan.sh",
		".claude/skills/opensource-maintainer/allowlist.txt",
	} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("reason %q must name the unprotected path %q", got.Reason, want)
		}
	}
}

func TestCheckCodeowners_ADRWorkflowsDoubleStarNotationAlsoMatches(t *testing.T) {
	// The ADR text itself writes ".github/workflows/**" while the real file
	// uses "/.github/workflows/" — the check must tolerate either notation.
	content := `.github/workflows/** @chnu-kim
docs/adr/0004-*.md @chnu-kim
docs/adr/0007-*.md @chnu-kim
docs/adr/0008-*.md @chnu-kim
docs/adr/0009-*.md @chnu-kim
docs/adr/0010-*.md @chnu-kim
docs/adr/0011-*.md @chnu-kim
docs/adr/0012-*.md @chnu-kim
docs/adr/0013-*.md @chnu-kim
docs/adr/0014-*.md @chnu-kim
docs/adr/0015-*.md @chnu-kim
docs/runbooks/phase-b-entry.md @chnu-kim
scripts/** @chnu-kim
internal/gate/** @chnu-kim
cmd/verdict-gate/** @chnu-kim
configs/gate/** @chnu-kim
.claude/skills/opensource-maintainer/** @chnu-kim
.github/CODEOWNERS @chnu-kim
`
	got := CheckCodeowners(content)
	if !got.Satisfied {
		t.Fatalf("CheckCodeowners() = %+v, want Satisfied=true for ** notation", got)
	}
}

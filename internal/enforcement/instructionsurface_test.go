package enforcement

import (
	"os"
	"path/filepath"
	"testing"
)

// The instruction surfaces (always-loaded policy, worker procedures, orchestration
// skills) are protected in CODEOWNERS by DIRECTORY rules, but GitHub resolves
// ownership last-match-wins: a later, narrower ownerless entry can strip exactly one
// file inside a "protected" directory. sacredRequiredPaths therefore has to name every
// such file individually — the same rule internal/gate already follows.
//
// Sampling one representative file is not enough: a NEW agent or skill added later
// would sit outside the carve-out check while CheckCodeowners still passed. These
// tests fail when such a file is added without registering it (codex:review [P2] on
// PR #81).
func TestSacredRequiredPaths_CoversEveryAgentInstructionFile(t *testing.T) {
	assertGlobFullyRegistered(t, "../../.claude/agents", "*.md", ".claude/agents")
}

func TestSacredRequiredPaths_CoversEveryOrchestrationSkill(t *testing.T) {
	// Only the repo-local skills that gate or generate gate evidence are sacred.
	// NOTE: codex-pr-review also gates (it IS the review invocation) but lives in the
	// USER-GLOBAL ~/.claude/skills/, outside this repo — CODEOWNERS cannot protect it.
	// That trust-boundary gap is booked, not closed here.
	for _, dir := range []string{"dispatch-issue"} {
		assertGlobFullyRegistered(t,
			filepath.Join("../../.claude/skills", dir), "*.md",
			filepath.Join(".claude/skills", dir))
	}
}

// assertGlobFullyRegistered fails when a file matching pattern under diskDir is not
// present in sacredRequiredPaths under its repo-relative form.
func assertGlobFullyRegistered(t *testing.T, diskDir, pattern, repoDir string) {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(diskDir, pattern))
	if err != nil {
		t.Fatalf("glob %s/%s: %v", diskDir, pattern, err)
	}
	if len(matches) == 0 {
		// Fail closed: an empty glob means the directory moved or the test is
		// pointed at the wrong place, not that everything is registered.
		t.Fatalf("no files matched %s/%s — the check would pass vacuously", diskDir, pattern)
	}

	registered := make(map[string]bool, len(sacredRequiredPaths))
	for _, p := range sacredRequiredPaths {
		registered[p] = true
	}

	for _, m := range matches {
		if _, err := os.Stat(m); err != nil {
			t.Fatalf("stat %s: %v", m, err)
		}
		want := filepath.ToSlash(filepath.Join(repoDir, filepath.Base(m)))
		if !registered[want] {
			t.Errorf("%s is a protected instruction surface but is missing from sacredRequiredPaths "+
				"(add %q — a directory CODEOWNERS rule alone does not survive a later narrower "+
				"ownerless entry)", m, want)
		}
	}
}

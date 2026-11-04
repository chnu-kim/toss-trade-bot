package enforcement

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
//
// Completeness instead of enumeration (ADR-0016 point 12 / point 15(c)): the checks
// below do not hard-code the set of skills, agents or runbooks they expect. They
// enumerate the directories on disk and walk every regular file in each tree
// (filepath.WalkDir, any extension), so a file added later is RED until it is
// registered in sacredRequiredPaths — registration can be forgotten, but it cannot
// be forgotten silently.
func TestSacredRequiredPaths_CoversEveryAgentInstructionFile(t *testing.T) {
	assertGlobFullyRegistered(t, "../../.claude/agents", "*.md", ".claude/agents")
}

func TestSacredRequiredPaths_CoversEveryOrchestrationSkill(t *testing.T) {
	// Every repo-local skill is an instruction surface that gates or generates gate
	// evidence: dispatch-issue (orchestration: eligibility, risk stops, delegation,
	// PR handoff), opensource-maintainer (the pre-commit secret gate PROCEDURE —
	// protecting only its scripts lets a loop weaken the instruction to skip them;
	// PR #81 R4), and architect / issue-drafter / retro (ADR authoring, issue
	// drafting and the retro that promotes rules into durable surfaces — ADR-0016
	// point 15). Rather than naming them, enumerate .claude/skills/* and walk each
	// skill tree in full (ADR-0016 point 15(c)).
	// NOTE: codex-pr-review also gates (it IS the review invocation) but lives in the
	// user-global skills directory rather than this repo — CODEOWNERS cannot reach it.
	// That trust-boundary gap is booked, not closed here.
	const diskRoot = "../../.claude/skills"
	const repoRoot = ".claude/skills"

	entries, err := os.ReadDir(diskRoot)
	if err != nil {
		t.Fatalf("read %s: %v", diskRoot, err)
	}
	var skillDirs []string
	for _, e := range entries {
		if e.IsDir() {
			skillDirs = append(skillDirs, e.Name())
		}
	}
	if len(skillDirs) == 0 {
		// Fail closed: no skill directories means the tree moved or the test is
		// pointed at the wrong place, not that everything is registered.
		t.Fatalf("no skill directories under %s — the check would pass vacuously", diskRoot)
	}

	for _, dir := range skillDirs {
		assertTreeFullyRegistered(t,
			filepath.Join(diskRoot, dir),
			filepath.Join(repoRoot, dir),
			nil)
	}
}

// TestSacredRequiredPaths_CoversEveryClaudeTreeFile walks the whole .claude tree —
// agents, skills AND root-level files (ADR-0016 point 15(c), "root file walk") — so
// that adding .claude/settings.json, a hooks/ directory, a new agent or a new skill
// without registering it is structurally impossible: the file is RED until it
// appears in sacredRequiredPaths.
//
// Skipped on purpose: settings.local.json (per-machine, .gitignored) and anything
// under worktrees/ (Agent worktree isolation scratch space, .gitignored).
func TestSacredRequiredPaths_CoversEveryClaudeTreeFile(t *testing.T) {
	assertTreeFullyRegistered(t, "../../.claude", ".claude", func(rel string) bool {
		if filepath.Base(rel) == "settings.local.json" {
			return true
		}
		return rel == "worktrees" || strings.HasPrefix(rel, "worktrees/")
	})
}

// TestSacredRequiredPaths_CoversEveryRunbook walks docs/runbooks in full
// (ADR-0016 point 12): a runbook holds the *executable* steps an ADR governs by
// reference, so every file in the directory must be individually registered, not
// just the ones a reviewer remembered to list.
func TestSacredRequiredPaths_CoversEveryRunbook(t *testing.T) {
	assertTreeFullyRegistered(t, "../../docs/runbooks", "docs/runbooks", nil)
}

// assertTreeFullyRegistered walks diskRoot recursively and fails when any regular
// file (any extension) is not present in sacredRequiredPaths under its repo-relative
// form repoRoot/<rel>. skip, if non-nil, receives the slash-separated path relative
// to diskRoot; returning true for a directory prunes that subtree, returning true for
// a file leaves it unchecked. Zero regular files is a hard failure: an empty walk
// means the directory moved or the test is pointed at the wrong place, not that
// everything is registered.
func assertTreeFullyRegistered(t *testing.T, diskRoot, repoRoot string, skip func(rel string) bool) {
	t.Helper()

	registered := make(map[string]bool, len(sacredRequiredPaths))
	for _, p := range sacredRequiredPaths {
		registered[p] = true
	}

	seen := 0
	err := filepath.WalkDir(diskRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == diskRoot {
			return nil
		}
		rel, err := filepath.Rel(diskRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		seen++
		want := filepath.ToSlash(filepath.Join(repoRoot, rel))
		if !registered[want] {
			t.Errorf("%s is a protected instruction surface but is missing from sacredRequiredPaths "+
				"(add %q — a directory CODEOWNERS rule alone does not survive a later narrower "+
				"ownerless entry)", path, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", diskRoot, err)
	}
	if seen == 0 {
		t.Fatalf("no regular files under %s — the check would pass vacuously", diskRoot)
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

package enforcement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Harness contract checks: two rules that CLAUDE.md, the worker definition
// (.claude/agents/) and the orchestration skills (.claude/skills/*/SKILL.md)
// used to carry only as prose plus a worker-run self-grep.
//
// Why a Go test: on PR #81 the rule "codex review runs against origin/main"
// was written as an invariant while the concrete command further down the
// same file still pointed at a stale local base; the self-grep added to catch
// that drift quoted the forbidden literal in its own description, so it was
// permanently red, and it was pushed anyway because its output was never read
// (docs/retros/2026-07-19-loop-engineering-campaign.md, "축 1 — 결과물 품질 /
// 부족했던 것"). A rule, the command it governs and the check that guards it
// move in one change, and a check counts only once it has run green.
//
// Hence: the check lives in the sacred enforcement package (kept registered by
// TestSacredRequiredPaths_CoversEveryEnforcementGoFile); it never spells out
// the stale-base form — every condition is "a required string is absent", so
// the test cannot trip over its own source; and it fails closed (missing file
// or empty glob = failure). Like adrprotects.go it is a DRIFT DETECTOR, not an
// enforcer — green grants nothing.

const (
	harnessClaudeMD      = "../../CLAUDE.md"
	harnessWorkerFile    = "../../.claude/agents/go-tdd-implementer.md"
	harnessDispatchSkill = "../../.claude/skills/dispatch-issue/SKILL.md"
)

// harnessFiles returns CLAUDE.md plus every agent definition and skill entry
// point. An empty glob is a hard failure, not an empty check.
func harnessFiles(t *testing.T) []string {
	t.Helper()
	files := []string{harnessClaudeMD}
	for _, pattern := range []string{"../../.claude/agents/*.md", "../../.claude/skills/*/SKILL.md"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("no files matched %s — the check would pass vacuously", pattern)
		}
		files = append(files, matches...)
	}
	return files
}

func readHarnessLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(raw), "\n")
}

func countLinesContaining(t *testing.T, path string, needles ...string) int {
	t.Helper()
	n := 0
	for _, line := range readHarnessLines(t, path) {
		ok := true
		for _, needle := range needles {
			ok = ok && strings.Contains(line, needle)
		}
		if ok {
			n++
		}
	}
	return n
}

// TestHarnessReviewBaseIsOriginMain: in every harness file, a line naming the
// review skill (`codex-pr-review`) or passing a base (`--base`) must also name
// `origin/main` — the worktree's local main is stale (retro "축 2 — 프로세스 /
// 부족했던 것 / 마찰"). Two anchors stop the rule being satisfied by deletion: the
// worker must keep ≥1 line carrying both literals (otherwise the review
// invocation itself is gone — zero lines is how a gate is switched off without
// touching gate code), the dispatch skill must keep ≥1 line carrying both
// `git diff` and origin/main (the orchestrator-run advisory diff command — a
// bare origin/main mention elsewhere must not satisfy it), and CLAUDE.md must
// keep ≥1 line carrying both codex-pr-review and origin/main (the always-loaded
// REVIEWER ladder, which claims this test checks it).
func TestHarnessReviewBaseIsOriginMain(t *testing.T) {
	for _, path := range harnessFiles(t) {
		for i, line := range readHarnessLines(t, path) {
			if !strings.Contains(line, "codex-pr-review") && !strings.Contains(line, "--base") {
				continue
			}
			if !strings.Contains(line, "origin/main") {
				t.Errorf("%s:%d: review/base line must name origin/main: %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if countLinesContaining(t, harnessWorkerFile, "codex-pr-review", "origin/main") == 0 {
		t.Fatalf("%s: no line carries both codex-pr-review and origin/main — the review invocation is gone", harnessWorkerFile)
	}
	if countLinesContaining(t, harnessDispatchSkill, "git diff", "origin/main") == 0 {
		t.Fatalf("%s: no line carries both `git diff` and origin/main — the advisory review base is no longer fixed", harnessDispatchSkill)
	}
	if countLinesContaining(t, harnessClaudeMD, "codex-pr-review", "origin/main") == 0 {
		t.Fatalf("%s: no line carries both codex-pr-review and origin/main — the REVIEWER ladder is gone", harnessClaudeMD)
	}
}

// TestHarnessDocs_CarryW0Literal: the W0 window (no sacred merge path between
// enforce_admins re-enable and App-key provisioning, ADR-0016 point 5) must stay
// visible on every always-loaded surface the ADR-0016 twin table lists under
// "절차(상시 로드)": CLAUDE.md holds the prose, worker and dispatch skill a one-line
// pointer. ADR-0016's twin table says the phase-b-entry runbook's Step 0 will
// grep for the same key; that runbook change is still pending (#83 §D).
func TestHarnessDocs_CarryW0Literal(t *testing.T) {
	for _, path := range []string{harnessClaudeMD, harnessWorkerFile, harnessDispatchSkill} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "W0") {
			t.Errorf("%s: literal W0 missing — the sacred-merge-window pointer was dropped", path)
		}
	}
}

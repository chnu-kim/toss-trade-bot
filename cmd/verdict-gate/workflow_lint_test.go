package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestVerdictGateWorkflow_NoDirectInterpolationOfUntrustedGitHubContext is a
// regression guard for ADR-0011 point 4(e)(i)'s "instruction/data
// separation, env: only" principle: an independent adversarial review found
// one "Retry decision" step splicing github.event.inputs.action,
// github.event_name, and github.actor directly via `${{ }}` into a shell
// `run:` body instead of routing them through `env:` like every other guard
// in this file (e.g. "Guard — sender/actor allowlist"). `inputs.action` is
// declared `type: choice`, but GitHub does not enforce that constraint
// server-side for a workflow_dispatch fired via the REST API — a
// write-access actor could send an arbitrary string, which direct `${{ }}`
// interpolation would splice unescaped into this privileged (checks:write)
// job's shell script.
//
// This test does not parse YAML (no YAML dependency in go.mod, and the
// workflow's own convention is exactly the grep-able pattern below): for
// every line inside a step's `run: |` body, the three dangerous context
// expressions below must never appear directly — they must only ever be
// read from an already-declared shell variable (env: block), never spliced
// as `${{ ... }}` into the run body text itself.
func TestVerdictGateWorkflow_NoDirectInterpolationOfUntrustedGitHubContext(t *testing.T) {
	const workflowPath = "../../.github/workflows/verdict-gate.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}

	dangerous := []string{
		"github.event.inputs",
		"github.actor",
		"github.event_name",
		"github.event.client_payload",
		"github.event.sender",
	}

	runLineRe := regexp.MustCompile(`^\s*run:\s*\|`)

	lines := strings.Split(string(data), "\n")
	runIndent := -1
	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimRight(line, " \t")
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if runIndent >= 0 {
			// A line at or below the `run:` key's own indentation ends the
			// block scalar (YAML block-scalar semantics: content must be
			// indented deeper than the key introducing it).
			if strings.TrimSpace(trimmed) != "" && indent <= runIndent {
				runIndent = -1
			}
		}

		if runLineRe.MatchString(line) {
			runIndent = indent
			continue
		}

		if runIndent < 0 {
			continue // not inside a run: body — env: assignments and
			// workflow-level if:/on:/run-name: expressions are evaluated
			// by the Actions runner itself, not spliced into a shell
			// command, so they are out of scope for this check.
		}

		trimmedContent := strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmedContent, "#") {
			continue // comments (including ones documenting this very
			// guard) are not executed.
		}

		for _, pattern := range dangerous {
			if strings.Contains(trimmed, pattern) {
				t.Errorf("%s:%d: %q is directly interpolated inside a run: body via ${{ }} — must be routed through an env: block instead (ADR-0011 point 4(e)(i)). Line: %s",
					workflowPath, lineNo, pattern, trimmedContent)
			}
		}
	}
}

// TestVerdictGateWorkflow_RevalidatesHeadShaBeforeArtifactUploadAndCheckRunPublish
// is a regression guard for a codex:adversarial-review [high] finding: the
// "resolve" job records head_sha from one `gh pr view` call, then a later,
// separate step fetches the diff/changed-files for the PR's CURRENT head —
// if the PR's head moves in between (an eligible same-repo bot branch has
// push/force-push capability), the diff actually reviewed can silently
// diverge from the SHA the verdict-check ends up published against.
// SHA-sticky logic then treats that mis-associated check-run as an
// authoritative, reusable record forever, and if the branch is later reset
// back to exactly that SHA, `--match-head-commit` at merge time would
// succeed — merging content that was never actually reviewed as reviewed.
//
// This does not verify runtime correctness (that would need a live PR and a
// real race) — it is a structural lint ensuring the fix (re-fetch
// headRefOid and fail closed on mismatch) is present at both required
// checkpoints and cannot be silently deleted later: once right after the
// diff/files snapshot in "resolve" (before that data is uploaded as an
// artifact for the leg jobs), and once more immediately before "finalize"
// publishes the check-run (since the leg jobs in between can take long
// enough for the head to move again).
func TestVerdictGateWorkflow_RevalidatesHeadShaBeforeArtifactUploadAndCheckRunPublish(t *testing.T) {
	const workflowPath = "../../.github/workflows/verdict-gate.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	content := string(data)

	revalidationPattern := `current_head=$(gh pr view "$PR_NUMBER" --json headRefOid`
	count := strings.Count(content, revalidationPattern)
	if count < 2 {
		t.Fatalf("found %d occurrence(s) of the head-SHA revalidation pattern %q in %s, want at least 2 (once after the diff/files fetch in the resolve job, once immediately before finalize's check-run publish) — a stale-head race between recording head_sha and using it would let a mis-associated verdict get published for a SHA whose content was never actually reviewed",
			count, revalidationPattern, workflowPath)
	}

	// Every occurrence must be paired with an explicit mismatch check that
	// fails closed (exit 1) — not just fetched-and-ignored.
	mismatchCheck := `if [ "$current_head" != "$HEAD_SHA" ]; then`
	if strings.Count(content, mismatchCheck) < 2 {
		t.Errorf("the head-SHA revalidation pattern is present but not consistently paired with %q (fail-closed on mismatch) at least twice in %s", mismatchCheck, workflowPath)
	}
}

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
// Issue #54 update: these two revalidations are no longer the load-bearing
// defense for diff↔SHA correspondence — an A→B→A reset re-passes a
// fetch-then-check sequence, so the real fix is fetching the diff/files
// from an immutable compare source keyed to the recorded SHAs (see
// TestVerdictGateWorkflow_DiffFetchUsesImmutableCompareKeyedToRecordedSha
// and the A→B→A reproduction in workflow_diff_fetch_test.go). The two
// revalidations remain as defense in depth and early-exit economy (don't
// spend both legs' API budgets, or publish a check-run, for a SHA that is
// already superseded) and must still not be silently deleted.
//
// This does not verify runtime correctness (that would need a live PR and a
// real race) — it is a structural lint ensuring the recheck (re-fetch
// headRefOid and fail closed on mismatch) is present at both checkpoints:
// once right after the diff/files snapshot in "resolve" (before that data
// is uploaded as an artifact for the leg jobs), and once more immediately
// before "finalize" publishes the check-run (since the leg jobs in between
// can take long enough for the head to move again).
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

// TestVerdictGateWorkflow_ClaudeLegPassesJSONSchemaValueInline is the
// structural lint for issue #54 ①: the claude CLI's --json-schema flag
// takes the schema *value* (inline JSON), not a file path. Passing a path
// fails argument parsing before auth with "--json-schema is not valid
// JSON" (empirically reproduced against the real CLI: the path form exits
// 1 at parse time; the inline form reaches the API stage). With the path
// form, the critical-path N-of-2 Claude leg could never produce an outcome
// once Phase B activates — the gate would fail its own regime for every
// risk:critical / unmapped-path PR (ADR-0008 points 3–5).
//
// The codex leg is deliberately asymmetric: the codex CLI's
// --output-schema flag DOES take a file path and must stay that way.
func TestVerdictGateWorkflow_ClaudeLegPassesJSONSchemaValueInline(t *testing.T) {
	const workflowPath = "../../.github/workflows/verdict-gate.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	content := string(data)

	const pathForm = `--json-schema "$WORKDIR/schema.json"`
	if strings.Contains(content, pathForm) {
		t.Errorf("%s passes a file PATH to the claude CLI's --json-schema (%s) — the flag takes the schema value (inline JSON) and a path fails parsing pre-auth, so the Claude leg can never produce an outcome (issue #54 ①)", workflowPath, pathForm)
	}
	const inlineForm = `--json-schema "$(cat "$WORKDIR/schema.json")"`
	if got := strings.Count(content, inlineForm); got != 1 {
		t.Errorf("want exactly 1 occurrence of the inline schema-value form %s in %s (the Claude leg invocation), got %d", inlineForm, workflowPath, got)
	}
	const codexForm = `--output-schema "$WORKDIR/schema.json"`
	if got := strings.Count(content, codexForm); got != 1 {
		t.Errorf("want exactly 1 occurrence of %s in %s — the codex CLI's --output-schema is a *path* argument (separate CLI, separate contract) and must not be \"fixed\" to match the claude flag; got %d", codexForm, workflowPath, got)
	}
}

// TestVerdictGateWorkflow_DiffFetchUsesImmutableCompareKeyedToRecordedSha is
// the structural lint for issue #54 ②: PR-number-keyed diff/file reads
// (`gh pr diff`, the pulls/{n}/files endpoint) return the PR's CURRENT head
// at request time. A fetch-then-check sequence closes a simple A→B push but
// not A→B→A — the head can be B during the fetch window and back at A by
// the time the current_head==HEAD_SHA recheck runs, so the recheck passes
// while the fetched content describes B. The fix this test pins: fetch both
// the diff and the changed-file list from the compare API keyed to the
// immutable commit SHAs themselves ($base_sha...$HEAD_SHA), whose response
// is a pure function of the two SHAs — no branch movement at any moment can
// change what it returns. The runtime behavior (fetched content follows
// HEAD_SHA, not the moving branch) is separately reproduced in
// workflow_diff_fetch_test.go by executing the real step body against a
// fake gh that simulates the A→B→A race.
func TestVerdictGateWorkflow_DiffFetchUsesImmutableCompareKeyedToRecordedSha(t *testing.T) {
	const workflowPath = "../../.github/workflows/verdict-gate.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	content := string(data)

	// (a) No PR-number-keyed mutable diff/file reads on any non-comment
	// line. (Comments may mention them when documenting this very fix.)
	forbidden := []string{"gh pr diff", "/pulls/$PR_NUMBER/files"}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, pattern := range forbidden {
			if strings.Contains(line, pattern) {
				t.Errorf("%s:%d: PR-number-keyed mutable read %q — this reads the PR's *current* head, not the recorded head SHA, and reopens the A→B→A fetch-window race (issue #54 ②). Fetch from the immutable compare source instead. Line: %s",
					workflowPath, i+1, pattern, trimmed)
			}
		}
	}

	// (b) The immutable compare source, keyed to the recorded SHAs, must be
	// what both the diff fetch and the file-list fetch use.
	const compareKey = `"repos/$REPO/compare/$base_sha...$HEAD_SHA"`
	if got := strings.Count(content, compareKey); got < 2 {
		t.Errorf("want at least 2 occurrences of the SHA-keyed immutable compare source %s in %s (diff fetch + changed-file-list fetch), got %d", compareKey, workflowPath, got)
	}
	if !strings.Contains(content, "Accept: application/vnd.github.diff") {
		t.Errorf("%s must fetch the raw diff via the compare endpoint's diff media type (Accept: application/vnd.github.diff) — otherwise pr.diff is not coming from the immutable SHA-keyed source", workflowPath)
	}
	// (c) The compare API caps the files array at 300 entries (first page
	// only; no SHA-immutable paginated alternative exists). A silently
	// truncated list could hide a critical path from risk classification
	// (ADR-0008 point 5), so the step must fail closed at the cap.
	if !strings.Contains(content, `-ge 300 ]`) {
		t.Errorf("%s must fail closed when the compare file list reaches the 300-entry platform cap (possible truncation ⇒ possible unclassified critical path) — the -ge 300 guard is missing", workflowPath)
	}
}

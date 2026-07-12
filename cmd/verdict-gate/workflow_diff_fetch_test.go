package main

// A→B→A head-race reproduction for issue #54 ② (ADR-0011 point 4 (e)(iv)'s
// premise: the diff a verdict is produced from and the SHA that verdict is
// published against must be the same content).
//
// These tests do not call the real GitHub API (repo test policy: no live
// API calls). Instead they execute the *actual* shell body of the
// verdict-gate workflow's "Fetch diff + changed paths" step — extracted
// verbatim from the YAML, so the code under test is the code that runs in
// CI, not a re-implementation — against a fake `gh` binary that simulates
// GitHub during the worst-case A→B→A window:
//
//   - PR-number-keyed reads (`gh pr diff`, pulls/{n}/files) see head B —
//     the branch was force-pushed to B during the fetch window;
//   - the post-fetch `gh pr view --json headRefOid` recheck sees head A —
//     the branch was reset back to A before the recheck, which is exactly
//     why a fetch-then-check sequence passes while having fetched B;
//   - compare reads keyed to immutable commit SHAs always return the
//     content of the SHAs in the URL, like the real API (a compare
//     response is a pure function of the two SHAs).
//
// The step passes the race iff the diff/changed-paths it records correspond
// to the recorded HEAD_SHA (A) — never to the moving branch (B).

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	fetchStepNameMarker = "- name: Fetch diff + changed paths"

	raceHeadSHA = "1111111111111111111111111111111111111111" // A — recorded by the meta step
	raceBaseSHA = "9999999999999999999999999999999999999999" // base branch tip
)

// fakeGH simulates GitHub mid-A→B→A-race. Mutable PR-number-keyed reads
// return B content and log themselves to $FAKE_GH_STATE/mutable-reads.log;
// SHA-keyed compare reads return the content of the requested SHA;
// the head recheck reports A (branch already reset back).
const fakeGH = `#!/bin/sh
args="$*"
count="${FAKE_FILES_COUNT:-2}"
case "$args" in
  *"pr diff"*)
    printf '%s\n' "$args" >> "$FAKE_GH_STATE/mutable-reads.log"
    printf 'diff --git a/b-file.go b/b-file.go\nMARKER-DIFF-FOR-B\n'
    ;;
  *"/pulls/"*)
    printf '%s\n' "$args" >> "$FAKE_GH_STATE/mutable-reads.log"
    printf 'b-file.go\n'
    ;;
  *"/commits/main"*)
    printf '9999999999999999999999999999999999999999\n'
    ;;
  *"/compare/9999999999999999999999999999999999999999...1111111111111111111111111111111111111111"*)
    case "$args" in
      *"application/vnd.github.diff"*)
        printf 'diff --git a/a-file.go b/a-file.go\nMARKER-DIFF-FOR-A\n' ;;
      *".files | length"*)
        printf '%s\n' "$count" ;;
      *".files[]"*)
        if [ "$count" != "0" ]; then printf 'a-file.go\na-renamed-old-path.go\n'; fi ;;
      *)
        echo "fake gh: unhandled compare invocation: $args" >&2
        exit 64 ;;
    esac
    ;;
  *"/compare/"*)
    echo "fake gh: compare keyed to unexpected SHAs (must be the recorded base tip + recorded head SHA): $args" >&2
    exit 64
    ;;
  *"pr view"*)
    printf '1111111111111111111111111111111111111111\n'
    ;;
  *)
    echo "fake gh: unexpected invocation: $args" >&2
    exit 64
    ;;
esac
`

// extractFetchStepRunBlock returns the dedented shell body of the workflow's
// "Fetch diff + changed paths" step, verbatim. It fails the test if the body
// still contains `${{ }}` interpolation — every value the step needs must be
// routed through env: (which also keeps the body executable here as-is).
func extractFetchStepRunBlock(t *testing.T) string {
	t.Helper()
	const workflowPath = "../../.github/workflows/verdict-gate.yml"
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("reading %s: %v", workflowPath, err)
	}
	lines := strings.Split(string(data), "\n")

	stepIdx := -1
	for i, line := range lines {
		if strings.Contains(line, fetchStepNameMarker) && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			stepIdx = i
			break
		}
	}
	if stepIdx < 0 {
		t.Fatalf("step %q not found in %s", fetchStepNameMarker, workflowPath)
	}

	runRe := regexp.MustCompile(`^(\s*)run:\s*\|`)
	runIdx, runIndent := -1, 0
	for i := stepIdx + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "- name:") {
			break
		}
		if m := runRe.FindStringSubmatch(lines[i]); m != nil {
			runIdx, runIndent = i, len(m[1])
			break
		}
	}
	if runIdx < 0 {
		t.Fatalf("no run: | block found for step %q in %s", fetchStepNameMarker, workflowPath)
	}

	var body []string
	contentIndent := -1
	for i := runIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			body = append(body, "")
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= runIndent {
			break
		}
		if contentIndent < 0 {
			contentIndent = indent
		}
		if indent < contentIndent {
			t.Fatalf("%s:%d: unexpected dedent inside the run block", workflowPath, i+1)
		}
		body = append(body, line[contentIndent:])
	}
	script := strings.Join(body, "\n")
	if script == "" {
		t.Fatalf("extracted an empty run block for step %q", fetchStepNameMarker)
	}
	if strings.Contains(script, "${{") {
		t.Fatalf("the %q run body still contains ${{ }} interpolation — route every value through env: so the body stays executable shell (and this reproduction test can run it verbatim):\n%s", fetchStepNameMarker, script)
	}
	return script
}

// runFetchStep executes the extracted step body in an isolated workspace
// with the fake gh on PATH, returning the exit error (nil on success), the
// workspace dir (where pr.diff / changed-paths.txt land), the fake-state
// dir, and combined output.
func runFetchStep(t *testing.T, filesCount string) (runErr error, workDir, stateDir, output string) {
	t.Helper()
	script := extractFetchStepRunBlock(t)

	binDir := t.TempDir()
	workDir = t.TempDir()
	stateDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatalf("writing fake gh: %v", err)
	}

	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_TOKEN=fake-token-never-used",
		"REPO=x-owner/x-repo",
		"PR_NUMBER=7",
		"HEAD_SHA="+raceHeadSHA,
		"BASE_REF=main",
		"FAKE_GH_STATE="+stateDir,
		"FAKE_FILES_COUNT="+filesCount,
	)
	out, err := cmd.CombinedOutput()
	return err, workDir, stateDir, string(out)
}

// TestVerdictGateWorkflow_DiffFetchStep_ABAHeadRace_ReviewsRecordedHeadShaContent
// is the A→B→A reproduction proper: with the branch at B during the fetch
// window and back at A for the recheck (so any fetch-then-check sequence
// passes), the step must still record A's diff and A's changed paths —
// i.e. the content of the SHA the verdict will be published against, not
// the content the moving branch happened to point at.
func TestVerdictGateWorkflow_DiffFetchStep_ABAHeadRace_ReviewsRecordedHeadShaContent(t *testing.T) {
	runErr, workDir, stateDir, output := runFetchStep(t, "2")
	if runErr != nil {
		t.Fatalf("step body failed under the A→B→A simulation (it must succeed by fetching immutable SHA-keyed content): %v\noutput:\n%s", runErr, output)
	}

	if log, err := os.ReadFile(filepath.Join(stateDir, "mutable-reads.log")); err == nil {
		t.Errorf("the step performed PR-number-keyed mutable diff/file reads during the race window — these fetched head B's content while the verdict publishes against A (issue #54 ②):\n%s", log)
	}

	prDiff, err := os.ReadFile(filepath.Join(workDir, "pr.diff"))
	if err != nil {
		t.Fatalf("step did not produce pr.diff: %v\noutput:\n%s", err, output)
	}
	if !strings.Contains(string(prDiff), "MARKER-DIFF-FOR-A") || strings.Contains(string(prDiff), "MARKER-DIFF-FOR-B") {
		t.Errorf("pr.diff must contain the recorded head SHA (A)'s content and never the mid-race branch head (B)'s — a verdict produced from B's diff would be published against A, and SHA-sticky logic + merge-time --match-head-commit (both keyed to A) would honor it forever. Got:\n%s", prDiff)
	}

	changed, err := os.ReadFile(filepath.Join(workDir, "changed-paths.txt"))
	if err != nil {
		t.Fatalf("step did not produce changed-paths.txt: %v\noutput:\n%s", err, output)
	}
	want := "a-file.go\na-renamed-old-path.go\n"
	if string(changed) != want {
		t.Errorf("changed-paths.txt must list A's files (including a rename's previous_filename), got:\n%q\nwant:\n%q", changed, want)
	}
}

// TestVerdictGateWorkflow_DiffFetchStep_FailsClosedAtCompareFileCap pins the
// 300-file fail-closed guard: the compare API returns at most 300 files
// (first page only; there is no SHA-immutable paginated alternative), so a
// file list at the cap may be silently truncated and could hide a critical
// path from risk classification (ADR-0008 point 5). At the cap the step
// must fail — no verdict at all instead of a verdict on a partial list.
func TestVerdictGateWorkflow_DiffFetchStep_FailsClosedAtCompareFileCap(t *testing.T) {
	runErr, _, _, output := runFetchStep(t, "300")
	if runErr == nil {
		t.Fatalf("step succeeded with a compare file list at the 300-entry platform cap — it must fail closed on possible truncation (a hidden critical path would skip the N-of-2 regime). Output:\n%s", output)
	}
}

// TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnEmptyFileList pins the
// pre-existing zero-changed-files guard across the #54 rewrite: an empty
// file list for a real PR most likely means the API call failed silently,
// and must remain fail-closed rather than flowing into risk classification.
func TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnEmptyFileList(t *testing.T) {
	runErr, _, _, output := runFetchStep(t, "0")
	if runErr == nil {
		t.Fatalf("step succeeded with an empty changed-file list — it must fail closed (silent API failure indistinguishable from a genuinely empty PR). Output:\n%s", output)
	}
}

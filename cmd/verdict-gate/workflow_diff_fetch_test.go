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
// SHA-keyed compare reads return the content of the requested SHAs; the
// head recheck reports A (branch already reset back); the default-branch
// read reports $FAKE_DEFAULT_BRANCH.
//
// Crucially (issue #54 additional ③), the compare *files* read returns raw
// JSON and runs the workflow's OWN --jq expression against it via the real
// jq binary — the fake does not hand back a pre-projected path list. So if
// the workflow's jq stops extracting `previous_filename`, the #52
// rename-path-hiding guard regresses and this reproduction goes RED instead
// of false-green. The raw files JSON deliberately includes a renamed file
// (filename + previous_filename) so previous_filename extraction is
// observable in the output.
const fakeGH = `#!/bin/sh
# Capture the workflow's own --jq expression (if any) and run it, via the
# real jq, against raw JSON — so a jq regression in the workflow is caught.
jq_expr=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--jq" ]; then jq_expr="$a"; fi
  prev="$a"
done
args="$*"
count="${FAKE_FILES_COUNT:-2}"

emit() { # $1 = raw JSON; apply the workflow's own --jq if it passed one
  if [ -n "$jq_expr" ]; then printf '%s' "$1" | jq -r "$jq_expr"; else printf '%s' "$1"; fi
}

case "$count" in
  0) files_raw='{"files":[]}' ;;
  2) files_raw='{"files":[{"filename":"a-file.go"},{"filename":"a-new-path.go","previous_filename":"a-renamed-old-path.go"}]}' ;;
  *) files_raw=$(jq -nc --argjson n "$count" '{files: [range($n) | {filename: ("f\(.).go")}]}') ;;
esac

case "$args" in
  *"/compare/9999999999999999999999999999999999999999...1111111111111111111111111111111111111111"*)
    case "$args" in
      *"application/vnd.github.diff"*)
        printf 'diff --git a/a-file.go b/a-file.go\nMARKER-DIFF-FOR-A\n' ;;
      *)
        emit "$files_raw" ;;
    esac
    ;;
  *"/compare/"*)
    echo "fake gh: compare keyed to unexpected SHAs (must be the recorded base tip 9999... + recorded head 1111...): $args" >&2
    exit 64
    ;;
  *".default_branch"*)
    printf '%s\n' "${FAKE_DEFAULT_BRANCH:-main}"
    ;;
  *"/commits/"*)
    emit '{"sha":"9999999999999999999999999999999999999999"}'
    ;;
  *"pr view"*)
    printf '1111111111111111111111111111111111111111\n'
    ;;
  *"pr diff"*)
    printf '%s\n' "$args" >> "$FAKE_GH_STATE/mutable-reads.log"
    printf 'diff --git a/b-file.go b/b-file.go\nMARKER-DIFF-FOR-B\n'
    ;;
  *"/pulls/"*)
    printf '%s\n' "$args" >> "$FAKE_GH_STATE/mutable-reads.log"
    printf 'b-file.go\n'
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

// fetchStepParams configures one execution of the extracted step body.
type fetchStepParams struct {
	filesCount    string // FAKE_FILES_COUNT
	baseRef       string // BASE_REF (PR base branch name)
	defaultBranch string // FAKE_DEFAULT_BRANCH (what the repo reports)
}

// defaultFetchParams is the happy path: 2 files, base ref == default branch.
func defaultFetchParams() fetchStepParams {
	return fetchStepParams{filesCount: "2", baseRef: "main", defaultBranch: "main"}
}

// runFetchStep executes the extracted step body in an isolated workspace
// with the fake gh on PATH, returning the exit error (nil on success), the
// workspace dir (where pr.diff / changed-paths.txt land), the fake-state
// dir, and combined output.
func runFetchStep(t *testing.T, p fetchStepParams) (runErr error, workDir, stateDir, output string) {
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
		"BASE_REF="+p.baseRef,
		"FAKE_GH_STATE="+stateDir,
		"FAKE_FILES_COUNT="+p.filesCount,
		"FAKE_DEFAULT_BRANCH="+p.defaultBranch,
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
	runErr, workDir, stateDir, output := runFetchStep(t, defaultFetchParams())
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
	// Produced by the workflow's OWN jq (.files[] | .filename,
	// (.previous_filename // empty)) run against the fake's raw files JSON:
	// a-file.go (no rename), then a-new-path.go + its previous name
	// a-renamed-old-path.go. The renamed file's OLD path being present is
	// the #52 path-hiding guard — if the workflow jq stops extracting
	// previous_filename, a-renamed-old-path.go vanishes and this assertion
	// fails (no longer false-green — issue #54 additional ③).
	want := "a-file.go\na-new-path.go\na-renamed-old-path.go\n"
	if string(changed) != want {
		t.Errorf("changed-paths.txt must list A's files including a rename's previous_filename (#52 path-hiding guard), got:\n%q\nwant:\n%q", changed, want)
	}
	if !strings.Contains(string(changed), "a-renamed-old-path.go") {
		t.Errorf("the renamed file's OLD path (previous_filename) is missing from changed-paths.txt — the workflow's jq is not extracting previous_filename, so a rename that moves a critical path into a non-critical one would evade risk classification (ADR-0008 point 5 / #52). Got:\n%q", changed)
	}
}

// TestVerdictGateWorkflow_DiffFetchStep_FailsClosedAtCompareFileCap pins the
// 300-file fail-closed guard: the compare API returns at most 300 files
// (first page only; there is no SHA-immutable paginated alternative), so a
// file list at the cap may be silently truncated and could hide a critical
// path from risk classification (ADR-0008 point 5). At the cap the step
// must fail — no verdict at all instead of a verdict on a partial list.
func TestVerdictGateWorkflow_DiffFetchStep_FailsClosedAtCompareFileCap(t *testing.T) {
	p := defaultFetchParams()
	p.filesCount = "300"
	runErr, _, _, output := runFetchStep(t, p)
	if runErr == nil {
		t.Fatalf("step succeeded with a compare file list at the 300-entry platform cap — it must fail closed on possible truncation (a hidden critical path would skip the N-of-2 regime). Output:\n%s", output)
	}
}

// TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnNonDefaultBaseRef is the
// reproduction for issue #54 additional ①: fix ②'s compare source added
// base_ref as a mutable input to the risk-classification boundary. A base
// ref that is not the repo default branch would make the three-dot compare
// measure against the wrong base, producing a changed-paths set that is not
// this PR's real contribution — the step must fail closed before fetching
// (no verdict = merge blocked) rather than classify a mis-based path list.
func TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnNonDefaultBaseRef(t *testing.T) {
	p := defaultFetchParams()
	p.baseRef = "release/1.0" // valid branch, but not the default branch
	p.defaultBranch = "main"
	runErr, workDir, _, output := runFetchStep(t, p)
	if runErr == nil {
		t.Fatalf("step succeeded with base_ref (%q) != default_branch (%q) — it must fail closed, since the compare and thus risk classification would be measured against a non-default base (issue #54 ①). Output:\n%s", p.baseRef, p.defaultBranch, output)
	}
	// It must fail closed BEFORE producing any diff/paths for classification.
	if _, err := os.ReadFile(filepath.Join(workDir, "changed-paths.txt")); err == nil {
		t.Errorf("changed-paths.txt was produced for a non-default base ref — the guard must reject before any mis-based path list reaches classification")
	}
}

// TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnEmptyFileList pins the
// pre-existing zero-changed-files guard across the #54 rewrite: an empty
// file list for a real PR most likely means the API call failed silently,
// and must remain fail-closed rather than flowing into risk classification.
func TestVerdictGateWorkflow_DiffFetchStep_FailsClosedOnEmptyFileList(t *testing.T) {
	p := defaultFetchParams()
	p.filesCount = "0"
	runErr, _, _, output := runFetchStep(t, p)
	if runErr == nil {
		t.Fatalf("step succeeded with an empty changed-file list — it must fail closed (silent API failure indistinguishable from a genuinely empty PR). Output:\n%s", output)
	}
}

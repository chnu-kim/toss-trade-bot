// Command presence-check runs the ADR-0009 point 8 presence-check: it answers,
// mechanically, whether the loop-engineering enforcement layer (CODEOWNERS
// coverage, branch protection, PR-authoring identity) is actually standing
// before any orchestration skill starts autonomous work.
//
// Check (c) follows the ADR-0011 point 10 redefinition: instead of proving
// possession of any credential, it observes — read-only — that (c-1) the
// PR-creation workflow exists on the protected branch and (c-2) a recent
// loop-created PR is genuinely authored by the App bot identity. No
// GITHUB_APP_* environment variable is read here, deliberately: App
// credentials must not exist outside CI (ADR-0011 point 1), and key
// possession is not authorship evidence (point 10).
//
// main stays thin: load configuration from the environment, wire the three
// checks (internal/enforcement owns all judgement logic), run them, and
// report the verdict as a structured log line plus a machine-parseable exit
// code: 0 = satisfied (enforcement layer verified live), 1 = fail-closed
// (unsatisfied, misconfigured, or unverifiable — callers must fall back to
// the existing human-gate pipeline).
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/chnu-kim/toss-trade-bot/internal/enforcement"
	"github.com/chnu-kim/toss-trade-bot/internal/runtime"
)

const (
	defaultBranch         = "main"
	defaultCodeownersPath = ".github/CODEOWNERS"
	defaultExpectedActor  = "mechanu[bot]"
	// defaultPRCreationWorkflowPath is the workflow file check (c-1) verifies
	// on the protected branch — the filename issue #47 fixed (merged in PR
	// #51); this command does not re-decide it, only lets the standard
	// PRESENCE_CHECK_* env pattern override it for forks/tests.
	defaultPRCreationWorkflowPath = ".github/workflows/pr-creation.yml"
)

func main() {
	logger := runtime.NewLogger(os.Stdout)
	ctx := context.Background()

	owner, repo, err := ownerRepo()
	if err != nil {
		logger.Error("presence-check misconfigured: fail-closed", "err", err)
		os.Exit(1)
	}

	branch := getenv("PRESENCE_CHECK_BRANCH", defaultBranch)
	branchChecker := enforcement.NewGitHubClient(branchProtectionToken())

	// Plain-read client for checks (a), (c-1) and (c-2): Contents: read +
	// Pull requests: read are enough (ADR-0011 point 10 — c-1/c-2 must stay
	// within the post-narrowing PAT spec, point 5 ②). Kept separate from the
	// branch-protection client so the Administration: read credential is
	// used only where it is actually needed.
	readClient := enforcement.NewGitHubClient(readToken())

	// CODEOWNERS is read from the protected branch via the GitHub API — not
	// local disk. GitHub evaluates CODEOWNERS (and everything branch
	// protection cares about) from the target branch's committed content;
	// reading local disk here would let a feature branch that edits
	// CODEOWNERS, or a dirty checkout, silently report "protected" while main
	// itself is not (codex GitHub-native review finding on this PR).
	codeownersPath := getenv("PRESENCE_CHECK_CODEOWNERS_PATH", defaultCodeownersPath)
	content, err := readClient.FetchFileContent(ctx, owner, repo, codeownersPath, branch)
	if err != nil {
		// A missing/unfetchable CODEOWNERS is itself the unmet condition for
		// check (a) — pass an empty string through so Run reports that
		// specific, correct reason instead of us guessing here.
		logger.Warn("could not fetch CODEOWNERS from the protected branch, check (a) will fail-closed",
			"path", codeownersPath, "branch", branch, "err", err)
		content = ""
	}

	result := enforcement.Run(ctx, enforcement.Params{
		CodeownersContent:      content,
		Owner:                  owner,
		Repo:                   repo,
		Branch:                 branch,
		BranchChecker:          branchChecker,
		WorkflowFetcher:        readClient,
		PRCreationWorkflowPath: getenv("PRESENCE_CHECK_PR_CREATION_WORKFLOW_PATH", defaultPRCreationWorkflowPath),
		PRLister:               readClient,
		RevisionFetcher:        readClient,
		ExpectedActor:          getenv("PRESENCE_CHECK_EXPECTED_ACTOR", defaultExpectedActor),
	})

	logResult(logger, result)

	if !result.Satisfied {
		os.Exit(1)
	}
}

// ownerRepo reads GITHUB_REPOSITORY ("owner/repo", the GitHub Actions
// convention) and splits it. It is the only genuinely required piece of
// config — without it we cannot even ask GitHub about branch protection.
func ownerRepo() (owner, repo string, err error) {
	v := os.Getenv("GITHUB_REPOSITORY")
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New(`GITHUB_REPOSITORY must be set as "owner/repo"`)
	}
	return parts[0], parts[1], nil
}

// branchProtectionToken picks the credential for check (b). Reading branch
// protection requires fine-grained **Administration: read** — read-only is
// sufficient, a full "admin token" is NOT required (ADR-0011 point 10·point
// 5 ② implementation note): the post-narrowing loop PAT includes
// Administration: read precisely so check (b) keeps passing after the
// classic admin token is retired. GITHUB_ADMIN_TOKEN remains as an optional
// override for setups whose primary token lacks that permission; otherwise
// the regular GITHUB_TOKEN (the loop PAT) is expected to carry it.
func branchProtectionToken() string {
	if v := os.Getenv("GITHUB_ADMIN_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("GITHUB_TOKEN")
}

// readToken picks the credential for the plain read-only checks ((a) content
// fetch, (c-1) workflow fetch, (c-2) PR list): the regular GITHUB_TOKEN
// first — Contents: read + Pull requests: read suffice — falling back to
// GITHUB_ADMIN_TOKEN only so a setup configured with just that one token
// keeps working (a read with it grants nothing extra). Never a GITHUB_APP_*
// credential: those must not exist in this context at all (ADR-0011 point 1).
func readToken() string {
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("GITHUB_ADMIN_TOKEN")
}

// logResult writes the presence-check verdict as a single structured log
// line — this is the entire post-mortem surface for an unattended caller that
// only looks at the exit code.
func logResult(logger *slog.Logger, result enforcement.Result) {
	summary := make(map[string]bool, len(result.Checks))
	for _, c := range result.Checks {
		summary[c.Name] = c.Satisfied
	}

	if result.Satisfied {
		logger.Info("presence-check satisfied: enforcement layer verified live", "checks", summary)
		return
	}
	logger.Error("presence-check fail-closed: existing human-gate pipeline remains authoritative",
		"checks", summary,
		"reasons", result.Reasons(),
	)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

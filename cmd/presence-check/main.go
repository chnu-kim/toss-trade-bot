// Command presence-check runs the ADR-0009 point 8 presence-check: it answers,
// mechanically, whether the loop-engineering enforcement layer (CODEOWNERS
// coverage, branch protection, App identity) is actually standing before any
// orchestration skill starts autonomous work.
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

	// CODEOWNERS is read from the protected branch via the GitHub API — not
	// local disk. GitHub evaluates CODEOWNERS (and everything branch
	// protection cares about) from the target branch's committed content;
	// reading local disk here would let a feature branch that edits
	// CODEOWNERS, or a dirty checkout, silently report "protected" while main
	// itself is not (codex GitHub-native review finding on this PR).
	codeownersPath := getenv("PRESENCE_CHECK_CODEOWNERS_PATH", defaultCodeownersPath)
	content, err := branchChecker.FetchFileContent(ctx, owner, repo, codeownersPath, branch)
	if err != nil {
		// A missing/unfetchable CODEOWNERS is itself the unmet condition for
		// check (a) — pass an empty string through so Run reports that
		// specific, correct reason instead of us guessing here.
		logger.Warn("could not fetch CODEOWNERS from the protected branch, check (a) will fail-closed",
			"path", codeownersPath, "branch", branch, "err", err)
		content = ""
	}

	resolver, identityWarning := identityResolver()
	if identityWarning != "" {
		logger.Warn(identityWarning)
	}

	result := enforcement.Run(ctx, enforcement.Params{
		CodeownersContent: content,
		Owner:             owner,
		Repo:              repo,
		Branch:            branch,
		BranchChecker:     branchChecker,
		IdentityResolver:  resolver,
		ExpectedActor:     getenv("PRESENCE_CHECK_EXPECTED_ACTOR", defaultExpectedActor),
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
// protection requires admin-level read access, which the App deliberately
// lacks (ADR-0009 point 6) — so this intentionally prefers a
// separately-configured admin token over the App/PAT token used for identity,
// falling back to GITHUB_TOKEN only if no admin-specific token is set.
func branchProtectionToken() string {
	if v := os.Getenv("GITHUB_ADMIN_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("GITHUB_TOKEN")
}

// identityResolver wires check (c)'s ActorResolver. ADR-0011 point 10
// withdrew the previous wiring (App-JWT GET /app, with a PAT fallback): key
// possession proves nothing about which identity authors PRs — the probe
// passed while every loop PR was still authored by the human account
// (semantic false positive, empirically demonstrated; codex
// adversarial-review finding on PR #45). Until the c-1/c-2 redefinition
// (PR-creation workflow existence on main + actual recent loop-PR author)
// lands in ADR-0011's follow-up issue, check (c) is hard fail-closed no
// matter what credentials the environment carries — GITHUB_APP_ID /
// GITHUB_APP_PRIVATE_KEY(_PATH) are deliberately no longer read here.
func identityResolver() (enforcement.ActorResolver, string) {
	return enforcement.WithdrawnActorResolver{}, "check (c) is hard fail-closed pending the ADR-0011 c-1/c-2 redefinition — configured App/PAT credentials are intentionally ignored (key possession is not authorship evidence)"
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

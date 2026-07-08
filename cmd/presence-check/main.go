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

	codeownersPath := getenv("PRESENCE_CHECK_CODEOWNERS_PATH", defaultCodeownersPath)
	content, err := os.ReadFile(codeownersPath)
	if err != nil {
		// A missing/unreadable CODEOWNERS is itself the unmet condition for
		// check (a) — pass an empty string through so Run reports that
		// specific, correct reason instead of us guessing here.
		logger.Warn("could not read CODEOWNERS file, check (a) will fail-closed", "path", codeownersPath, "err", err)
	}

	resolver, identityWarning := identityResolver()
	if identityWarning != "" {
		logger.Warn(identityWarning)
	}

	result := enforcement.Run(ctx, enforcement.Params{
		CodeownersContent: string(content),
		Owner:             owner,
		Repo:              repo,
		Branch:            getenv("PRESENCE_CHECK_BRANCH", defaultBranch),
		BranchChecker:     enforcement.NewGitHubClient(branchProtectionToken()),
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

// identityResolver wires check (c)'s ActorResolver. It prefers the GitHub
// App's own credentials (GITHUB_APP_ID + a private key) so the check proves
// the App identity has actually taken over; if App credentials aren't
// configured it falls back to a PAT-based resolver against GITHUB_TOKEN,
// which correctly reports "chnu-kim" (or whatever human account owns that
// token) and therefore correctly fails the check until the App is wired in.
// If neither is configured, it returns a nil resolver — Run already
// fail-closes on that.
func identityResolver() (enforcement.ActorResolver, string) {
	appID := os.Getenv("GITHUB_APP_ID")
	keyPEM, keyErr := appPrivateKeyPEM()
	if appID != "" && keyErr == nil && len(keyPEM) > 0 {
		resolver, err := enforcement.NewAppActorResolverFromPEM(appID, keyPEM)
		if err == nil {
			return resolver, ""
		}
		return nil, "GITHUB_APP_ID/private key configured but invalid, falling back: " + err.Error()
	}

	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return enforcement.NewPATActorResolver(token), "no GitHub App credentials configured; using PAT identity resolver (will report the human actor, correctly failing check (c) until the App is wired in)"
	}

	return nil, "no identity credentials configured (neither GITHUB_APP_ID+private key nor GITHUB_TOKEN); check (c) will fail-closed"
}

// appPrivateKeyPEM reads the App's private key from a file path
// (GITHUB_APP_PRIVATE_KEY_PATH) or inline PEM content (GITHUB_APP_PRIVATE_KEY,
// with escaped "\n" sequences un-escaped — the common way to fit a multi-line
// PEM into a single-line env var).
func appPrivateKeyPEM() ([]byte, error) {
	if path := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"); path != "" {
		return os.ReadFile(path)
	}
	if raw := os.Getenv("GITHUB_APP_PRIVATE_KEY"); raw != "" {
		return []byte(strings.ReplaceAll(raw, `\n`, "\n")), nil
	}
	return nil, nil
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

package enforcement

import (
	"context"
	"fmt"
	"runtime/debug"
)

// Params wires everything Run needs to answer the three ADR-0009 point 8
// pillars. BranchChecker, WorkflowFetcher and AuthorLister are interfaces so
// callers (and tests) can inject fakes; cmd/presence-check is responsible for
// wiring real implementations with the right credentials. Note (b) may need a
// different credential than (a)/(c): reading branch protection requires
// Administration: read (fine-grained), which plain content/PR read tokens
// lack — while (a) and both (c) legs are ordinary Contents/Pull-requests
// reads (ADR-0011 point 10: c-1/c-2 are verifiable with a plain read token;
// no App credential is involved anywhere).
type Params struct {
	// (a) — raw content of .github/CODEOWNERS.
	CodeownersContent string

	// Shared lookup target for (b) and (c). Branch is the protected branch
	// (b) queries protection for and (c-1) reads the workflow from.
	Owner, Repo, Branch string

	// (b) — branch protection checker.
	BranchChecker BranchProtectionChecker

	// (c-1) — PR-creation workflow existence on the protected branch.
	WorkflowFetcher        FileContentFetcher
	PRCreationWorkflowPath string

	// (c-2) — recent loop-PR author must be ExpectedActor (e.g.
	// "mechanu[bot]").
	AuthorLister  PullRequestAuthorLister
	ExpectedActor string
}

// Run evaluates all three ADR-0009 point 8 presence-check pillars and
// aggregates them fail-closed: Result.Satisfied is true only if every pillar
// independently reports Satisfied=true. Each pillar runs behind its own
// panic-recover boundary — a bug in one check must never crash the process or
// (worse) leave the overall verdict undetermined; it degrades to that pillar
// reporting unmet, which still collapses the whole Result to false.
func Run(ctx context.Context, p Params) Result {
	checks := []CheckResult{
		runCheck(CheckNameCodeowners, func() CheckResult {
			return CheckCodeowners(p.CodeownersContent)
		}),
		runCheck(CheckNameBranchProtection, func() CheckResult {
			if p.BranchChecker == nil {
				return unmetResult(CheckNameBranchProtection, "branch protection checker가 설정되지 않음")
			}
			return p.BranchChecker.CheckBranchProtection(ctx, p.Owner, p.Repo, p.Branch)
		}),
		runCheck(CheckNameIdentity, func() CheckResult {
			return CheckIdentity(ctx, IdentityParams{
				WorkflowFetcher: p.WorkflowFetcher,
				WorkflowPath:    p.PRCreationWorkflowPath,
				AuthorLister:    p.AuthorLister,
				ExpectedActor:   p.ExpectedActor,
				Owner:           p.Owner,
				Repo:            p.Repo,
				Branch:          p.Branch,
			})
		}),
	}

	satisfied := true
	for _, c := range checks {
		if !c.Satisfied {
			satisfied = false
		}
	}
	return Result{Satisfied: satisfied, Checks: checks}
}

// runCheck wraps a single check in a panic-recover boundary, converting any
// panic into a fail-closed CheckResult instead of crashing the caller —
// mirroring internal/runtime.Supervisor's recover policy for this
// safety-critical gate.
func runCheck(name string, fn func() CheckResult) (result CheckResult) {
	defer func() {
		if r := recover(); r != nil {
			result = unmetResult(name, fmt.Sprintf("panic during check: %v\n%s", r, debug.Stack()))
		}
	}()
	return fn()
}

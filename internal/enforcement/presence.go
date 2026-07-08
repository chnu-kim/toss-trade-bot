package enforcement

import (
	"context"
	"fmt"
	"runtime/debug"
)

// Params wires everything Run needs to answer the three ADR-0009 point 8
// pillars. BranchChecker and IdentityResolver are interfaces so callers (and
// tests) can inject fakes; cmd/presence-check is responsible for wiring real
// implementations with the right credentials — notably (b) and (c)
// deliberately use *different* credentials (an admin-capable token for branch
// protection reads vs. the App's own JWT for identity), because the App
// intentionally lacks Administration permission (ADR-0009 point 6) and so
// cannot itself answer (b).
type Params struct {
	// (a) — raw content of .github/CODEOWNERS.
	CodeownersContent string

	// (b) — branch protection lookup target and checker.
	Owner, Repo, Branch string
	BranchChecker       BranchProtectionChecker

	// (c) — identity resolver and the actor it must resolve to.
	IdentityResolver ActorResolver
	ExpectedActor    string
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
			return CheckIdentity(ctx, p.IdentityResolver, p.ExpectedActor)
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

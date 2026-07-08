package enforcement

import (
	"context"
	"errors"
	"testing"
)

// fakeBranchProtectionChecker is a stub BranchProtectionChecker for exercising
// Run without a network round trip.
type fakeBranchProtectionChecker struct {
	result CheckResult
}

func (f fakeBranchProtectionChecker) CheckBranchProtection(context.Context, string, string, string) CheckResult {
	return f.result
}

func validParams() Params {
	return Params{
		CodeownersContent: validCodeowners,
		Owner:             "chnu-kim",
		Repo:              "toss-trade-bot",
		Branch:            "main",
		BranchChecker:     fakeBranchProtectionChecker{result: metResult(CheckNameBranchProtection)},
		IdentityResolver:  fakeActorResolver{actor: "mechanu[bot]"},
		ExpectedActor:     "mechanu[bot]",
	}
}

func TestRun_AllThreeSatisfied(t *testing.T) {
	got := Run(context.Background(), validParams())
	if !got.Satisfied {
		t.Fatalf("Run() = %+v, want Satisfied=true", got)
	}
	if len(got.Checks) != 3 {
		t.Fatalf("len(Checks) = %d, want 3", len(got.Checks))
	}
	if len(got.Reasons()) != 0 {
		t.Fatalf("Reasons() = %v, want empty when satisfied", got.Reasons())
	}
}

func TestRun_CodeownersUnmetCollapsesWhole(t *testing.T) {
	p := validParams()
	p.CodeownersContent = "" // (a) fails
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("unmet (a) codeowners must collapse the whole Result to false")
	}
	assertUnmetCheck(t, got, CheckNameCodeowners)
}

func TestRun_BranchProtectionUnmetCollapsesWhole(t *testing.T) {
	p := validParams()
	p.BranchChecker = fakeBranchProtectionChecker{
		result: unmetResult(CheckNameBranchProtection, "require_code_owner_reviews off"),
	}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("unmet (b) branch protection must collapse the whole Result to false")
	}
	assertUnmetCheck(t, got, CheckNameBranchProtection)
}

func TestRun_IdentityUnmetCollapsesWhole(t *testing.T) {
	p := validParams()
	p.IdentityResolver = fakeActorResolver{actor: "chnu-kim"} // still human
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("unmet (c) identity must collapse the whole Result to false")
	}
	assertUnmetCheck(t, got, CheckNameIdentity)
}

func TestRun_IdentityResolverErrorFailsClosed(t *testing.T) {
	p := validParams()
	p.IdentityResolver = fakeActorResolver{err: errors.New("network unreachable")}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("identity resolver error must fail-closed")
	}
}

func TestRun_MissingDependenciesFailClosedWithoutPanic(t *testing.T) {
	// A caller that forgets to wire the branch checker / identity resolver
	// must never crash the presence-check itself — it must fail-closed.
	p := Params{
		CodeownersContent: validCodeowners,
		Owner:             "chnu-kim",
		Repo:              "toss-trade-bot",
		Branch:            "main",
		// BranchChecker and IdentityResolver left nil.
		ExpectedActor: "mechanu[bot]",
	}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("missing dependencies must fail-closed, not satisfy the check")
	}
}

func assertUnmetCheck(t *testing.T, r Result, name string) {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			if c.Satisfied {
				t.Fatalf("check %q reported Satisfied=true, want false", name)
			}
			return
		}
	}
	t.Fatalf("no check named %q found in Result.Checks: %+v", name, r.Checks)
}

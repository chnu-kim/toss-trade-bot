package enforcement

import (
	"context"
	"errors"
	"strings"
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

// panickyFileFetcher simulates a bug inside a check implementation — the
// runCheck boundary must degrade it to unmet, never crash the process.
type panickyFileFetcher struct{}

func (panickyFileFetcher) FetchFileContent(context.Context, string, string, string, string) (string, error) {
	panic("bug inside check (c) implementation")
}

func validParams() Params {
	return Params{
		CodeownersContent:      validCodeowners,
		Owner:                  "chnu-kim",
		Repo:                   "toss-trade-bot",
		Branch:                 "main",
		BranchChecker:          fakeBranchProtectionChecker{result: metResult(CheckNameBranchProtection)},
		WorkflowFetcher:        fakeFileFetcher{content: "name: pr-creation\non: repository_dispatch\n"},
		PRCreationWorkflowPath: ".github/workflows/pr-creation.yml",
		AuthorLister:           fakeAuthorLister{authors: []string{"mechanu[bot]", "chnu-kim"}},
		ExpectedActor:          "mechanu[bot]",
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
	// c-2 unmet: every observed PR still authored by the human account.
	p.AuthorLister = fakeAuthorLister{authors: []string{"chnu-kim"}}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("unmet (c) identity must collapse the whole Result to false")
	}
	assertUnmetCheck(t, got, CheckNameIdentity)
}

func TestRun_PreTransitionStateFailsClosed(t *testing.T) {
	// Reproduction of the repo's current, real state (issue #49 acceptance
	// criterion): CODEOWNERS live (a met), branch protection live (b met),
	// the PR-creation workflow merged to main (c-1 met) — but no
	// mechanu[bot]-authored PR exists yet, because PR creation has not been
	// handed to the workflow. The overall presence-check verdict must be
	// fail-closed: this issue implements detection, it does not switch
	// autonomy on.
	p := validParams()
	p.AuthorLister = fakeAuthorLister{authors: []string{"chnu-kim", "chnu-kim", "chnu-kim"}}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("pre-transition repo state must leave the presence-check unmet (fail-closed)")
	}
	assertUnmetCheck(t, got, CheckNameIdentity)
	if reasons := got.Reasons(); len(reasons) != 1 || !strings.Contains(reasons[0], "mechanu[bot]") {
		t.Fatalf("Reasons() = %v, want exactly the identity reason naming the missing actor", reasons)
	}
}

func TestRun_IdentityListerErrorFailsClosed(t *testing.T) {
	p := validParams()
	p.AuthorLister = fakeAuthorLister{err: errors.New("network unreachable")}
	got := Run(context.Background(), p)
	if got.Satisfied {
		t.Fatal("PR author lister error must fail-closed")
	}
}

func TestRun_PanicInsideCheckDegradesToUnmet(t *testing.T) {
	// The runCheck recover boundary contract (issue #49 acceptance
	// criterion): a panic inside one check implementation must not crash the
	// process — it degrades that check to unmet, and the other pillars still
	// run and report.
	p := validParams()
	p.WorkflowFetcher = panickyFileFetcher{}
	got := Run(context.Background(), p) // must not panic
	if got.Satisfied {
		t.Fatal("a panicking check must degrade to unmet, never to satisfied")
	}
	assertUnmetCheck(t, got, CheckNameIdentity)
	for _, c := range got.Checks {
		if c.Name == CheckNameIdentity && !strings.Contains(c.Reason, "panic during check") {
			t.Fatalf("identity Reason = %q, want the panic diagnosis in it", c.Reason)
		}
		// The other two pillars must still have been evaluated normally.
		if c.Name != CheckNameIdentity && !c.Satisfied {
			t.Fatalf("check %q reported unmet, want the panic contained to the identity check", c.Name)
		}
	}
}

func TestRun_MissingDependenciesFailClosedWithoutPanic(t *testing.T) {
	// A caller that forgets to wire the branch checker / workflow fetcher /
	// author lister must never crash the presence-check itself — it must
	// fail-closed.
	p := Params{
		CodeownersContent: validCodeowners,
		Owner:             "chnu-kim",
		Repo:              "toss-trade-bot",
		Branch:            "main",
		// BranchChecker, WorkflowFetcher, AuthorLister left nil.
		PRCreationWorkflowPath: ".github/workflows/pr-creation.yml",
		ExpectedActor:          "mechanu[bot]",
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

package gate

// PRContext is the minimal PR metadata the eligibility guard (ADR-0011
// point 4(f)) needs. Both fields must come from the GitHub API (PR object
// fields), never from PR-derived free text.
type PRContext struct {
	// IsCrossRepository is GitHub's own authoritative computation of
	// whether this PR's head repository differs from its base repository
	// (the `isCrossRepository` field `gh pr view --json` returns).
	// Deliberately not a manual "head owner/repo" == "base owner/repo"
	// string comparison: `gh pr view --json` has no `baseRepository`
	// field to read for that comparison (a live `gh pr view --json
	// ...,baseRepository,...` call fails with "Unknown JSON field" —
	// caught by an independent adversarial review against a real PR).
	// GitHub already computes the fork/same-repo distinction for us;
	// `isCrossRepository` is the only field that reports it.
	IsCrossRepository bool
	// Author is the PR author's login.
	Author string
}

// RequiredAuthor is the only PR author identity eligible for verdict
// production, auto-merge enable, and the merge trigger (ADR-0011 point
// 4(f): loop-authored PRs only).
const RequiredAuthor = "mechanu[bot]"

// Eligible implements ADR-0011 point 4(f)'s AND guard: a PR is eligible for
// verdict production, auto-merge enable, and the merge trigger only if BOTH
// head repo == base repo (fork rejection, i.e. NOT cross-repository) AND
// author == RequiredAuthor. This is deliberately not an OR (ADR-0011 point
// 4(f) judgment: OR would leave same-repo human/write-access-authored PRs,
// or PRs impersonating mechanu[bot] from a fork, eligible — the AND costs
// nothing operationally because legitimate loop PRs always satisfy both).
func Eligible(pr PRContext) bool {
	return !pr.IsCrossRepository && pr.Author == RequiredAuthor
}

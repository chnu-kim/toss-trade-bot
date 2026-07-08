package gate

// PRContext is the minimal PR metadata the eligibility guard (ADR-0011
// point 4(f)) needs. All three fields must come from the GitHub API (PR
// object fields), never from PR-derived free text.
type PRContext struct {
	// HeadRepo and BaseRepo are "owner/repo" for the PR's source and target
	// repositories respectively.
	HeadRepo string
	BaseRepo string
	// Author is the PR author's login.
	Author string
}

// RequiredAuthor is the only PR author identity eligible for verdict
// production, auto-merge enable, and the merge trigger (ADR-0011 point
// 4(f): loop-authored PRs only).
const RequiredAuthor = "mechanu[bot]"

// Eligible implements ADR-0011 point 4(f)'s AND guard: a PR is eligible for
// verdict production, auto-merge enable, and the merge trigger only if BOTH
// head repo == base repo (fork rejection) AND author == RequiredAuthor.
// This is deliberately not an OR (ADR-0011 point 4(f) judgment: OR would
// leave same-repo human/write-access-authored PRs, or PRs impersonating
// mechanu[bot] from a fork, eligible — the AND costs nothing operationally
// because legitimate loop PRs always satisfy both).
func Eligible(pr PRContext) bool {
	return pr.HeadRepo == pr.BaseRepo && pr.Author == RequiredAuthor
}

package enforcement

import (
	"context"
	"fmt"
	"strings"
)

// recentLoopPRWindow is how many of the repo's most recent PRs (by creation
// date, any state) c-2 inspects for an expected-actor-authored PR. A bounded,
// single-page window keeps "최근" honest: a bot-authored PR older than the
// window is stale evidence and does not count, and an unbounded scan could
// never report "no bot PR observed" deterministically.
const recentLoopPRWindow = 30

// FileContentFetcher is the c-1 seam: something that can read a file's
// committed content from a branch via the GitHub Contents API.
// *GitHubClient.FetchFileContent satisfies it. Local disk is deliberately not
// an acceptable implementation — GitHub runs the PR-creation workflow from
// the default branch's committed definition (repository_dispatch, ADR-0011
// point 3), so only the protected branch's committed content answers "does
// the workflow the platform would actually run exist?" (same reasoning as
// check (a)'s CODEOWNERS fetch).
type FileContentFetcher interface {
	FetchFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
}

// PullRequestAuthorLister is the c-2 seam: something that can report the
// author logins of the repo's most recent pull requests (newest first).
// *GitHubClient.ListRecentPullRequestAuthors satisfies it.
type PullRequestAuthorLister interface {
	ListRecentPullRequestAuthors(ctx context.Context, owner, repo string, limit int) ([]string, error)
}

// IdentityParams carries everything CheckIdentity needs to evaluate ADR-0011
// point 10's two legs against one repo/branch.
type IdentityParams struct {
	// c-1 — PR-creation workflow existence on the protected branch.
	WorkflowFetcher FileContentFetcher
	// WorkflowPath is the repo-relative path of the PR-creation workflow
	// (issue #47 fixed it as .github/workflows/pr-creation.yml).
	WorkflowPath string

	// c-2 — recent loop-PR authorship.
	AuthorLister PullRequestAuthorLister
	// ExpectedActor is the App bot login every loop-created PR must carry
	// once PR creation has moved into the workflow (e.g. "mechanu[bot]").
	ExpectedActor string

	// Shared lookup target. Branch is the protected branch c-1 reads the
	// workflow from (the ref GitHub actually executes for
	// repository_dispatch).
	Owner, Repo, Branch string
}

// CheckIdentity implements ADR-0009 point 8(c) as redefined by ADR-0011 point
// 10: the loop's PR-authoring identity has genuinely moved to the GitHub App
// only if BOTH (c-1) the PR-creation workflow exists on the protected branch
// AND (c-2) a recent loop-created PR is actually authored by ExpectedActor.
//
// The previous definition — proving possession of the App's private key via
// an App-JWT GET /app — was withdrawn as a semantic false positive: holding
// the key says nothing about which identity authors PRs (the probe passed
// while every loop PR was still authored by the human account), and App
// credentials must not exist outside CI at all (ADR-0011 points 1·10). Both
// legs here are read-only observations a plain read token can make; neither
// touches App credentials.
//
// Both legs are always evaluated (no short-circuit) so an unmet result
// reports every failing leg, and any error, missing dependency, empty
// evidence, or pre-transition state ("no bot-authored PR observed") is unmet
// — never satisfied (ADR-0009 point 8: 증거 없음 ≠ 안전함).
func CheckIdentity(ctx context.Context, p IdentityParams) CheckResult {
	var reasons []string
	if reason, ok := checkPRCreationWorkflow(ctx, p); !ok {
		reasons = append(reasons, "c-1: "+reason)
	}
	if reason, ok := checkLoopPRAuthor(ctx, p); !ok {
		reasons = append(reasons, "c-2: "+reason)
	}
	if len(reasons) > 0 {
		return unmetResult(CheckNameIdentity, strings.Join(reasons, "; "))
	}
	return metResult(CheckNameIdentity)
}

// checkPRCreationWorkflow is leg c-1: the PR-creation workflow file must
// exist, as a file with content, on the protected branch — confirmed via the
// Contents API, never local disk. 404, non-200, network errors, and non-file
// responses all surface through the fetcher's error and are unmet.
func checkPRCreationWorkflow(ctx context.Context, p IdentityParams) (reason string, ok bool) {
	if p.WorkflowFetcher == nil {
		return "PR-생성 workflow 검증 불가: content fetcher가 설정되지 않음", false
	}
	if p.WorkflowPath == "" {
		return "PR-생성 workflow 검증 불가: workflow 경로가 설정되지 않음", false
	}

	content, err := p.WorkflowFetcher.FetchFileContent(ctx, p.Owner, p.Repo, p.WorkflowPath, p.Branch)
	if err != nil {
		return fmt.Sprintf("PR-생성 workflow(%s@%s) 확인 불가: %v", p.WorkflowPath, p.Branch, err), false
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("PR-생성 workflow(%s@%s)가 비어 있음 — 실질 workflow 없음", p.WorkflowPath, p.Branch), false
	}
	return "", true
}

// checkLoopPRAuthor is leg c-2: among the repo's recentLoopPRWindow most
// recent PRs there must be at least one authored by ExpectedActor. Before the
// transition every loop PR is authored by the human account and is
// indistinguishable from a human-created PR, so "no bot-authored PR observed"
// is exactly the pre-transition state and is unmet by definition (ADR-0011
// point 10: 전환 전이면 미충족). API errors, an empty PR list, and a blank
// expected actor are all "cannot judge" and therefore unmet.
func checkLoopPRAuthor(ctx context.Context, p IdentityParams) (reason string, ok bool) {
	if p.AuthorLister == nil {
		return "loop PR 작성자 검증 불가: PR lister가 설정되지 않음", false
	}
	if p.ExpectedActor == "" {
		return "loop PR 작성자 검증 불가: 기대 actor가 설정되지 않음", false
	}

	authors, err := p.AuthorLister.ListRecentPullRequestAuthors(ctx, p.Owner, p.Repo, recentLoopPRWindow)
	if err != nil {
		return fmt.Sprintf("최근 PR 목록 조회 실패: %v", err), false
	}
	if len(authors) == 0 {
		return "관측된 PR이 없음 — loop PR 작성 identity를 판정할 데이터 부족", false
	}

	for _, author := range authors {
		if author != "" && strings.EqualFold(author, p.ExpectedActor) {
			return "", true
		}
	}
	return fmt.Sprintf(
		"최근 %d개 PR 중 %s 작성 PR이 관측되지 않음 — PR 생성이 아직 workflow로 전환되지 않음(전환 전)",
		len(authors), p.ExpectedActor,
	), false
}

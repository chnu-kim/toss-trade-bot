package enforcement

import (
	"context"
	"fmt"
	"strings"
	"time"
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

// PullRequestSummary is the c-2 evidence for one recent PR: who authored it,
// and whether its head repository is the base repository itself. SameRepo
// matters because ADR-0011 point 4(f)'s eligibility predicate for a loop PR
// is "head repo == base repo AND author == mechanu[bot]" — loop PRs are
// same-repo by construction (the PR-creation workflow creates them from a
// branch pushed to this repo), so a fork-origin PR is not loop-authorship
// evidence regardless of its author (codex adversarial-review finding on
// this PR).
type PullRequestSummary struct {
	Author   string
	SameRepo bool
	// BaseRef is the branch this PR targets (base.ref). c-2 only counts PRs
	// targeting the protected branch — a bot PR against some other branch
	// (e.g. develop) is not evidence that the loop creates PRs against the
	// protected branch (codex:review [P1] finding on this PR).
	BaseRef string
	// CreatedAt is when the PR was opened. The zero value means the API did
	// not report a parseable creation time — such a PR can never be
	// freshness-eligible (fail-closed), see checkLoopPRAuthor.
	CreatedAt time.Time
}

// PullRequestLister is the c-2 seam: something that can report the repo's
// most recent pull requests (newest first). *GitHubClient satisfies it.
type PullRequestLister interface {
	ListRecentPullRequests(ctx context.Context, owner, repo string, limit int) ([]PullRequestSummary, error)
}

// WorkflowRevisionFetcher is the c-2 freshness seam: it reports when the
// current revision of the file at path became **live on ref** — i.e. the
// moment that revision became reachable from the protected branch's tip, not
// when its commit was authored/committed.
//
// *GitHubClient.FetchWorkflowRevisionLiveTime satisfies it by resolving the
// PR that introduced the file's current revision and returning that PR's
// merged_at (a GitHub-authoritative branch-publication timestamp, unlike a
// commit's committer.date which a rebase/cherry-pick/merge-commit can make
// predate publication — codex adversarial-review [high]). It uses only the
// two evidence sources ADR-0011 point 10 named (the workflow file via the
// Contents/commits API, and PRs via the PR API), read-only, within the
// post-narrowing PAT spec (Contents: read + Pull requests: read, point 5 ②).
// Any inability to prove the live time (no history, no merged PR that
// published the revision, unparseable timestamp) is a zero time / error the
// caller treats as unmet — never fail-open.
type WorkflowRevisionFetcher interface {
	FetchWorkflowRevisionLiveTime(ctx context.Context, owner, repo, path, ref string) (time.Time, error)
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
	PRLister PullRequestLister
	// RevisionFetcher reports when the PR-creation workflow file's current
	// revision became live on the protected branch — c-2's freshness anchor
	// (a bot PR is only evidence about the revision that is on main *now* if
	// it was created after that revision went live).
	RevisionFetcher WorkflowRevisionFetcher
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
// recent PRs there must be at least one that satisfies the loop-PR
// eligibility predicate — head repo == base repo (ADR-0011 point 4(f)) AND
// author == ExpectedActor AND created after the PR-creation workflow's
// current revision went live.
//
// The freshness clause narrows a false-green class (codex adversarial-review
// [high]): author + same-repo alone has no causal link to the workflow
// revision c-1 sees. A legitimate, human-approved workflow rewrite that
// breaks PR creation would otherwise keep check (c) green on a stale
// bot PR — created under a revision that no longer exists — until that PR
// falls out of the window. Requiring created_at after the current revision's
// merged_at removes the whole "old bot PR under a superseded revision" class.
// This is a filter on which PR qualifies as "the recent loop-created PR" —
// the axis issue #49's 미정 사항 delegates ("조회 엔드포인트·범위·정렬·필터") —
// and it only ever narrows what can go green, so it is strictly more
// fail-closed, not an ADR fork. Evidence sources are unchanged: the workflow
// file (Contents API) and recent PRs (Pull requests API); the revision-live time
// comes from the introducing PR's merged_at (Pull requests: read),
// resolved via the commits endpoint (Contents: read).
//
// 정직한 한계 (mirroring ADR-0011 point 4(e)(v)'s honest-limitation pattern —
// (i)~(iv)는 완화이지 보증이 아니다): this freshness test is a
// **defense-in-depth proxy, not a causal proof** that the PR was produced by
// the current revision. created_at is when the PR was *opened*, whereas the
// repository_dispatch run that opens it fixed its definition at *dispatch*
// time. So an in-flight straggler — a run started under revision R1 that
// reaches its "open PR" step only after a human-approved R2 (which breaks PR
// creation) merges — opens a PR with created_at > R2's merged_at and is
// briefly miscounted as R2-fresh, keeping check (c) green until that single
// straggler leaves the window. This is a non-adversarial, non-forgeable, self-
// limiting race (nobody controls the concurrent merge; pr-creation.yml's
// `concurrency: pr-creation` only serialises create-loop-pr runs among
// themselves, not against an R2 merge), and created_at is the best
// fail-closed proxy obtainable from GitHub PR fields. A stronger *causal*
// binding — tying the eligible PR to the revision via its head commit or the
// originating workflow_run — is NOT decided by ADR-0011 point 10 (which named
// only the workflow file and recent-PR-author as evidence sources); adopting
// one would be an architecture fork requiring /architect, the same booking as
// the SHA-stamped-PR-output-contract option this issue declined. Residual:
// the transient straggler false-green is booked here, not closed.
//
// Before the transition every loop PR is authored by the human account, so
// "no such PR observed" is exactly the pre-transition state and is unmet by
// definition (ADR-0011 point 10: 전환 전이면 미충족). A missing dependency, an
// API error, an empty PR list, a blank expected actor, and any ambiguity
// about the revision time or a PR's creation time are all "cannot judge" and
// therefore unmet (이슈 #49 미정 사항: 식별 모호·데이터 부족·판정 불가 → 미충족).
func checkLoopPRAuthor(ctx context.Context, p IdentityParams) (reason string, ok bool) {
	if p.PRLister == nil {
		return "loop PR 작성자 검증 불가: PR lister가 설정되지 않음", false
	}
	if p.RevisionFetcher == nil {
		return "loop PR 작성자 검증 불가: workflow revision fetcher가 설정되지 않음", false
	}
	if p.ExpectedActor == "" {
		return "loop PR 작성자 검증 불가: 기대 actor가 설정되지 않음", false
	}
	if p.WorkflowPath == "" {
		return "loop PR 작성자 검증 불가: workflow 경로가 설정되지 않음", false
	}

	revisionAt, err := p.RevisionFetcher.FetchWorkflowRevisionLiveTime(ctx, p.Owner, p.Repo, p.WorkflowPath, p.Branch)
	if err != nil {
		return fmt.Sprintf("현재 PR-생성 workflow 리비전이 live가 된 시각 확인 불가: %v", err), false
	}
	if revisionAt.IsZero() {
		return "현재 PR-생성 workflow 리비전이 live가 된 시각을 판정할 수 없음(리비전 도입 PR의 merged_at 확인 불가)", false
	}

	prs, err := p.PRLister.ListRecentPullRequests(ctx, p.Owner, p.Repo, recentLoopPRWindow)
	if err != nil {
		return fmt.Sprintf("최근 PR 목록 조회 실패: %v", err), false
	}
	if len(prs) == 0 {
		return "관측된 PR이 없음 — loop PR 작성 identity를 판정할 데이터 부족", false
	}

	for _, pr := range prs {
		// Eligible loop PR: same-repo (not a fork) AND targeting the
		// protected branch (loop PRs target it; a PR against another branch
		// is not evidence — codex:review [P1]) AND authored by ExpectedActor
		// AND created strictly after the revision instant. Equal timestamps
		// are ambiguous (second-granularity; a PR cannot be produced by a
		// revision that went live at the very same second) → 미충족.
		if pr.SameRepo && pr.BaseRef == p.Branch &&
			pr.Author != "" && strings.EqualFold(pr.Author, p.ExpectedActor) &&
			!pr.CreatedAt.IsZero() && pr.CreatedAt.After(revisionAt) {
			return "", true
		}
	}
	return fmt.Sprintf(
		"최근 %d개 PR 중 현재 workflow 리비전(%s 이후) 생성된 %s 작성 same-repo·%s-대상 PR이 관측되지 않음 — PR 생성이 아직 현재 workflow로 전환되지 않음(전환 전이거나 낡은 증거)",
		len(prs), revisionAt.UTC().Format(time.RFC3339), p.ExpectedActor, p.Branch,
	), false
}

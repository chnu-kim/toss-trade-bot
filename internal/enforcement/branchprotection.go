package enforcement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// BranchProtectionChecker is implemented by *GitHubClient; presence.go
// depends on this interface (not the concrete type) so tests can inject a
// fake without spinning up an httptest server.
type BranchProtectionChecker interface {
	CheckBranchProtection(ctx context.Context, owner, repo, branch string) CheckResult
}

// branchProtectionResponse is the subset of GET
// /repos/{owner}/{repo}/branches/{branch}/protection this check needs.
type branchProtectionResponse struct {
	RequiredPullRequestReviews *struct {
		RequireCodeOwnerReviews bool `json:"require_code_owner_reviews"`
	} `json:"required_pull_request_reviews"`
}

// CheckBranchProtection implements ADR-0009 point 8(b): main's branch
// protection must require a CODEOWNERS review
// (required_pull_request_reviews.require_code_owner_reviews == true). Any
// network error, non-200 response, or unparseable body is "confirm unable" and
// therefore fail-closed, never treated as satisfied.
func (c *GitHubClient) CheckBranchProtection(ctx context.Context, owner, repo, branch string) CheckResult {
	path := fmt.Sprintf("/repos/%s/%s/branches/%s/protection", owner, repo, branch)
	resp, err := c.get(ctx, path)
	if err != nil {
		return unmetResult(CheckNameBranchProtection, fmt.Sprintf("branch protection 조회 실패(네트워크): %v", err))
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return unmetResult(CheckNameBranchProtection, fmt.Sprintf("branch protection 조회 실패: status %d", resp.StatusCode))
	}

	var parsed branchProtectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return unmetResult(CheckNameBranchProtection, fmt.Sprintf("branch protection 응답 파싱 실패: %v", err))
	}

	if parsed.RequiredPullRequestReviews == nil || !parsed.RequiredPullRequestReviews.RequireCodeOwnerReviews {
		return unmetResult(CheckNameBranchProtection, "require_code_owner_reviews가 꺼져 있거나 확인되지 않음")
	}
	return metResult(CheckNameBranchProtection)
}

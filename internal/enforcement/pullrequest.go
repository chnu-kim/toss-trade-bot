package enforcement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ListRecentPullRequests returns summaries (author login + same-repo flag) of
// the repo's most recently created pull requests, newest first, via
// GET /repos/{owner}/{repo}/pulls?state=all&sort=created&direction=desc.
//
// Query choices, pinned deliberately for check (c-2):
//   - state=all — a loop PR that was already merged (or closed) is still
//     authorship evidence; open-only would go blind right after a merge.
//   - sort=created&direction=desc — "the N most recent PRs" must be a
//     deterministic window; the default sort (created) is made explicit and
//     activity-based reordering (sort=updated) is avoided.
//   - single page of limit results — bounded recency, no pagination walk.
//
// Fail-closed field handling: a PR whose user object is null (e.g. a deleted
// account) yields an empty Author — it stays in the window (the window size
// is honest) but can never match an expected actor. SameRepo is true only
// when both head and base carry a usable (non-zero) repo id and they are
// equal; a null/absent head repo (deleted fork) or missing ids classify as
// NOT same-repo, never as evidence. BaseRef carries the PR's target branch so
// c-2 can require it equal the protected branch (codex:review [P1]).
//
// This is a read-only GET; the presence-check performs zero GitHub write
// calls. Fine-grained token requirement: Pull requests: read — within the
// post-narrowing PAT spec (ADR-0011 point 5 ②).
func (c *GitHubClient) ListRecentPullRequests(ctx context.Context, owner, repo string, limit int) ([]PullRequestSummary, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls?state=all&sort=created&direction=desc&per_page=%d",
		url.PathEscape(owner), url.PathEscape(repo), limit)

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return nil, fmt.Errorf("enforcement: list pull requests: %w", err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enforcement: list pull requests: status %d", resp.StatusCode)
	}

	type repoRef struct {
		Ref  string `json:"ref"`
		Repo *struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	}
	var parsed []struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
		Head      *repoRef `json:"head"`
		Base      *repoRef `json:"base"`
		CreatedAt string   `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("enforcement: decode pull request list: %w", err)
	}

	summaries := make([]PullRequestSummary, 0, len(parsed))
	for _, pr := range parsed {
		var s PullRequestSummary
		if pr.User != nil {
			s.Author = pr.User.Login
		}
		if pr.Base != nil {
			s.BaseRef = pr.Base.Ref
		}
		if pr.Head != nil && pr.Head.Repo != nil && pr.Base != nil && pr.Base.Repo != nil &&
			pr.Head.Repo.ID != 0 && pr.Head.Repo.ID == pr.Base.Repo.ID {
			s.SameRepo = true
		}
		// An absent/unparseable created_at leaves CreatedAt zero, which can
		// never satisfy the freshness predicate — per-PR fail-closed rather
		// than failing the whole list.
		if t, err := time.Parse(time.RFC3339, pr.CreatedAt); err == nil {
			s.CreatedAt = t
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}

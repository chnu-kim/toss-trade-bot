package enforcement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ListRecentPullRequestAuthors returns the author logins of the repo's most
// recently created pull requests, newest first, via
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
// A PR whose user object is null (e.g. a deleted account) yields an empty
// string entry: it stays in the window (the window size is honest) but can
// never match an expected actor. This is a read-only GET; the presence-check
// performs zero GitHub write calls. Fine-grained token requirement:
// Pull requests: read — within the post-narrowing PAT spec (ADR-0011 point
// 5 ②).
func (c *GitHubClient) ListRecentPullRequestAuthors(ctx context.Context, owner, repo string, limit int) ([]string, error) {
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

	var parsed []struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("enforcement: decode pull request list: %w", err)
	}

	authors := make([]string, 0, len(parsed))
	for _, pr := range parsed {
		if pr.User == nil {
			authors = append(authors, "")
			continue
		}
		authors = append(authors, pr.User.Login)
	}
	return authors, nil
}

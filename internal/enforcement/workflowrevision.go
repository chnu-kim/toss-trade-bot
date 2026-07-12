package enforcement

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// FetchWorkflowRevisionLiveTime returns when the current revision of the file
// at path became **live on ref** — the branch-publication moment, not a
// commit timestamp. It is c-2's freshness anchor (see WorkflowRevisionFetcher).
//
// Two read-only hops, both within the post-narrowing PAT spec (ADR-0011 point
// 5 ②):
//
//  1. GET /repos/{o}/{r}/commits?path={path}&sha={ref}&per_page=1 (Contents:
//     read) — the SHA of the commit that last modified the file as reachable
//     from ref.
//  2. GET /repos/{o}/{r}/commits/{sha}/pulls (Pull requests: read) — the PR(s)
//     that contain that commit; the merged one whose base branch is ref
//     published the revision, and its merged_at is the authoritative
//     branch-publication time.
//
// Why merged_at, not the commit's committer.date (codex adversarial-review
// [high]): committer.date is commit metadata that a rebase, cherry-pick, or
// merge-commit can make predate the moment the revision actually reached ref.
// A bot PR created in that gap would spuriously look "fresh". merged_at is set
// by GitHub when the merge lands on ref and cannot be backdated, so it is the
// correct live-on-ref boundary across squash, rebase, and merge-commit
// strategies alike.
//
// Fail-closed everywhere: a network error, non-200, empty commit history, a
// commit with no merged PR that targets ref (e.g. a direct push — which
// branch protection forbids on a sacred path anyway), or an unparseable
// merged_at all yield an error the caller treats as unmet. When several
// merged PRs target ref, the latest merged_at is used (the most conservative,
// most-recent publication boundary). This function never fabricates a time.
func (c *GitHubClient) FetchWorkflowRevisionLiveTime(ctx context.Context, owner, repo, path, ref string) (time.Time, error) {
	sha, err := c.lastCommitSHA(ctx, owner, repo, path, ref)
	if err != nil {
		return time.Time{}, err
	}
	return c.commitPublishTime(ctx, owner, repo, sha, ref)
}

// lastCommitSHA returns the SHA of the most recent commit that touched path as
// reachable from ref.
func (c *GitHubClient) lastCommitSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/commits?path=%s&sha=%s&per_page=1",
		url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(path), url.QueryEscape(ref))

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return "", fmt.Errorf("enforcement: fetch last commit for %s@%s: %w", path, ref, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enforcement: fetch last commit for %s@%s: status %d", path, ref, resp.StatusCode)
	}

	var parsed []struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("enforcement: decode commits response for %s@%s: %w", path, ref, err)
	}
	if len(parsed) == 0 || parsed[0].SHA == "" {
		return "", fmt.Errorf("enforcement: %s@%s has no commit history", path, ref)
	}
	return parsed[0].SHA, nil
}

// commitPublishTime returns the merged_at of the merged PR that brought sha to
// baseRef (branch-publication time). Only PRs whose base branch is baseRef and
// that are actually merged qualify; the latest such merged_at wins.
func (c *GitHubClient) commitPublishTime(ctx context.Context, owner, repo, sha, baseRef string) (time.Time, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/commits/%s/pulls",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha))

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("enforcement: fetch PRs for commit %s: %w", sha, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("enforcement: fetch PRs for commit %s: status %d", sha, resp.StatusCode)
	}

	var parsed []struct {
		MergedAt string `json:"merged_at"`
		Base     struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return time.Time{}, fmt.Errorf("enforcement: decode PRs for commit %s: %w", sha, err)
	}

	var latest time.Time
	for _, pr := range parsed {
		if pr.Base.Ref != baseRef || pr.MergedAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, pr.MergedAt)
		if err != nil {
			// An unparseable merged_at on an otherwise-qualifying PR is
			// ambiguous evidence — skip it rather than trust it.
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return time.Time{}, fmt.Errorf(
			"enforcement: commit %s has no merged PR targeting %s — cannot prove when the revision went live", sha, baseRef)
	}
	return latest, nil
}

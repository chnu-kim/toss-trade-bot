package enforcement

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpDoer is the minimal HTTP surface this package needs. *http.Client
// satisfies it; tests inject an httptest-backed server.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultGitHubAPIBaseURL is the GitHub REST API base used outside tests.
const defaultGitHubAPIBaseURL = "https://api.github.com"

// GitHubClient performs authenticated read-only GitHub REST calls needed by
// the presence-check. It intentionally does not retry: a presence-check must
// answer quickly and any failure already collapses to fail-closed, so masking
// a transient error behind a retry would only delay (not avoid) the correct
// unmet verdict, at the cost of test/CLI latency.
//
// The token here is the branch-protection *reader's* credential (ADR-0009
// point 8(b) needs admin-level read access, which the loop's GitHub App
// deliberately lacks per point 6 — see cmd/presence-check for how the caller
// is expected to supply an admin-capable token for this specific check,
// separate from the App credential used for CheckIdentity).
type GitHubClient struct {
	baseURL string
	token   string
	http    httpDoer
}

// NewGitHubClient builds a GitHubClient authenticating with token.
func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		baseURL: defaultGitHubAPIBaseURL,
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// get performs a single authenticated GET, no retries (see GitHubClient doc).
func (c *GitHubClient) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("enforcement: build request GET %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return c.http.Do(req)
}

// drainClose drains and closes a response body so the connection can be
// reused, and never panics on a nil response/body.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

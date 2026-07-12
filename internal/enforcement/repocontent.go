package enforcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchFileContent reads a single file's content from repo at ref (a branch,
// tag, or commit SHA) via GET /repos/{owner}/{repo}/contents/{path}?ref={ref}
// — the only correct way to answer "what does GitHub actually enforce",
// because GitHub evaluates CODEOWNERS (and everything else branch protection
// cares about) from the target branch's committed content, never from a
// caller's local checkout. A caller on a feature branch that edits
// CODEOWNERS, or with an uncommitted local change, must still see what is
// live on the protected branch — reading local disk here would silently
// paper over exactly the gap ADR-0009 point 8 exists to catch.
func (c *GitHubClient) FetchFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), pathEscapeSegments(path), url.QueryEscape(ref))

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return "", fmt.Errorf("enforcement: fetch %s@%s: %w", path, ref, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enforcement: fetch %s@%s: status %d", path, ref, resp.StatusCode)
	}

	var parsed struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("enforcement: decode contents response for %s@%s: %w", path, ref, err)
	}

	if parsed.Type != "file" {
		return "", fmt.Errorf("enforcement: %s@%s is not a file (type=%q — path may be a directory or not exist)", path, ref, parsed.Type)
	}
	if parsed.Encoding != "base64" {
		return "", fmt.Errorf("enforcement: %s@%s has unsupported encoding %q (file may exceed the Contents API's 1MB base64 limit)", path, ref, parsed.Encoding)
	}

	// GitHub wraps the base64 content across multiple lines; strip all
	// whitespace before decoding.
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, parsed.Content)

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("enforcement: decode base64 content for %s@%s: %w", path, ref, err)
	}
	return string(decoded), nil
}

// FetchLastCommitTime returns the timestamp of the most recent commit that
// touched path on ref, via
// GET /repos/{owner}/{repo}/commits?path={path}&sha={ref}&per_page=1 — the
// commit that produced the revision of that file now live on ref. It answers
// c-2's freshness question ("when did the current PR-creation workflow
// revision go live?") using only **Contents: read**, within the
// post-narrowing PAT spec (ADR-0011 point 5 ②); no Actions permission is
// required. It reads the committer date (when the commit actually landed on
// the branch), not the author date (which a rebase/cherry-pick can backdate).
//
// Every failure mode — network error, non-200, empty commit list (the file
// has no history on ref), or an unparseable/absent date — returns a zero
// time and/or error so the caller treats it as "cannot judge" and fails
// closed. It never fabricates a timestamp.
func (c *GitHubClient) FetchLastCommitTime(ctx context.Context, owner, repo, path, ref string) (time.Time, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/commits?path=%s&sha=%s&per_page=1",
		url.PathEscape(owner), url.PathEscape(repo), url.QueryEscape(path), url.QueryEscape(ref))

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("enforcement: fetch last commit for %s@%s: %w", path, ref, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("enforcement: fetch last commit for %s@%s: status %d", path, ref, resp.StatusCode)
	}

	var parsed []struct {
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return time.Time{}, fmt.Errorf("enforcement: decode commits response for %s@%s: %w", path, ref, err)
	}
	if len(parsed) == 0 {
		return time.Time{}, fmt.Errorf("enforcement: %s@%s has no commit history", path, ref)
	}

	raw := parsed[0].Commit.Committer.Date
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("enforcement: unparseable committer date %q for %s@%s: %w", raw, path, ref, err)
	}
	return t, nil
}

// pathEscapeSegments percent-escapes each "/"-separated segment of p
// individually, preserving the literal "/" separators between them — needed
// because the Contents API path parameter is itself a "/"-joined path, and
// naively escaping the whole string would also escape the separators.
func pathEscapeSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

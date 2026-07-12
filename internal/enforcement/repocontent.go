package enforcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

package enforcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHubClient_FetchFileContent_DecodesFromProtectedBranch(t *testing.T) {
	// The whole point of this check is that CODEOWNERS is fetched from the
	// branch GitHub actually enforces protection on (ref), not whatever
	// happens to be on local disk — a PR that edits CODEOWNERS, or a dirty
	// checkout, must not be able to fool this into reading the wrong content.
	var gotPath, gotQuery, gotAuth string
	// GitHub's real API wraps base64 content across multiple lines; the
	// decoder must tolerate that.
	want := "/.github/workflows/ @chnu-kim\n/.github/CODEOWNERS @chnu-kim\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(want))
	chunked := encoded[:len(encoded)/2] + "\n" + encoded[len(encoded)/2:] + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  chunked,
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main")
	if err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if gotPath != "/repos/chnu-kim/toss-trade-bot/contents/.github/CODEOWNERS" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotQuery != "ref=main" {
		t.Fatalf("request query = %q, want ref=main", gotQuery)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer test-token", gotAuth)
	}
}

func TestGitHubClient_FetchFileContent_UsesGivenRefNotLocalDisk(t *testing.T) {
	// Regression for the exact bug this test suite guards against: the ref
	// passed by the caller must reach the request unchanged — a caller on a
	// feature branch or with an uncommitted local edit must still ask GitHub
	// about the protected branch specifically.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte("content")),
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "some-feature-branch"); err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}
	if gotQuery != "ref=some-feature-branch" {
		t.Fatalf("request query = %q, want ref=some-feature-branch", gotQuery)
	}
}

func TestGitHubClient_FetchFileContent_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main"); err == nil {
		t.Fatal("non-200 status must return an error, not empty/zero content treated as success")
	}
}

func TestGitHubClient_FetchFileContent_NotAFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "dir", "name": "CODEOWNERS"}})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main"); err == nil {
		t.Fatal("a directory listing response must return an error")
	}
}

func TestGitHubClient_FetchFileContent_UnsupportedEncoding(t *testing.T) {
	// Files over 1MB come back with encoding "none" and empty content — must
	// not be silently treated as an empty (and therefore "missing sacred
	// path") CODEOWNERS.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "none",
			"content":  "",
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main"); err == nil {
		t.Fatal("unsupported encoding must return an error")
	}
}

func TestGitHubClient_FetchFileContent_MalformedBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  "not-valid-base64!!!",
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main"); err == nil {
		t.Fatal("malformed base64 must return an error")
	}
}

func TestGitHubClient_FetchFileContent_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main"); err == nil {
		t.Fatal("network error must return an error")
	}
}

func TestGitHubClient_FetchFileContent_PathIsEscapedAndJoinedWithSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte("x")),
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", "docs/adr/0009-adr-autonomy-sacred-invariant.md", "main"); err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}
	want := "/repos/chnu-kim/toss-trade-bot/contents/docs/adr/0009-adr-autonomy-sacred-invariant.md"
	if gotPath != want {
		t.Fatalf("request path = %q, want %q", gotPath, want)
	}
	if !strings.Contains(gotPath, "docs/adr/") {
		t.Fatalf("expected nested path segments preserved, got %q", gotPath)
	}
}

// --- FetchWorkflowRevisionLiveTime (c-2 freshness anchor) ---

const testWorkflowPath = ".github/workflows/pr-creation.yml"

// revisionServer wires a two-hop mock: the commits endpoint returns the given
// file-touching SHA, and the commits/{sha}/pulls endpoint returns the given
// raw JSON array body. It records the paths/queries hit so tests can assert
// the read-only contract.
func revisionServer(t *testing.T, sha, pullsBody string) (*httptest.Server, *[]string) {
	t.Helper()
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET (read-only)", r.Method)
		}
		hits = append(hits, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_, _ = w.Write([]byte(pullsBody))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			_, _ = w.Write([]byte(`[{"sha": "` + sha + `"}]`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	return srv, &hits
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_UsesMergedAtNotCommitDate(t *testing.T) {
	// The merge-commit backdating scenario (codex adversarial-review [high]):
	// the file-touching commit could carry an old committer date, but the PR
	// that published it to main merged at 2026-07-10T12:00:00Z — that
	// GitHub-set merged_at is the true live-on-main boundary.
	pulls := `[{"merged_at": "2026-07-10T12:00:00Z", "base": {"ref": "main"}}]`
	srv, hits := revisionServer(t, "abc123", pulls)
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main")
	if err != nil {
		t.Fatalf("FetchWorkflowRevisionLiveTime: %v", err)
	}
	if want := "2026-07-10T12:00:00Z"; got.UTC().Format(time.RFC3339) != want {
		t.Fatalf("time = %s, want merged_at %s", got.UTC().Format(time.RFC3339), want)
	}

	// Two read-only hops: commits (Contents: read) then commits/{sha}/pulls
	// (Pull requests: read). ADR-0011 point 5 ②.
	if len(*hits) != 2 {
		t.Fatalf("hits = %v, want exactly 2 (commits, then pulls)", *hits)
	}
	if !strings.Contains((*hits)[0], "/repos/chnu-kim/toss-trade-bot/commits?") ||
		!strings.Contains((*hits)[0], "path=.github%2Fworkflows%2Fpr-creation.yml") ||
		!strings.Contains((*hits)[0], "sha=main") {
		t.Fatalf("first hit = %q, want the commits lookup", (*hits)[0])
	}
	if !strings.Contains((*hits)[1], "/repos/chnu-kim/toss-trade-bot/commits/abc123/pulls") {
		t.Fatalf("second hit = %q, want the commit's pulls lookup", (*hits)[1])
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_LatestMergedAtWins(t *testing.T) {
	// Several qualifying merged PRs → the latest merged_at (most conservative
	// publication boundary) is used.
	pulls := `[
		{"merged_at": "2026-07-05T00:00:00Z", "base": {"ref": "main"}},
		{"merged_at": "2026-07-10T00:00:00Z", "base": {"ref": "main"}}
	]`
	srv, _ := revisionServer(t, "abc123", pulls)
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main")
	if err != nil {
		t.Fatalf("FetchWorkflowRevisionLiveTime: %v", err)
	}
	if want := "2026-07-10T00:00:00Z"; got.UTC().Format(time.RFC3339) != want {
		t.Fatalf("time = %s, want the latest merged_at %s", got.UTC().Format(time.RFC3339), want)
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_IgnoresOtherBaseAndUnmerged(t *testing.T) {
	// A PR merged into some other branch, and an unmerged PR, both mentioning
	// the commit — neither published the revision to main, so both are
	// ignored and the qualifying one (main) is used.
	pulls := `[
		{"merged_at": "2026-07-20T00:00:00Z", "base": {"ref": "develop"}},
		{"merged_at": null, "base": {"ref": "main"}},
		{"merged_at": "2026-07-09T00:00:00Z", "base": {"ref": "main"}}
	]`
	srv, _ := revisionServer(t, "abc123", pulls)
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main")
	if err != nil {
		t.Fatalf("FetchWorkflowRevisionLiveTime: %v", err)
	}
	if want := "2026-07-09T00:00:00Z"; got.UTC().Format(time.RFC3339) != want {
		t.Fatalf("time = %s, want the main-targeting merged PR's merged_at %s", got.UTC().Format(time.RFC3339), want)
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_NoMergedPRTargetingRefIsError(t *testing.T) {
	// A commit with no merged PR that targets main (e.g. a direct push, or
	// only unmerged/other-base PRs) cannot prove branch-publication time →
	// error → the caller fails closed.
	pulls := `[
		{"merged_at": null, "base": {"ref": "main"}},
		{"merged_at": "2026-07-20T00:00:00Z", "base": {"ref": "develop"}}
	]`
	srv, _ := revisionServer(t, "abc123", pulls)
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("no merged PR targeting the ref must return an error, not a zero time treated as valid")
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_EmptyCommitHistoryIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`)) // commits endpoint: no history
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("an empty commit history must return an error")
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_CommitsNonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("non-200 on the commits hop must return an error")
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_PullsNonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[{"sha": "abc123"}]`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("non-200 on the pulls hop must return an error")
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_UnparseableMergedAtIsError(t *testing.T) {
	// The only PR is main-targeting and "merged" but its merged_at does not
	// parse — ambiguous, so it does not count, leaving no qualifying PR.
	pulls := `[{"merged_at": "not-a-timestamp", "base": {"ref": "main"}}]`
	srv, _ := revisionServer(t, "abc123", pulls)
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("an unparseable merged_at must not be trusted; with no other qualifying PR this must error")
	}
}

func TestGitHubClient_FetchWorkflowRevisionLiveTime_NetworkErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.FetchWorkflowRevisionLiveTime(context.Background(), "chnu-kim", "toss-trade-bot", testWorkflowPath, "main"); err == nil {
		t.Fatal("network error must return an error")
	}
}

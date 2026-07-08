package enforcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

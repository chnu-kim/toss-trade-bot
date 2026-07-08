package enforcement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestGitHubClient(baseURL, token string) *GitHubClient {
	c := NewGitHubClient(token)
	c.baseURL = baseURL
	return c
}

func TestGitHubClient_CheckBranchProtection_CodeOwnerReviewRequired(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"required_pull_request_reviews": map[string]any{
				"require_code_owner_reviews": true,
			},
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")

	if !got.Satisfied {
		t.Fatalf("CheckBranchProtection() = %+v, want Satisfied=true", got)
	}
	if got.Name != CheckNameBranchProtection {
		t.Fatalf("Name = %q, want %q", got.Name, CheckNameBranchProtection)
	}
	wantPath := "/repos/chnu-kim/toss-trade-bot/branches/main/protection"
	if gotPath != wantPath {
		t.Fatalf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer test-token", gotAuth)
	}
}

func TestGitHubClient_CheckBranchProtection_CodeOwnerReviewNotRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"required_pull_request_reviews": map[string]any{
				"require_code_owner_reviews": false,
			},
		})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")
	if got.Satisfied {
		t.Fatal("require_code_owner_reviews=false must not satisfy the check")
	}
}

func TestGitHubClient_CheckBranchProtection_MissingRequiredPullRequestReviews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")
	if got.Satisfied {
		t.Fatal("missing required_pull_request_reviews must not satisfy the check")
	}
}

func TestGitHubClient_CheckBranchProtection_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")
	if got.Satisfied {
		t.Fatal("non-200 status must fail-closed, not satisfy the check")
	}
	if !strings.Contains(got.Reason, "403") {
		t.Fatalf("Reason = %q, want it to mention the status code", got.Reason)
	}
}

func TestGitHubClient_CheckBranchProtection_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")
	if got.Satisfied {
		t.Fatal("malformed JSON must fail-closed, not satisfy the check")
	}
}

func TestGitHubClient_CheckBranchProtection_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: any request errors out

	c := newTestGitHubClient(srv.URL, "test-token")
	got := c.CheckBranchProtection(context.Background(), "chnu-kim", "toss-trade-bot", "main")
	if got.Satisfied {
		t.Fatal("network error must fail-closed, not satisfy the check")
	}
	if got.Reason == "" {
		t.Fatal("unmet result must carry a reason")
	}
}

package enforcement

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- fakes ---

// fakeFileFetcher is a stub FileContentFetcher for exercising CheckIdentity's
// c-1 leg without a network round trip.
type fakeFileFetcher struct {
	content string
	err     error
}

func (f fakeFileFetcher) FetchFileContent(context.Context, string, string, string, string) (string, error) {
	return f.content, f.err
}

// fakePRLister is a stub PullRequestLister for exercising CheckIdentity's
// c-2 leg without a network round trip.
type fakePRLister struct {
	prs []PullRequestSummary
	err error
}

func (f fakePRLister) ListRecentPullRequests(context.Context, string, string, int) ([]PullRequestSummary, error) {
	return f.prs, f.err
}

// sameRepoPRs builds same-repo PullRequestSummary entries for the given
// author logins — the common fixture shape (loop PRs are same-repo by
// construction).
func sameRepoPRs(authors ...string) []PullRequestSummary {
	prs := make([]PullRequestSummary, 0, len(authors))
	for _, a := range authors {
		prs = append(prs, PullRequestSummary{Author: a, SameRepo: true})
	}
	return prs
}

func metIdentityParams() IdentityParams {
	return IdentityParams{
		WorkflowFetcher: fakeFileFetcher{content: "name: pr-creation\non: repository_dispatch\n"},
		Owner:           "chnu-kim",
		Repo:            "toss-trade-bot",
		Branch:          "main",
		WorkflowPath:    ".github/workflows/pr-creation.yml",
		PRLister:        fakePRLister{prs: sameRepoPRs("mechanu[bot]", "chnu-kim")},
		ExpectedActor:   "mechanu[bot]",
	}
}

// --- composite: check (c) = c-1 AND c-2 ---

func TestCheckIdentity_BothLegsMet(t *testing.T) {
	got := CheckIdentity(context.Background(), metIdentityParams())
	if !got.Satisfied {
		t.Fatalf("CheckIdentity() = %+v, want Satisfied=true when c-1 and c-2 are both met", got)
	}
	if got.Name != CheckNameIdentity {
		t.Fatalf("Name = %q, want %q", got.Name, CheckNameIdentity)
	}
}

func TestCheckIdentity_C1MetC2UnmetIsUnmet(t *testing.T) {
	// Workflow exists on main, but no mechanu[bot]-authored PR has ever been
	// observed (pre-transition) — one leg alone must never satisfy check (c).
	p := metIdentityParams()
	p.PRLister = fakePRLister{prs: sameRepoPRs("chnu-kim", "chnu-kim")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("c-1 met + c-2 unmet must not satisfy check (c)")
	}
	if !strings.Contains(got.Reason, "c-2") {
		t.Fatalf("Reason = %q, want it to identify the failing leg c-2", got.Reason)
	}
}

func TestCheckIdentity_C1UnmetC2MetIsUnmet(t *testing.T) {
	// A mechanu[bot]-authored PR is observed, but the PR-creation workflow is
	// not confirmed on main — the other one-leg combination must also be unmet.
	p := metIdentityParams()
	p.WorkflowFetcher = fakeFileFetcher{err: errors.New("status 404")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("c-1 unmet + c-2 met must not satisfy check (c)")
	}
	if !strings.Contains(got.Reason, "c-1") {
		t.Fatalf("Reason = %q, want it to identify the failing leg c-1", got.Reason)
	}
}

func TestCheckIdentity_BothLegsUnmetReportsBothReasons(t *testing.T) {
	p := metIdentityParams()
	p.WorkflowFetcher = fakeFileFetcher{err: errors.New("status 404")}
	p.PRLister = fakePRLister{err: errors.New("network unreachable")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("both legs unmet must not satisfy check (c)")
	}
	if !strings.Contains(got.Reason, "c-1") || !strings.Contains(got.Reason, "c-2") {
		t.Fatalf("Reason = %q, want both failing legs reported for diagnosability", got.Reason)
	}
}

// --- c-1: PR-creation workflow existence on the protected branch ---

func TestCheckIdentity_C1NilFetcherFailsClosed(t *testing.T) {
	p := metIdentityParams()
	p.WorkflowFetcher = nil
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("nil workflow fetcher must fail-closed")
	}
	if got.Reason == "" {
		t.Fatal("unmet result must carry a reason")
	}
}

func TestCheckIdentity_C1EmptyWorkflowPathFailsClosed(t *testing.T) {
	p := metIdentityParams()
	p.WorkflowPath = ""
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("empty workflow path must fail-closed, not silently fetch nothing")
	}
}

func TestCheckIdentity_C1EmptyWorkflowContentFailsClosed(t *testing.T) {
	// A zero-byte/whitespace-only file at the workflow path is not evidence
	// that a PR-creation workflow exists — no evidence is never satisfied.
	p := metIdentityParams()
	p.WorkflowFetcher = fakeFileFetcher{content: "   \n"}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("empty workflow file content must fail-closed")
	}
}

// The four c-1 failure modes the issue's acceptance criteria enumerate —
// 404, non-200, network error, directory (non-file) response — exercised
// through the real GitHubClient wired as the WorkflowFetcher, so the test
// covers the exact code path cmd/presence-check runs.

func c1ParamsWithServer(t *testing.T, handler http.HandlerFunc) (IdentityParams, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	p := metIdentityParams()
	p.WorkflowFetcher = newTestGitHubClient(srv.URL, "test-token")
	return p, srv
}

func TestCheckIdentity_C1WorkflowNotFoundOnMain(t *testing.T) {
	p, srv := c1ParamsWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("404 for the PR-creation workflow must fail-closed")
	}
	if !strings.Contains(got.Reason, "404") {
		t.Fatalf("Reason = %q, want the diagnosable status 404 in it", got.Reason)
	}
}

func TestCheckIdentity_C1NonOKStatusFailsClosed(t *testing.T) {
	p, srv := c1ParamsWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("non-200 for the PR-creation workflow must fail-closed")
	}
	if !strings.Contains(got.Reason, "500") {
		t.Fatalf("Reason = %q, want the diagnosable status 500 in it", got.Reason)
	}
}

func TestCheckIdentity_C1NetworkErrorFailsClosed(t *testing.T) {
	p, srv := c1ParamsWithServer(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // connection refused from here on

	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("network error fetching the PR-creation workflow must fail-closed")
	}
	if !strings.Contains(got.Reason, "c-1") {
		t.Fatalf("Reason = %q, want the failing leg identified", got.Reason)
	}
}

func TestCheckIdentity_C1DirectoryResponseFailsClosed(t *testing.T) {
	// Contents API "type" other than "file" (symlink/submodule object form) —
	// the path existing as something that is not a file is not a workflow.
	p, srv := c1ParamsWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"dir"}`))
	})
	defer srv.Close()

	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("non-file contents response must fail-closed")
	}
	if !strings.Contains(got.Reason, "not a file") {
		t.Fatalf("Reason = %q, want the non-file diagnosis in it", got.Reason)
	}
}

// --- c-2: recent loop-PR author == expected actor ---

func TestCheckIdentity_C2NilListerFailsClosed(t *testing.T) {
	p := metIdentityParams()
	p.PRLister = nil
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("nil author lister must fail-closed")
	}
}

func TestCheckIdentity_C2PreTransitionNoBotPRFailsClosed(t *testing.T) {
	// The repo's current, real state: every observed PR is authored by the
	// human account — the transition has not happened, so check (c) must be
	// unmet (ADR-0011 point 10: 전환 전이면 미충족).
	p := metIdentityParams()
	p.PRLister = fakePRLister{prs: sameRepoPRs("chnu-kim", "chnu-kim", "chnu-kim")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("pre-transition state (no mechanu[bot]-authored PR) must fail-closed")
	}
	if !strings.Contains(got.Reason, "mechanu[bot]") {
		t.Fatalf("Reason = %q, want the missing expected actor named", got.Reason)
	}
}

func TestCheckIdentity_C2APIErrorFailsClosed(t *testing.T) {
	p := metIdentityParams()
	p.PRLister = fakePRLister{err: errors.New("status 500")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("PR list API error must fail-closed")
	}
	if got.Reason == "" {
		t.Fatal("unmet result must carry a reason")
	}
}

func TestCheckIdentity_C2EmptyPRListFailsClosed(t *testing.T) {
	// Zero PRs observed = no data to judge authorship — unverifiable is
	// never satisfied.
	p := metIdentityParams()
	p.PRLister = fakePRLister{prs: []PullRequestSummary{}}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("empty PR list must fail-closed")
	}
}

func TestCheckIdentity_C2EmptyExpectedActorFailsClosed(t *testing.T) {
	// A blank expected actor must never accidentally match a blank author
	// entry (e.g. a PR whose user object was null).
	p := metIdentityParams()
	p.ExpectedActor = ""
	p.PRLister = fakePRLister{prs: sameRepoPRs("")}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("empty expected actor must fail-closed, not match empty author entries")
	}
}

func TestCheckIdentity_C2ForkBotPRDoesNotCount(t *testing.T) {
	// codex adversarial-review finding on this PR: c-2 must not be satisfied
	// by just any recent ExpectedActor-authored PR. The machine-checkable
	// non-loop class is a PR whose head repo is not the base repo (fork) —
	// loop PRs are same-repo by construction (the PR-creation workflow
	// pushes/creates within this repo), and ADR-0011 point 4(f)'s Phase B
	// eligibility predicate is exactly "head repo == base repo AND author ==
	// mechanu[bot]". A bot-authored PR that fails the same-repo half is not
	// loop-authorship evidence.
	p := metIdentityParams()
	p.PRLister = fakePRLister{prs: []PullRequestSummary{
		{Author: "mechanu[bot]", SameRepo: false}, // fork — must not count
		{Author: "chnu-kim", SameRepo: true},
	}}
	got := CheckIdentity(context.Background(), p)
	if got.Satisfied {
		t.Fatal("a fork-origin bot-authored PR must not satisfy c-2")
	}
	if !strings.Contains(got.Reason, "c-2") {
		t.Fatalf("Reason = %q, want the failing leg identified", got.Reason)
	}
}

// --- GitHubClient.ListRecentPullRequests ---

func TestGitHubClient_ListRecentPullRequests(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// #55: same-repo bot PR. #54: same-repo human PR. #53: null user,
		// null head repo (deleted fork). #52: fork PR (different repo ids).
		_, _ = w.Write([]byte(`[
			{"number": 55, "user": {"login": "mechanu[bot]"},
			 "head": {"repo": {"id": 111}}, "base": {"repo": {"id": 111}}},
			{"number": 54, "user": {"login": "chnu-kim"},
			 "head": {"repo": {"id": 111}}, "base": {"repo": {"id": 111}}},
			{"number": 53, "user": null,
			 "head": {"repo": null}, "base": {"repo": {"id": 111}}},
			{"number": 52, "user": {"login": "mechanu[bot]"},
			 "head": {"repo": {"id": 999}}, "base": {"repo": {"id": 111}}}
		]`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	prs, err := c.ListRecentPullRequests(context.Background(), "chnu-kim", "toss-trade-bot", 30)
	if err != nil {
		t.Fatalf("ListRecentPullRequests: %v", err)
	}
	want := []PullRequestSummary{
		{Author: "mechanu[bot]", SameRepo: true},
		{Author: "chnu-kim", SameRepo: true},
		{Author: "", SameRepo: false},
		{Author: "mechanu[bot]", SameRepo: false},
	}
	if len(prs) != len(want) {
		t.Fatalf("prs = %+v, want %+v", prs, want)
	}
	for i := range want {
		if prs[i] != want[i] {
			t.Fatalf("prs[%d] = %+v, want %+v", i, prs[i], want[i])
		}
	}

	// Read-only contract: this must be a GET — the presence-check performs
	// zero GitHub write calls (issue #49 규칙).
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET (read-only)", gotMethod)
	}
	if gotPath != "/repos/chnu-kim/toss-trade-bot/pulls" {
		t.Fatalf("request path = %q", gotPath)
	}
	// state=all: merged loop PRs are closed; sort by creation desc so the
	// window is "the N most recent PRs", deterministically.
	for _, param := range []string{"state=all", "sort=created", "direction=desc", "per_page=30"} {
		if !strings.Contains(gotQuery, param) {
			t.Fatalf("query = %q, want it to contain %q", gotQuery, param)
		}
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer test-token", gotAuth)
	}
}

func TestGitHubClient_ListRecentPullRequests_MissingRepoIDsAreNotSameRepo(t *testing.T) {
	// A response whose repo objects carry no usable id (0/absent) must not be
	// classified as same-repo — zero==zero is not evidence (fail-closed).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"number": 51, "user": {"login": "mechanu[bot]"},
			 "head": {"repo": {}}, "base": {"repo": {}}}
		]`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	prs, err := c.ListRecentPullRequests(context.Background(), "chnu-kim", "toss-trade-bot", 30)
	if err != nil {
		t.Fatalf("ListRecentPullRequests: %v", err)
	}
	if len(prs) != 1 || prs[0].SameRepo {
		t.Fatalf("prs = %+v, want a single entry with SameRepo=false when repo ids are absent", prs)
	}
}

func TestGitHubClient_ListRecentPullRequests_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.ListRecentPullRequests(context.Background(), "chnu-kim", "toss-trade-bot", 30); err == nil {
		t.Fatal("non-200 pulls response must return an error")
	}
}

func TestGitHubClient_ListRecentPullRequests_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message": "not an array"}`))
	}))
	defer srv.Close()

	c := newTestGitHubClient(srv.URL, "test-token")
	if _, err := c.ListRecentPullRequests(context.Background(), "chnu-kim", "toss-trade-bot", 30); err == nil {
		t.Fatal("malformed pulls response must return an error")
	}
}

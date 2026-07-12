package enforcement

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestProtectedBranchContentGovernsNotLocalDisk is the end-to-end regression
// for a GitHub-native review finding on an earlier version of this PR: the
// presence-check must judge CODEOWNERS by what is live on the protected
// branch (fetched via the GitHub Contents API), never by whatever happens to
// be sitting on local disk. It reproduces the exact failure scenario — local
// disk has a fully-compliant CODEOWNERS (as it would on a feature branch that
// is mid-edit, or a dirty checkout), while the protected branch's real,
// GitHub-enforced content is missing a sacred path — and asserts the overall
// Run() verdict follows the protected branch, not disk.
func TestProtectedBranchContentGovernsNotLocalDisk(t *testing.T) {
	localDiskContent := validCodeowners // fully compliant, would satisfy (a) if read directly

	// The protected branch's real content is missing 0009 protection — this
	// is what GitHub actually enforces right now.
	protectedBranchContent := `/.github/workflows/ @chnu-kim
/docs/adr/0004-*.md @chnu-kim
/docs/adr/0007-*.md @chnu-kim
/docs/adr/0008-*.md @chnu-kim
/docs/adr/0010-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`

	// Sanity-check the fixtures themselves demonstrate real divergence:
	// reading local disk directly would (wrongly) satisfy the check, while
	// the protected branch content correctly does not.
	if !CheckCodeowners(localDiskContent).Satisfied {
		t.Fatal("test fixture invalid: localDiskContent must itself satisfy CheckCodeowners")
	}
	if CheckCodeowners(protectedBranchContent).Satisfied {
		t.Fatal("test fixture invalid: protectedBranchContent must itself NOT satisfy CheckCodeowners")
	}

	// Write the "wrong" content to a real local file, at the same relative
	// path a naive os.ReadFile(".github/CODEOWNERS") implementation would
	// use, so a regression back to reading local disk would silently pick
	// this up and flip the test green for the wrong reason.
	dir := t.TempDir()
	localPath := filepath.Join(dir, ".github", "CODEOWNERS")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(localPath, []byte(localDiskContent), 0o644); err != nil {
		t.Fatalf("write local CODEOWNERS: %v", err)
	}

	// The mock GitHub API represents the protected branch: it always returns
	// protectedBranchContent, regardless of what's on local disk.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte(protectedBranchContent)),
		})
	}))
	defer srv.Close()

	client := newTestGitHubClient(srv.URL, "test-token")
	fetched, err := client.FetchFileContent(context.Background(), "chnu-kim", "toss-trade-bot", ".github/CODEOWNERS", "main")
	if err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}

	got := Run(context.Background(), Params{
		CodeownersContent:      fetched, // the fix under test: NOT localDiskContent
		Owner:                  "chnu-kim",
		Repo:                   "toss-trade-bot",
		Branch:                 "main",
		BranchChecker:          fakeBranchProtectionChecker{result: metResult(CheckNameBranchProtection)},
		WorkflowFetcher:        fakeFileFetcher{content: "name: pr-creation\non: repository_dispatch\n"},
		PRCreationWorkflowPath: ".github/workflows/pr-creation.yml",
		AuthorLister:           fakeAuthorLister{authors: []string{"mechanu[bot]"}},
		ExpectedActor:          "mechanu[bot]",
	})

	if got.Satisfied {
		t.Fatalf("Run() must follow the protected branch content (missing 0009) and fail-closed, got %+v", got)
	}
	assertUnmetCheck(t, got, CheckNameCodeowners)
}

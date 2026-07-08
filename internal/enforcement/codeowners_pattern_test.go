package enforcement

import "testing"

// TestCodeownersPatternMatches covers the subset of GitHub's CODEOWNERS
// (gitignore-style) pattern syntax this package relies on:
// https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners
func TestCodeownersPatternMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"anchored exact match", "/.github/CODEOWNERS", ".github/CODEOWNERS", true},
		{"anchored exact no match on suffix", "/.github/CODEOWNERS", ".github/CODEOWNERSX", false},
		{"anchored exact does not match nested", "/.github/CODEOWNERS", "sub/.github/CODEOWNERS", false},

		{"trailing slash matches file directly under dir", "/.github/workflows/", ".github/workflows/ci.yml", true},
		{"trailing slash matches nested file", "/.github/workflows/", ".github/workflows/sub/dir/ci.yml", true},
		{"trailing slash does not match the bare dir name", "/.github/workflows/", ".github/workflows", false},
		{"trailing slash does not match a sibling dir", "/.github/workflows/", ".github/workflows2/ci.yml", false},

		{"single star matches within one segment", "/docs/adr/0009-*.md", "docs/adr/0009-adr-autonomy-sacred-invariant.md", true},
		{"single star does not cross a slash", "/docs/adr/0009-*.md", "docs/adr/0009-x/nested.md", false},
		{"single star requires the literal suffix", "/docs/adr/0009-*.md", "docs/adr/0008-other.md", false},

		{"single segment wildcard matches one path segment", "/.github/*", ".github/CODEOWNERS", true},
		{"single segment wildcard does not match nested path", "/.github/*", ".github/workflows/ci.yml", false},

		{"trailing double star matches like trailing slash", ".github/workflows/**", ".github/workflows/ci.yml", true},
		{"trailing double star matches nested", ".github/workflows/**", ".github/workflows/sub/dir/ci.yml", true},

		{"unanchored pattern matches at root too", ".github/CODEOWNERS", ".github/CODEOWNERS", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codeownersPatternMatches(tc.pattern, tc.path)
			if got != tc.want {
				t.Fatalf("codeownersPatternMatches(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

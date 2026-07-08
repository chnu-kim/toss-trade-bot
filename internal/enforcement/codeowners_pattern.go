package enforcement

import (
	"regexp"
	"strings"
)

// codeownersPatternMatches reports whether a CODEOWNERS pattern matches path
// (a repo-root-relative path using "/" separators, no leading slash), per the
// subset of GitHub's gitignore-style CODEOWNERS syntax this package relies on
// (https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners):
//
//   - A leading "/" anchors the pattern to the repository root; without one,
//     the pattern may match starting at any directory level.
//   - A trailing "/" (or trailing "/**") matches files *under* that
//     directory, not the directory name itself.
//   - "*" matches any run of characters except "/" (one path segment).
//   - "**" matches across any number of path segments.
//
// Negation ("!"), character classes ("[...]"), and escaped "#" are not
// supported — GitHub's own docs say these don't work in CODEOWNERS either,
// and this package only ever evaluates its own small, static, repo-owned
// pattern list (ADR-0009's sacred paths), never arbitrary input.
func codeownersPatternMatches(pattern, path string) bool {
	return codeownersPatternRegexp(pattern).MatchString(path)
}

func codeownersPatternRegexp(pattern string) *regexp.Regexp {
	p := pattern
	anchored := strings.HasPrefix(p, "/")
	p = strings.TrimPrefix(p, "/")

	dirOnly := false
	switch {
	case strings.HasSuffix(p, "/**"):
		dirOnly = true
		p = strings.TrimSuffix(p, "/**")
	case strings.HasSuffix(p, "/"):
		dirOnly = true
		p = strings.TrimSuffix(p, "/")
	}

	var re strings.Builder
	if anchored {
		re.WriteString("^")
	} else {
		re.WriteString("^(?:.*/)?")
	}
	re.WriteString(translateCodeownersGlob(p))
	if dirOnly {
		re.WriteString("/.*")
	}
	re.WriteString("$")

	// pattern is always one of this package's own static sacred-path
	// patterns, never external input, so a compile failure here would be a
	// programming error caught immediately by this package's own tests.
	return regexp.MustCompile(re.String())
}

// translateCodeownersGlob converts "*"/"**" wildcards into their regexp
// equivalents, escaping every other character literally.
func translateCodeownersGlob(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); {
		if strings.HasPrefix(p[i:], "**") {
			b.WriteString(".*")
			i += 2
			continue
		}
		if p[i] == '*' {
			b.WriteString("[^/]*")
			i++
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(p[i])))
		i++
	}
	return b.String()
}

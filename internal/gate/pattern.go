package gate

import (
	"path/filepath"
	"strings"
)

// globMatches reports whether pattern matches path, using a small,
// root-anchored glob subset tailored to this package's risk-classification
// mapping (ADR-0008 point 5): every pattern matches starting at the
// repository root (there is no CODEOWNERS-style "may match at any directory
// level" behavior here, because every path this package classifies is
// already a repo-root-relative diff path — anchoring removes an entire
// class of ambiguity that mapping doesn't need).
//
//   - path segments are separated by "/".
//   - "*" inside a segment matches any run of characters except "/" (one
//     path segment, via path/filepath.Match).
//   - "**" as a whole segment matches zero or more path segments.
//
// This is deliberately a fresh, independent implementation rather than a
// reuse of internal/enforcement's CODEOWNERS pattern matcher: the
// risk-classification mapping is a distinct, independently-owned
// enforcement-integrity artifact (ADR-0011 point 11), and importing across
// that boundary would let a change to one package's matching semantics
// silently change the other's.
func globMatches(pattern, path string) bool {
	return segmentsMatch(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func segmentsMatch(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			return true
		}
		for i := 0; i <= len(path); i++ {
			if segmentsMatch(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return segmentsMatch(pat[1:], path[1:])
}

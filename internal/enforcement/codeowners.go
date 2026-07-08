package enforcement

import (
	"fmt"
	"strings"
)

// RequiredOwner is the owner every sacred CODEOWNERS entry must list
// (ADR-0009 point 4).
const RequiredOwner = "@chnu-kim"

// RequiredSacredPathFragments are the ADR-0009 point 3-4 sacred paths that
// .github/CODEOWNERS must register, each with RequiredOwner attached, for
// enforcement-integrity to be technically backed.
//
// Matching is substring-based against each CODEOWNERS pattern rather than an
// exact string match, because the ADR prose and the actual CODEOWNERS syntax
// use slightly different path notations for the same intent (the ADR text
// writes ".github/workflows/**"; the real file writes "/.github/workflows/").
// The risk:critical path-classification mapping file is deliberately absent
// here — ADR-0009/point 4 and the issue that tracks this check both say its
// location/format is unconfirmed and out of scope for this presence-check.
var RequiredSacredPathFragments = []string{
	".github/workflows",
	".github/CODEOWNERS",
	"docs/adr/0004-",
	"docs/adr/0007-",
	"docs/adr/0008-",
	"docs/adr/0009-",
	"docs/adr/0010-",
}

// codeownersEntry is one non-comment, non-blank CODEOWNERS line: a path
// pattern and the owners listed after it.
type codeownersEntry struct {
	Pattern string
	Owners  []string
}

// parseCodeowners extracts path/owner entries from raw CODEOWNERS content,
// skipping comments (# ...) and blank lines. It never errors — CODEOWNERS has
// no line that is invalid to skip; an entry with no owners is still returned
// (owners will simply be empty), because "owner stripped" is itself a
// meaningful, checkable state (see CheckCodeowners).
func parseCodeowners(content string) []codeownersEntry {
	var entries []codeownersEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		entries = append(entries, codeownersEntry{
			Pattern: fields[0],
			Owners:  fields[1:],
		})
	}
	return entries
}

// hasOwnedFragment reports whether entries contains a line whose pattern
// contains fragment and whose owners include owner (case-insensitive).
func hasOwnedFragment(entries []codeownersEntry, fragment, owner string) bool {
	for _, e := range entries {
		if !strings.Contains(e.Pattern, fragment) {
			continue
		}
		for _, o := range e.Owners {
			if strings.EqualFold(o, owner) {
				return true
			}
		}
	}
	return false
}

// CheckCodeowners implements ADR-0009 point 8(a): .github/CODEOWNERS must
// exist (non-empty) and every RequiredSacredPathFragments entry must be
// registered with RequiredOwner attached. A path pattern present but with its
// owner stripped does NOT count — GitHub's CODEOWNERS semantics treat an
// ownerless pattern as no required review at all for that path, so it is
// indistinguishable from the path being entirely absent for enforcement
// purposes.
func CheckCodeowners(content string) CheckResult {
	if strings.TrimSpace(content) == "" {
		return unmetResult(CheckNameCodeowners, "CODEOWNERS가 비어있거나 존재하지 않음")
	}

	entries := parseCodeowners(content)

	var missing []string
	for _, fragment := range RequiredSacredPathFragments {
		if !hasOwnedFragment(entries, fragment, RequiredOwner) {
			missing = append(missing, fragment)
		}
	}
	if len(missing) > 0 {
		return unmetResult(CheckNameCodeowners, fmt.Sprintf(
			"sacred 경로가 %s 소유로 등록되지 않음(경로 누락 또는 owner 없음): %s",
			RequiredOwner, strings.Join(missing, ", "),
		))
	}
	return metResult(CheckNameCodeowners)
}

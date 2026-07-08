package enforcement

import (
	"fmt"
	"strings"
)

// RequiredOwner is the owner every sacred CODEOWNERS entry must effectively
// resolve to (ADR-0009 point 4).
const RequiredOwner = "@chnu-kim"

// sacredRequiredPaths are concrete, currently-real repo-root-relative file
// paths, one representing each ADR-0009 point 3-4 sacred path. Using real
// files (rather than a synthetic placeholder like "0009-example.md") matters:
// GitHub resolves CODEOWNERS ownership by "last matching pattern wins"
// (entirely, not merged — see codeownersPatternMatches doc), so a later,
// narrower CODEOWNERS entry that strips protection from exactly one real
// sacred file is only caught if the check evaluates that exact file, not a
// made-up stand-in. The risk:critical path-classification mapping file is
// deliberately absent here — ADR-0009 point 4 and the issue tracking this
// check both say its location/format is unconfirmed and out of scope.
//
// These paths are expected to stay stable: ADR-0009's own "수정하지 말고
// 대체한다" convention (docs/adr/README.md) means sacred ADRs are superseded
// by new files, not renamed, and the workflow/CODEOWNERS self-reference paths
// are structural, not content that changes.
var sacredRequiredPaths = []string{
	".github/workflows/ci.yml",
	".github/CODEOWNERS",
	"docs/adr/0004-kill-switch-submit-guard.md",
	"docs/adr/0007-dev-time-autonomy-boundary.md",
	"docs/adr/0008-independent-verification-gate.md",
	"docs/adr/0009-adr-autonomy-sacred-invariant.md",
	"docs/adr/0010-adr-ssot-frontmatter-hybrid.md",
}

// codeownersEntry is one non-comment, non-blank CODEOWNERS line: a path
// pattern and the owners listed after it.
type codeownersEntry struct {
	Pattern string
	Owners  []string
}

// parseCodeowners extracts path/owner entries from raw CODEOWNERS content, in
// file order (order matters — see effectiveOwner), skipping comments (# ...)
// and blank lines. It never errors — CODEOWNERS has no line that is invalid
// to skip; an entry with no owners is still returned (owners will simply be
// empty), because "owner stripped" is itself a meaningful, checkable state.
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

// effectiveOwner returns the owners of the LAST entry (in file order) whose
// pattern matches path, and whether any entry matched at all. This mirrors
// GitHub's actual CODEOWNERS resolution rule — "the last matching pattern
// takes the most precedence" — entirely, not merged with earlier matches. A
// naive "does ANY entry cover this path with the right owner" check can be
// fooled by a later entry that also matches the same path with a different
// (or no) owner; GitHub would use that later entry, not the earlier
// protective-looking one (codex review + adversarial-review both flagged this
// gap in an earlier version of this check).
func effectiveOwner(entries []codeownersEntry, path string) (owners []string, matched bool) {
	for _, e := range entries {
		if codeownersPatternMatches(e.Pattern, path) {
			owners = e.Owners
			matched = true
		}
	}
	return owners, matched
}

func hasOwner(owners []string, owner string) bool {
	for _, o := range owners {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// CheckCodeowners implements ADR-0009 point 8(a): .github/CODEOWNERS must
// exist (non-empty) and, for every sacredRequiredPaths entry, the
// *effectively applicable* CODEOWNERS rule (per GitHub's last-match-wins
// resolution, not just "some line somewhere mentions it") must list
// RequiredOwner. A path with no matching pattern at all, or whose effective
// owner is empty or someone else, does NOT count.
func CheckCodeowners(content string) CheckResult {
	if strings.TrimSpace(content) == "" {
		return unmetResult(CheckNameCodeowners, "CODEOWNERS가 비어있거나 존재하지 않음")
	}

	entries := parseCodeowners(content)

	var problems []string
	for _, path := range sacredRequiredPaths {
		owners, matched := effectiveOwner(entries, path)
		switch {
		case !matched:
			problems = append(problems, fmt.Sprintf("%s: 매칭되는 CODEOWNERS 패턴이 없음", path))
		case !hasOwner(owners, RequiredOwner):
			problems = append(problems, fmt.Sprintf(
				"%s: 최종 적용되는 owner가 %s가 아님(실측: %v) — 이후에 등록된 다른 패턴이 이 경로의 보호를 덮어씀",
				path, RequiredOwner, owners,
			))
		}
	}
	if len(problems) > 0 {
		return unmetResult(CheckNameCodeowners, strings.Join(problems, "; "))
	}
	return metResult(CheckNameCodeowners)
}

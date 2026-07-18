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
// made-up stand-in.
//
// These paths are expected to stay stable: ADR-0009's own "수정하지 말고
// 대체한다" convention (docs/adr/README.md) means sacred ADRs are superseded
// by new files, not renamed, and the workflow/CODEOWNERS self-reference paths
// are structural, not content that changes.
//
// This list is a hand-written mirror of an SSOT that lives elsewhere: ADR
// frontmatter's protects: field (ADR-0010). That mirror is no longer trusted to
// stay in sync by convention — TestADRProtectsCompleteness_RealRepo
// (adrprotects.go) derives the requirement from the frontmatter and fails
// closed if any ADR declaring a non-empty protects: is absent from this slice
// or from .github/CODEOWNERS. Add a new sacred ADR to both surfaces; the test
// will tell you precisely which one you forgot.
//
// The verdict-gate.yml/internal/gate/cmd/verdict-gate/configs/gate entries
// (#48) are the ADR-0011 point 11 registration for the ADR-0008 verdict
// gate's own judgement logic, CLI, and risk-classification mapping: all three
// live outside .github/workflows/** but are executed/read by the privileged
// verdict-generating job, so — per ADR-0011 point 4(b) round 9 ("main에
// 있음 ≠ 보호됨") — they must be CODEOWNERS-protected exactly like the
// workflow YAML itself, not merely present on the default branch.
var sacredRequiredPaths = []string{
	".github/workflows/ci.yml",
	".github/workflows/verdict-gate.yml",
	// The PR-creation workflow (#47, ADR-0011 point 3): presence-check check
	// (c-1) verifies this file EXISTS on main, so check (a) must verify it
	// stays CODEOWNERS-protected — twin-artifact coupling for issue #49. It
	// is covered by the /.github/workflows/ directory rule; listing the real
	// file catches a later, narrower entry stripping exactly it
	// (last-match-wins), same as ci.yml/verdict-gate.yml above.
	".github/workflows/pr-creation.yml",
	".github/CODEOWNERS",
	"docs/adr/0004-kill-switch-submit-guard.md",
	"docs/adr/0007-dev-time-autonomy-boundary.md",
	"docs/adr/0008-independent-verification-gate.md",
	"docs/adr/0009-adr-autonomy-sacred-invariant.md",
	"docs/adr/0010-adr-ssot-frontmatter-hybrid.md",
	"docs/adr/0011-loop-pr-credential-flow.md",
	// ADR-0012 declares protects: [live-execution-human-gate] — it defines the
	// kill-switch durability/ordering contract (durable-before-visible, count-
	// first order-failure), part of the sacred live-execution invariant set
	// like ADR-0004. Same twin-artifact rule as the ADR-0011 registration.
	"docs/adr/0012-killswitch-durability-ordering.md",
	// ADR-0013 declares protects: [live-execution-human-gate] — it pins the
	// kill-switch mirror/transition concurrency model (disjoint block-carriers,
	// single-mu snapshot), part of the same sacred live-execution invariant set
	// as ADR-0004/0012 (a mirror-coherence break is a money-guard fail-open).
	// Same twin-artifact rule (codex review finding on PR #63).
	"docs/adr/0013-killswitch-mirror-concurrency.md",
	// ADR-0014 declares protects: [live-execution-human-gate] — it governs the
	// single reconciler's ambiguous local fail-closed / backlog global
	// escalation / bounded LIVE re-count, completing the kill-switch truth-
	// confirmation the sacred live-execution set (ADR-0004/0012/0013) depends
	// on. Same twin-artifact rule.
	"docs/adr/0014-reconciler-escalation-bounded-recount.md",
	// ADR-0015 declares protects: [enforcement-integrity, live-execution-human-gate]
	// — it fixes the Phase A/B activation procedure (credential ordering,
	// flip-and-verify-or-rollback, bootstrap-exception scope) as an ADR-0011
	// amendment; a later, narrower CODEOWNERS entry stripping its protection would
	// let the loop quietly rewrite the gate-activation contract. Same twin-artifact rule.
	"docs/adr/0015-loop-pr-amendment-bootstrap-activation.md",
	// The Phase B entry runbook (#50) holds the *executable* activation steps
	// (credential narrowing order, App-key provisioning, flip-and-verify-or-
	// rollback, bootstrap prechecks) that ADR-0015 governs by reference. Per
	// ADR-0011 point 4(b) ("main에 있음 ≠ 보호됨"), a gate-defining operational
	// doc must be CODEOWNERS-protected exactly like the ADR — otherwise a later
	// loop PR could reorder provisioning ahead of narrowing or drop rollback
	// without tripping the code-owner gate that protects ADR-0015 itself.
	"docs/runbooks/phase-b-entry.md",
	// The credential-narrowing verifier is the capability-measurement path that
	// decides hard precondition ② ("narrowing complete"). If a loop-controlled
	// change can weaken it, the loop manufactures its own green for the check
	// that is supposed to prove it no longer holds admin/approve capability —
	// a false-green on the linchpin of the whole Phase A/B entry order. Same
	// twin-artifact rule as the runbook (codex adversarial finding on PR #73).
	"scripts/verify-credential-narrowing.sh",
	// Every non-test .go source file in internal/gate, individually — not
	// just one representative file (codex:review [P2] finding on #48's PR:
	// the privileged workflow compiles and executes the whole package via
	// cmd/verdict-gate, so a later, narrower CODEOWNERS entry stripping
	// protection from any ONE of these files — not only
	// riskclassification.go — must be caught).
	"internal/gate/diffparse.go",
	"internal/gate/doc.go",
	"internal/gate/eligibility.go",
	"internal/gate/gateconfig.go",
	"internal/gate/outcome.go",
	"internal/gate/pattern.go",
	"internal/gate/retry.go",
	"internal/gate/riskclassification.go",
	"internal/gate/sanity.go",
	"internal/gate/verdict.go",
	"cmd/verdict-gate/main.go",
	"configs/gate/risk-classification.json",
	// The commit-time/CI secret scanner, its regression suite, and its
	// allowlist manifest (#27). ci.yml runs scan.sh directly against the PR
	// checkout to block leaks, which makes it gate-defining (enforcement)
	// code exactly like internal/gate above — same "main에 있음 ≠ 보호됨"
	// rule (ADR-0011 point 4(b) round 9 / point 11). Unprotected, a PR could
	// neuter the scanner (drop patterns, exit 0) or append an allowlist entry
	// in the same change and ship the very leak this gate exists to stop.
	// The allowlist is listed because it is enforcement *configuration*: an
	// entry there suppresses findings, so adding one must require owner review.
	".claude/skills/opensource-maintainer/scripts/scan.sh",
	".claude/skills/opensource-maintainer/scripts/scan_test.sh",
	".claude/skills/opensource-maintainer/allowlist.txt",
	// Instruction surfaces: the always-loaded policy (CLAUDE.md) and the worker's
	// executable procedure (.claude/agents/) decide whether the gates get INVOKED
	// at all — codex review invocation, ADR-conflict handling, verification gates,
	// and the "promote to gate => register as sacred" rule itself all live there.
	// Leaving them unprotected is a path to turning a gate off without touching a
	// single line of gate code. Caught by codex adversarial review on PR #81: the
	// new rule had not been applied to the files that carry it.
	".claude/agents/go-tdd-implementer.md",
	"CLAUDE.md",
	// Orchestration skills: protecting the worker leaf while leaving the procedure
	// that INVOKES it unowned turns the gate off just as effectively. dispatch-issue
	// decides issue eligibility, risk:critical stop conditions, account pinning,
	// worktree isolation, mandatory worker delegation, PR handoff and cleanup;
	// codex-pr-review decides the review invocation itself (both channels, base ref).
	// Both pass the rule's own test: "would editing this change a gate verdict or its
	// evidence?" (codex adversarial review on PR #81, R3 — same class, fifth instance.)
	".claude/skills/dispatch-issue/SKILL.md",
	".claude/skills/opensource-maintainer/SKILL.md",
	// This package itself (#64). The checker that decides whether the sacred
	// paths are protected was, until now, the one gate-defining component with
	// no protection of its own: neither .github/CODEOWNERS nor this slice
	// mentioned internal/enforcement. That is a hollow gate — a loop able to
	// edit this code can delete an entry from this very slice, or weaken
	// CheckCodeowners/effectiveOwner, and thereby open the code-owner gate for
	// everything else without a single code-owner review. ADR-0009 point 3's
	// adversarial-loop threat model is exactly this case, and it is the same
	// class as (but more fundamental than) the internal/gate, phase-b-entry
	// runbook and scripts/ registrations: "main에 있음 ≠ 보호됨"
	// (ADR-0011 point 4(b)).
	//
	// This is NOT circular self-approval. The slice only asserts "does
	// CODEOWNERS still keep these paths owned by RequiredOwner" — it grants no
	// merge right, approves nothing, and cannot let a PR through. The actual
	// blocking power stays where ADR-0009 point 4 put it: GitHub branch
	// protection plus a human code-owner review, layers a loop cannot edit.
	// This check can only ever report that protection is gone; it can never
	// supply protection to itself.
	//
	// Listed file-by-file rather than leaning on the /internal/enforcement/
	// directory rule alone, following the internal/gate precedent: ownership
	// resolves last-match-wins, so a later, narrower ownerless entry can strip
	// exactly one file while the directory rule still looks protective.
	// TestSacredRequiredPaths_CoversEveryEnforcementGoFile keeps this list
	// complete as the package grows.
	//
	// _test.go files are included here, unlike internal/gate. There the gate
	// logic sits in non-test files and the tests merely verify it; here the
	// enforcement IS the tests — TestADRProtectsCompleteness_RealRepo,
	// TestSacredADRRegistry_* and TestSacredRequiredPaths_* are the only things
	// standing between this repo and silent de-wiring, and nothing else
	// re-checks them. Omitting them would leave the completeness suite
	// strippable by a later ownerless entry while CheckCodeowners still passed,
	// since an unlisted path is never evaluated (codex adversarial-review
	// [high] on PR #74).
	"internal/enforcement/adrprotects.go",
	"internal/enforcement/adrprotects_test.go",
	"internal/enforcement/branchprotection.go",
	"internal/enforcement/branchprotection_test.go",
	"internal/enforcement/codeowners.go",
	"internal/enforcement/codeowners_pattern.go",
	"internal/enforcement/codeowners_pattern_test.go",
	"internal/enforcement/codeowners_test.go",
	"internal/enforcement/doc.go",
	"internal/enforcement/github_client.go",
	"internal/enforcement/identity.go",
	"internal/enforcement/identity_test.go",
	"internal/enforcement/instructionsurface_test.go",
	"internal/enforcement/presence.go",
	"internal/enforcement/presence_test.go",
	"internal/enforcement/protectedbranch_test.go",
	"internal/enforcement/pullrequest.go",
	"internal/enforcement/repocontent.go",
	"internal/enforcement/repocontent_test.go",
	"internal/enforcement/result.go",
	"internal/enforcement/workflowrevision.go",
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

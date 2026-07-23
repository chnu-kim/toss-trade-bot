package enforcement

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file derives the enforcement-completeness invariant from ADR frontmatter
// instead of restating it by hand.
//
// ADR-0010 makes the `protects:` frontmatter field the single source of truth
// for "which ADRs guard a sacred invariant". ADR-0009 makes the actual
// protection static: a path listed in sacredRequiredPaths (so CheckCodeowners
// evaluates it) AND covered by an owning .github/CODEOWNERS rule. Those two
// enforcement surfaces are hand-mirrored from the SSOT, and hand mirrors drift:
// the "declared protects: but forgot to wire it" coupling failure recurred six
// times in this repo and was caught by an external reviewer every single time,
// never by the repo itself — because the existing CheckCodeowners is a
// *presence* check (are the listed paths owned?) and never asks the
// *completeness* question (is every declaring ADR listed at all?).
//
// The helpers here answer that second question mechanically. They are
// deliberately fail-closed: anything unparseable is an error, never an implicit
// "declares nothing, so nothing is required" — a fail-open would let a mangled
// sacred ADR drop silently out of the assertion, which is exactly the class of
// silent hole this check exists to remove.
//
// # Trust-model boundary (read this before extending)
//
// This is a DRIFT DETECTOR, not an enforcer. Do not read a green build here as
// "the sacred invariants are protected" — that guarantee comes from CODEOWNERS
// plus branch protection and from nothing else (ADR-0009 point 4). Concretely:
//
//   - It never decides whether a PR may merge from the *semantic content* of a
//     protects: value. It only asserts set equality between a declaration and
//     its two registrations. ADR-0010 point 4 rejects the former ("status/
//     protects 값 자체를 근거로 merge를 막거나 허용하는 판단은 CI가 하지 않는다"
//     — circular, because CI is loop-editable) while explicitly assigning the
//     latter to CI in the same sentence ("CI는 오직 구조적 정합성만 검증한다 —
//     ... 참조 무결성"). A declaration with no matching registration is a
//     dangling reference, the same species as supersedes/superseded_by
//     referential integrity, and ADR-0010's Consequences anticipates exactly
//     this ("향후 ... 자동 점검을 domain/protects 필드 기반으로 확장할 수 있다").
//   - It is not load-bearing. Deleting these checks removes the alarm, not the
//     protection: CODEOWNERS and branch protection stand unchanged, and the
//     repo simply returns to the status quo in which this drift recurred six
//     times unnoticed. That is why the circularity argument against
//     CI-as-enforcer does not apply here — there is no protection to be
//     circular about.
//   - Consequently these helpers stay OUT of the presence-check pipeline: they
//     produce no CheckResult and no exported Check* function consults them.
//     CheckCodeowners' verdict is byte-for-byte unaffected by ADR frontmatter.
//     Wiring frontmatter values into a runtime autonomy verdict WOULD be the
//     rejected design, and would need an ADR amendment first.

// adrDirRel is the repo-root-relative directory holding ADRs. It matches the
// docs/adr/ prefix used by sacredRequiredPaths.
const adrDirRel = "docs/adr"

// sacredADRRegistry is the historical roster of ADR ids that declare a
// non-empty protects: list. It is checked in BOTH directions
// (TestSacredADRRegistry_*), which is what makes it self-maintaining rather
// than one more hand-mirror of the kind this file exists to police:
//
//   - Every id here must still declare a non-empty protects:. This catches
//     de-wiring, which the frontmatter-derived checks structurally cannot: an
//     ADR that loses its protects: AND its sacredRequiredPaths entry AND its
//     CODEOWNERS line disappears from both directions at once and leaves them
//     vacuously green (codex adversarial-review [high] on PR #74).
//   - Every currently-declaring ADR must appear here. Without this the roster
//     would only ever cover ADRs that existed when it was written — exactly the
//     drift that made the ten hand-written per-ADR fixtures fail — so a sacred
//     ADR added later would never gain de-wiring protection.
//
// The practical effect is that removing a decision from the sacred set can no
// longer be a silent deletion: it must show up in a diff here, next to a
// comment saying so, in the same PR that edits the (code-owner protected) ADR.
//
// Known limitation, deliberately not fixed here: this file is not itself a
// CODEOWNERS-protected path, so the roster raises the visibility of a de-wiring
// PR rather than gating it. Moving it under CODEOWNERS protection is a wiring
// change (a new sacred path), which is out of scope for issue #64 — that PR
// touches enforcement wiring only, by explicit instruction.
var sacredADRRegistry = []string{
	"0004", // live-execution-human-gate — kill-switch submit guard
	"0007", // live-execution-human-gate — dev-time autonomy boundary
	"0008", // enforcement-integrity — independent verification gate
	"0009", // live-execution-human-gate, enforcement-integrity — the sacred carve-out itself
	"0010", // enforcement-integrity — frontmatter SSOT
	"0011", // enforcement-integrity — loop PR credential flow
	"0012", // live-execution-human-gate — kill-switch durability/ordering
	"0013", // live-execution-human-gate — kill-switch mirror concurrency
	"0014", // live-execution-human-gate — reconciler escalation / bounded re-count
	"0015", // enforcement-integrity, live-execution-human-gate — Phase A/B activation
	"0016", // enforcement-integrity — Phase B activation ordering / enforce_admins target / #76 disposition
	"0017", // enforcement-integrity — protection-flip pure transformer charter
}

// adrFileNameRE matches the canonical ADR filename form (docs/adr/README.md and
// why-adr.md are companion docs, not ADRs, and carry no frontmatter).
var adrFileNameRE = regexp.MustCompile(`^(\d{4})-.+\.md$`)

// adrDecl is one ADR file and the sacred invariants its frontmatter declares.
type adrDecl struct {
	// Path is repo-root-relative with "/" separators, e.g.
	// "docs/adr/0013-killswitch-mirror-concurrency.md" — the same form
	// sacredRequiredPaths and codeownersPatternMatches use.
	Path string
	// Protects is the frontmatter `protects:` list; empty means the ADR guards
	// no sacred invariant and therefore requires no enforcement wiring.
	Protects []string
}

// adrIDFromPath returns the four-digit ADR id encoded in a path, or "" if the
// path is not a canonical ADR filename.
func adrIDFromPath(path string) string {
	m := adrFileNameRE.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return ""
	}
	return m[1]
}

// loadADRDecls reads every canonical ADR under repoRoot/docs/adr and returns its
// declared protects list. Any ADR whose frontmatter cannot be parsed is an error
// (fail-closed), as is finding no ADRs at all — that means the directory was
// resolved wrongly and a silently empty result would make every completeness
// assertion vacuously true.
func loadADRDecls(repoRoot string) ([]adrDecl, error) {
	dir := filepath.Join(repoRoot, filepath.FromSlash(adrDirRel))
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read ADR dir %s: %w", dir, err)
	}

	var decls []adrDecl
	for _, e := range names {
		if e.IsDir() || !adrFileNameRE.MatchString(e.Name()) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read ADR %s: %w", e.Name(), err)
		}
		protects, err := parseADRProtects(string(content))
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", adrDirRel, e.Name(), err)
		}
		decls = append(decls, adrDecl{Path: adrDirRel + "/" + e.Name(), Protects: protects})
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("%s 아래에서 ADR을 하나도 찾지 못함 — 경로 해석 오류로 보임", dir)
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Path < decls[j].Path })
	return decls, nil
}

var frontmatterKeyRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):(.*)$`)

// parseADRProtects extracts the top-level `protects:` list from a markdown
// file's YAML frontmatter.
//
// Only the frontmatter block (between the leading "---" and its closing "---")
// is considered, and within it only *top-level* keys (column 0). Both matter on
// this repo's real ADRs: "protects:" also appears inside indented
// `verification:` verdict prose and all over ADR body text, so a document-wide
// grep would read a reviewer's sentence as the SSOT.
//
// Supported value forms (YAML sequences):
//
//	protects: []
//	protects: [a]
//	protects: [a, b]          (elements may be quoted)
//	protects:
//	  - a
//	  - b
//
// Anything else — no frontmatter, no closing delimiter, a missing or duplicated
// protects key, a scalar value, an unterminated array, an empty element — is an
// error. Returning "no protects" for input this parser does not understand
// would silently exempt that ADR from the completeness check.
func parseADRProtects(content string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return nil, fmt.Errorf("frontmatter 시작 구분자('---')로 시작하지 않음")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("frontmatter 종료 구분자('---')가 없음")
	}
	body := lines[1:end]

	found := false
	var protects []string
	for i := 0; i < len(body); i++ {
		m := frontmatterKeyRE.FindStringSubmatch(body[i])
		if m == nil || m[1] != "protects" {
			continue
		}
		if found {
			return nil, fmt.Errorf("frontmatter에 top-level protects 키가 둘 이상")
		}
		found = true

		value := strings.TrimSpace(m[2])
		if value == "" {
			// Block sequence: the items are the following indented lines, up to
			// the next top-level key.
			items, err := parseProtectsBlock(body[i+1:])
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				// A bare "protects:" is YAML null, not an empty list.
				// docs/adr/README.md defines the field as an array and requires
				// an explicit [] when nothing is protected, so reading null as
				// "declares nothing" would be a fail-open: a malformed new
				// sacred ADR would dodge both sacredRequiredPaths and
				// CODEOWNERS while the completeness check sees nothing to
				// enforce (codex bot [P2] on PR #74).
				return nil, fmt.Errorf("protects 값이 비어있음(YAML null) — 보호 대상이 없으면 빈 배열([])로 명시하라(docs/adr/README.md)")
			}
			protects = items
			continue
		}
		items, err := parseProtectsInlineArray(value)
		if err != nil {
			return nil, err
		}
		protects = items
	}
	if !found {
		return nil, fmt.Errorf("frontmatter에 top-level protects 키가 없음(ADR-0010은 필수로 요구)")
	}
	return protects, nil
}

// parseProtectsBlock reads a YAML block sequence following a bare "protects:",
// stopping at the next top-level key.
func parseProtectsBlock(rest []string) ([]string, error) {
	var items []string
	for _, line := range rest {
		if frontmatterKeyRE.MatchString(line) {
			break // next top-level key
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			return nil, fmt.Errorf("protects 블록에서 해석할 수 없는 줄: %q", line)
		}
		item, err := normalizeProtectsItem(strings.TrimPrefix(trimmed, "- "))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// parseProtectsInlineArray parses "[a, b]" (optionally followed by a comment).
func parseProtectsInlineArray(value string) ([]string, error) {
	if !strings.HasPrefix(value, "[") {
		return nil, fmt.Errorf("protects 값이 YAML 배열이 아님: %q", value)
	}
	close := strings.LastIndex(value, "]")
	if close < 0 {
		return nil, fmt.Errorf("protects 배열이 ']'로 닫히지 않음: %q", value)
	}
	if tail := strings.TrimSpace(value[close+1:]); tail != "" && !strings.HasPrefix(tail, "#") {
		return nil, fmt.Errorf("protects 배열 뒤에 해석할 수 없는 내용: %q", tail)
	}
	inner := strings.TrimSpace(value[1:close])
	if inner == "" {
		return nil, nil
	}
	if strings.ContainsAny(inner, "[]{}") {
		return nil, fmt.Errorf("protects 배열에 중첩 구조가 있어 해석할 수 없음: %q", value)
	}
	var items []string
	for _, raw := range strings.Split(inner, ",") {
		item, err := normalizeProtectsItem(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func normalizeProtectsItem(raw string) (string, error) {
	item := strings.TrimSpace(raw)
	if len(item) >= 2 {
		if (item[0] == '"' && item[len(item)-1] == '"') || (item[0] == '\'' && item[len(item)-1] == '\'') {
			item = item[1 : len(item)-1]
		}
	}
	item = strings.TrimSpace(item)
	if item == "" {
		return "", fmt.Errorf("protects 항목이 비어있음")
	}
	return item, nil
}

// adrProtectsWiringProblems reports every ADR that declares a non-empty
// protects list but is not registered on BOTH enforcement surfaces:
//
//	(a) sacredRequiredPaths — without it CheckCodeowners never evaluates the
//	    path, so the presence-check stays green no matter what CODEOWNERS says;
//	(b) .github/CODEOWNERS — evaluated through effectiveOwner, i.e. GitHub's
//	    last-match-wins rule. "The path is mentioned somewhere" is NOT enough:
//	    a later, narrower entry that drops or replaces the owner is what
//	    actually applies, and treating the earlier protective-looking line as
//	    proof would be a false green.
//
// Each problem names the ADR and the specific missing surface; an ADR missing
// from both yields two problems, so the message never under-reports the work.
func adrProtectsWiringProblems(decls []adrDecl, sacred []string, coEntries []codeownersEntry) []string {
	inSacred := make(map[string]bool, len(sacred))
	for _, p := range sacred {
		inSacred[p] = true
	}

	var problems []string
	for _, d := range decls {
		if len(d.Protects) == 0 {
			continue
		}
		if !inSacred[d.Path] {
			problems = append(problems, fmt.Sprintf(
				"%s: protects: %v 를 선언했으나 internal/enforcement/codeowners.go 의 sacredRequiredPaths 에 미등재 — CheckCodeowners가 이 경로를 아예 검사하지 않는다",
				d.Path, d.Protects))
		}
		owners, matched := effectiveOwner(coEntries, d.Path)
		switch {
		case !matched:
			problems = append(problems, fmt.Sprintf(
				"%s: protects: %v 를 선언했으나 .github/CODEOWNERS 에 매칭되는 패턴이 없음 — code-owner 리뷰가 요구되지 않는다",
				d.Path, d.Protects))
		case !hasOwner(owners, RequiredOwner):
			problems = append(problems, fmt.Sprintf(
				".github/CODEOWNERS: %s 에 최종 적용되는 owner가 %s가 아님(실측: %v) — 후행 패턴이 이 protects 선언의 보호를 덮어씀",
				d.Path, RequiredOwner, owners))
		}
	}
	return problems
}

// sacredADRPathProblems checks the reverse direction for docs/adr entries in
// sacredRequiredPaths: the file must still exist and must still declare a
// non-empty protects list.
//
// Without this, protection can evaporate with every check green. CODEOWNERS
// patterns and sacredRequiredPaths both match *paths*, not files, so renaming
// or deleting a sacred ADR leaves CheckCodeowners passing on a path nothing
// lives at, while the forward completeness check simply stops seeing the ADR.
// Emptying a protects: declaration is the same hole from the other end.
//
// # Ownership boundary with the non-ADR sacred paths
//
// Only docs/adr entries are checked here. Every other sacred path — workflows,
// the phase-b-entry runbook, scripts/, gate code, this package itself, and the
// secret scanner/allowlist registered by #27 — has no frontmatter, therefore no
// protects: SSOT to derive completeness from, and is deliberately NOT this
// check's business. Each of those keeps its own dedicated fail-closed test
// (TestCheckCodeowners_MissingSecretScannerFailsClosed for the scanner,
// TestSacredRequiredPaths_CoversEveryEnforcementGoFile for this package, and so
// on).
//
// The two layers are complementary, not redundant: this one answers "is every
// ADR that *declares* protection actually wired?", those answer "is this
// specific known path still owned?". Nothing is asserted twice, and no sacred
// path falls between them — a path with frontmatter belongs here, a path
// without belongs to its own test.
func sacredADRPathProblems(decls []adrDecl, sacred []string) []string {
	byPath := make(map[string]adrDecl, len(decls))
	for _, d := range decls {
		byPath[d.Path] = d
	}

	var problems []string
	for _, p := range sacred {
		if !strings.HasPrefix(p, adrDirRel+"/") {
			continue
		}
		d, ok := byPath[p]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s: sacredRequiredPaths에 등재돼 있으나 실제 ADR 파일이 없음(리네임/삭제?) — 경로 매칭은 계속 통과하므로 보호가 조용히 사라진다",
				p))
			continue
		}
		if len(d.Protects) == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s: sacredRequiredPaths에 등재돼 있으나 frontmatter protects: 가 비어있음 — SSOT(ADR-0010)와 강제 배선이 어긋난다",
				p))
		}
	}
	return problems
}

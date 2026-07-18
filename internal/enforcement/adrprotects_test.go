package enforcement

import (
	"os"
	"strings"
	"testing"
)

// repoRootForTest is the repository root relative to this package's directory
// (internal/enforcement), which is where `go test` runs.
const repoRootForTest = "../.."

// loadRealCodeownersEntries parses the repository's actual .github/CODEOWNERS —
// the file GitHub itself will evaluate. Synthetic fixtures cannot prove the
// real wiring is complete.
func loadRealCodeownersEntries() ([]codeownersEntry, error) {
	content, err := os.ReadFile(repoRootForTest + "/.github/CODEOWNERS")
	if err != nil {
		return nil, err
	}
	return parseCodeowners(string(content)), nil
}

// --- frontmatter parsing (ADR-0010: frontmatter is the SSOT for protects) ---

func TestParseADRProtects_Forms(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name: "empty inline array",
			content: `---
id: "0001"
protects: []
supersedes: []
---

# body
`,
			want: nil,
		},
		{
			name: "single element inline array",
			content: `---
id: "0004"
protects: [live-execution-human-gate]
---
`,
			want: []string{"live-execution-human-gate"},
		},
		{
			name: "two element inline array",
			content: `---
id: "0015"
protects: [enforcement-integrity, live-execution-human-gate]
---
`,
			want: []string{"enforcement-integrity", "live-execution-human-gate"},
		},
		{
			name: "quoted elements",
			content: `---
id: "0009"
protects: ["live-execution-human-gate", 'enforcement-integrity']
---
`,
			want: []string{"live-execution-human-gate", "enforcement-integrity"},
		},
		{
			name: "multiline block sequence",
			content: `---
id: "0013"
protects:
  - live-execution-human-gate
  - enforcement-integrity
supersedes: []
---
`,
			want: []string{"live-execution-human-gate", "enforcement-integrity"},
		},
		{
			name: "multiline block with no items is empty",
			content: `---
id: "0002"
protects:
supersedes: []
---
`,
			want: nil,
		},
		{
			name: "inline array with trailing comment",
			content: `---
protects: [enforcement-integrity] # registered in CODEOWNERS
---
`,
			want: []string{"enforcement-integrity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseADRProtects(tt.content)
			if err != nil {
				t.Fatalf("parseADRProtects() error = %v, want nil", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseADRProtects() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseADRProtects() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestParseADRProtects_OnlyTopLevelFrontmatterKeyCounts guards the two ways a
// naive "grep protects:" parser goes wrong on this repo's real ADRs: the word
// appears inside indented `verification:` verdict prose (ADR-0013/0015) and
// throughout ADR body text (ADR-0009/0010 discuss the field at length). Only
// the top-level frontmatter key is the SSOT.
func TestParseADRProtects_OnlyTopLevelFrontmatterKeyCounts(t *testing.T) {
	content := `---
id: "0013"
protects: [live-execution-human-gate]
verification:
  - reviewer: architect
    verdict: enforcement P1(protects 선언에 CODEOWNERS twin-artifact 배선 누락). protects: [bogus-from-prose]
---

# ADR-0013

본문에서 protects: [also-bogus] 를 언급한다.
`
	got, err := parseADRProtects(content)
	if err != nil {
		t.Fatalf("parseADRProtects() error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "live-execution-human-gate" {
		t.Fatalf("parseADRProtects() = %v, want [live-execution-human-gate]", got)
	}
}

// TestParseADRProtects_FailsClosed: this check exists to catch missing wiring,
// so anything it cannot understand must be an error — never "no protects, so
// nothing required" (that would be a fail-open: an unparseable sacred ADR
// would silently drop out of the completeness assertion).
func TestParseADRProtects_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "no frontmatter at all", content: "# ADR without frontmatter\n"},
		{name: "unterminated frontmatter", content: "---\nid: \"0001\"\nprotects: []\n"},
		{name: "protects key missing", content: "---\nid: \"0001\"\nsupersedes: []\n---\n"},
		{name: "scalar value instead of array", content: "---\nprotects: enforcement-integrity\n---\n"},
		{name: "unterminated inline array", content: "---\nprotects: [enforcement-integrity\n---\n"},
		{name: "garbage after closing bracket", content: "---\nprotects: [a] bogus\n---\n"},
		{name: "empty element in array", content: "---\nprotects: [a, ]\n---\n"},
		{name: "duplicate protects keys", content: "---\nprotects: []\nprotects: [a]\n---\n"},
		{name: "unrecognised block item", content: "---\nprotects:\n  bogus: value\n---\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseADRProtects(tt.content)
			if err == nil {
				t.Fatalf("parseADRProtects() = %v, want error (fail-closed)", got)
			}
		})
	}
}

// --- completeness: protects (SSOT) -> sacredRequiredPaths + CODEOWNERS ---

const completenessCodeowners = `/docs/adr/0004-*.md @chnu-kim
/docs/adr/0013-*.md @chnu-kim
/.github/CODEOWNERS @chnu-kim
`

func TestADRProtectsWiringProblems_FullyWiredIsClean(t *testing.T) {
	decls := []adrDecl{
		{Path: "docs/adr/0004-kill-switch-submit-guard.md", Protects: []string{"live-execution-human-gate"}},
		{Path: "docs/adr/0013-killswitch-mirror-concurrency.md", Protects: []string{"live-execution-human-gate"}},
		// protects: [] — deliberately wired nowhere; must not be required.
		{Path: "docs/adr/0001-single-flight-token-refresh.md"},
	}
	sacred := []string{
		"docs/adr/0004-kill-switch-submit-guard.md",
		"docs/adr/0013-killswitch-mirror-concurrency.md",
	}
	problems := adrProtectsWiringProblems(decls, sacred, parseCodeowners(completenessCodeowners))
	if len(problems) != 0 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want none", problems)
	}
}

func TestADRProtectsWiringProblems_MissingFromSacredRequiredPaths(t *testing.T) {
	// The exact recurrence this issue closes (6+ occurrences, always caught by
	// an external reviewer, never by this repo itself): an ADR declares
	// protects: [...] and the CODEOWNERS line is added, but the
	// sacredRequiredPaths mirror is forgotten — so CheckCodeowners never even
	// evaluates that path and the presence-check stays green.
	decls := []adrDecl{
		{Path: "docs/adr/0013-killswitch-mirror-concurrency.md", Protects: []string{"live-execution-human-gate"}},
	}
	problems := adrProtectsWiringProblems(decls, nil, parseCodeowners(completenessCodeowners))
	if len(problems) != 1 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want exactly 1 problem", problems)
	}
	if !strings.Contains(problems[0], "0013") || !strings.Contains(problems[0], "sacredRequiredPaths") {
		t.Fatalf("problem message must name the ADR and the missing surface, got %q", problems[0])
	}
}

func TestADRProtectsWiringProblems_MissingFromCodeowners(t *testing.T) {
	decls := []adrDecl{
		{Path: "docs/adr/0016-not-in-codeowners.md", Protects: []string{"enforcement-integrity"}},
	}
	sacred := []string{"docs/adr/0016-not-in-codeowners.md"}
	problems := adrProtectsWiringProblems(decls, sacred, parseCodeowners(completenessCodeowners))
	if len(problems) != 1 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want exactly 1 problem", problems)
	}
	if !strings.Contains(problems[0], "0016") || !strings.Contains(problems[0], "CODEOWNERS") {
		t.Fatalf("problem message must name the ADR and the missing surface, got %q", problems[0])
	}
}

func TestADRProtectsWiringProblems_NamesBothMissingSurfaces(t *testing.T) {
	decls := []adrDecl{
		{Path: "docs/adr/0016-wired-nowhere.md", Protects: []string{"enforcement-integrity"}},
	}
	problems := adrProtectsWiringProblems(decls, nil, parseCodeowners(completenessCodeowners))
	if len(problems) != 2 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want 2 problems (one per surface)", problems)
	}
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "sacredRequiredPaths") || !strings.Contains(joined, "CODEOWNERS") {
		t.Fatalf("both surfaces must be named, got %q", joined)
	}
}

// TestADRProtectsWiringProblems_LaterEntryStrippingOwnerIsCaught is the
// false-green guard: GitHub resolves CODEOWNERS by last-match-wins, so a
// "somewhere it is mentioned" check would pass a file whose protection a later
// pattern silently removes. This must reuse effectiveOwner, not substring
// matching.
func TestADRProtectsWiringProblems_LaterEntryStrippingOwnerIsCaught(t *testing.T) {
	content := `/docs/adr/ @chnu-kim
/docs/adr/0013-killswitch-mirror-concurrency.md
/.github/CODEOWNERS @chnu-kim
`
	decls := []adrDecl{
		{Path: "docs/adr/0013-killswitch-mirror-concurrency.md", Protects: []string{"live-execution-human-gate"}},
	}
	sacred := []string{"docs/adr/0013-killswitch-mirror-concurrency.md"}
	problems := adrProtectsWiringProblems(decls, sacred, parseCodeowners(content))
	if len(problems) != 1 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want 1 problem (owner stripped by later entry)", problems)
	}
	if !strings.Contains(problems[0], "CODEOWNERS") {
		t.Fatalf("problem must point at the CODEOWNERS surface, got %q", problems[0])
	}
}

func TestADRProtectsWiringProblems_LaterEntryWithDifferentOwnerIsCaught(t *testing.T) {
	content := `/docs/adr/ @chnu-kim
/docs/adr/0013-killswitch-mirror-concurrency.md @someone-else
/.github/CODEOWNERS @chnu-kim
`
	decls := []adrDecl{
		{Path: "docs/adr/0013-killswitch-mirror-concurrency.md", Protects: []string{"live-execution-human-gate"}},
	}
	sacred := []string{"docs/adr/0013-killswitch-mirror-concurrency.md"}
	problems := adrProtectsWiringProblems(decls, sacred, parseCodeowners(content))
	if len(problems) != 1 {
		t.Fatalf("adrProtectsWiringProblems() = %v, want 1 problem (owner replaced by later entry)", problems)
	}
}

// --- reverse direction: a wired ADR path must still exist and still declare ---

func TestSacredADRPathProblems_RenamedOrDeletedADRIsCaught(t *testing.T) {
	// If a sacred ADR file is renamed/deleted, its sacredRequiredPaths entry
	// keeps passing CheckCodeowners (patterns match paths, not files) while the
	// forward completeness check simply stops seeing it — protection evaporates
	// with both checks green. This closes that hole.
	decls := []adrDecl{
		{Path: "docs/adr/0013-renamed.md", Protects: []string{"live-execution-human-gate"}},
	}
	sacred := []string{"docs/adr/0013-killswitch-mirror-concurrency.md"}
	problems := sacredADRPathProblems(decls, sacred)
	if len(problems) != 1 {
		t.Fatalf("sacredADRPathProblems() = %v, want 1 problem", problems)
	}
	if !strings.Contains(problems[0], "0013-killswitch-mirror-concurrency.md") {
		t.Fatalf("problem must name the vanished path, got %q", problems[0])
	}
}

func TestSacredADRPathProblems_WiredButProtectsEmptyIsCaught(t *testing.T) {
	decls := []adrDecl{
		{Path: "docs/adr/0013-killswitch-mirror-concurrency.md"},
	}
	sacred := []string{"docs/adr/0013-killswitch-mirror-concurrency.md"}
	problems := sacredADRPathProblems(decls, sacred)
	if len(problems) != 1 {
		t.Fatalf("sacredADRPathProblems() = %v, want 1 problem (protects emptied)", problems)
	}
	if !strings.Contains(problems[0], "protects") {
		t.Fatalf("problem must name the emptied field, got %q", problems[0])
	}
}

func TestSacredADRPathProblems_NonADRPathsIgnored(t *testing.T) {
	sacred := []string{
		".github/workflows/ci.yml",
		"scripts/verify-credential-narrowing.sh",
		"docs/runbooks/phase-b-entry.md",
		"internal/gate/verdict.go",
	}
	if problems := sacredADRPathProblems(nil, sacred); len(problems) != 0 {
		t.Fatalf("sacredADRPathProblems() = %v, want none (non-ADR sacred paths are out of scope)", problems)
	}
}

// --- the real repo ---

func TestLoadADRDecls_RealRepoParsesEveryADR(t *testing.T) {
	decls, err := loadADRDecls(repoRootForTest)
	if err != nil {
		t.Fatalf("loadADRDecls() error = %v (fail-closed: an unparseable ADR is a failure, not an exemption)", err)
	}
	if len(decls) < 10 {
		t.Fatalf("loadADRDecls() returned %d ADRs — too few; the glob is probably resolving the wrong directory", len(decls))
	}
	for _, d := range decls {
		if !strings.HasPrefix(d.Path, adrDirRel+"/") {
			t.Fatalf("decl path %q must be repo-root-relative", d.Path)
		}
	}
	// docs/adr also holds non-ADR markdown (README.md, why-adr.md) which has no
	// frontmatter; only NNNN-*.md files are ADRs.
	for _, d := range decls {
		if strings.HasSuffix(d.Path, "/README.md") || strings.HasSuffix(d.Path, "/why-adr.md") {
			t.Fatalf("non-ADR markdown %q must not be treated as an ADR", d.Path)
		}
	}
}

// TestADRProtectsCompleteness_RealRepo is the invariant this issue exists for:
// every ADR whose frontmatter declares a non-empty protects list must be wired
// into BOTH enforcement surfaces. It replaces the per-ADR hardcoded
// TestCheckCodeowners_Missing00NNFailsClosed fixtures, which had to be
// hand-written for each new sacred ADR and never caught a *missing* one.
func TestADRProtectsCompleteness_RealRepo(t *testing.T) {
	decls, err := loadADRDecls(repoRootForTest)
	if err != nil {
		t.Fatalf("loadADRDecls() error = %v", err)
	}
	entries, err := loadRealCodeownersEntries()
	if err != nil {
		t.Fatalf("read real CODEOWNERS: %v", err)
	}

	if problems := adrProtectsWiringProblems(decls, sacredRequiredPaths, entries); len(problems) > 0 {
		t.Fatalf("protects 선언과 강제 배선이 어긋남(twin-artifact):\n  - %s",
			strings.Join(problems, "\n  - "))
	}
	if problems := sacredADRPathProblems(decls, sacredRequiredPaths); len(problems) > 0 {
		t.Fatalf("sacredRequiredPaths의 ADR 항목이 SSOT와 어긋남:\n  - %s",
			strings.Join(problems, "\n  - "))
	}
}

// TestADRProtectsCompleteness_GrantsNoAutonomy makes the trust-model boundary
// executable rather than merely documented (codex adversarial-review on PR #74
// raised the split-brain risk: a future reader treating this CI check as the
// sacred-invariant guarantee that ADR-0010 point 4 says only CODEOWNERS +
// branch protection can provide).
//
// Fully-wired ADR frontmatter must NOT make the real enforcement check pass. If
// a later change ever routes frontmatter into CheckCodeowners' verdict, this
// test fails — which is the moment an ADR amendment is required, per ADR-0010's
// rejection of "protects 값을 CI가 읽고 merge 허용/차단을 직접 결정".
func TestADRProtectsCompleteness_GrantsNoAutonomy(t *testing.T) {
	decls, err := loadADRDecls(repoRootForTest)
	if err != nil {
		t.Fatalf("loadADRDecls() error = %v", err)
	}
	entries, err := loadRealCodeownersEntries()
	if err != nil {
		t.Fatalf("read real CODEOWNERS: %v", err)
	}
	// Precondition: the completeness invariant currently holds.
	if problems := adrProtectsWiringProblems(decls, sacredRequiredPaths, entries); len(problems) > 0 {
		t.Fatalf("precondition broken, completeness must be clean here: %v", problems)
	}

	// Despite that, a repository whose CODEOWNERS is empty or has lost the
	// sacred entries must still fail the actual gate. The drift detector buys
	// no credit toward it.
	if got := CheckCodeowners(""); got.Satisfied {
		t.Fatal("완결성이 green이어도 빈 CODEOWNERS는 통과해선 안 된다 — 이 체크는 게이트가 아니다")
	}
	if got := CheckCodeowners("/docs/adr/0004-*.md @chnu-kim\n"); got.Satisfied {
		t.Fatal("완결성이 green이어도 불완전한 CODEOWNERS는 통과해선 안 된다 — 실제 강제는 CODEOWNERS+branch protection")
	}
}

// currentlyDeclaringADRIDs returns the ids of every ADR whose frontmatter
// declares a non-empty protects: list.
func currentlyDeclaringADRIDs(t *testing.T) map[string]bool {
	t.Helper()
	decls, err := loadADRDecls(repoRootForTest)
	if err != nil {
		t.Fatalf("loadADRDecls() error = %v", err)
	}
	declaring := map[string]bool{}
	for _, d := range decls {
		if len(d.Protects) > 0 {
			declaring[adrIDFromPath(d.Path)] = true
		}
	}
	return declaring
}

// TestSacredADRRegistry_NoDeclarationSilentlyRemoved is the de-wiring guard.
//
// The frontmatter-derived checks are structurally blind to a simultaneous
// three-way removal: drop an ADR's protects:, its sacredRequiredPaths entry and
// its CODEOWNERS line in one PR and the forward check skips it (nothing
// declared), the reverse check skips it (nothing registered), and protection is
// gone with everything green. The registry is the only artifact that remembers
// the decision was sacred.
func TestSacredADRRegistry_NoDeclarationSilentlyRemoved(t *testing.T) {
	declaring := currentlyDeclaringADRIDs(t)
	for _, id := range sacredADRRegistry {
		if !declaring[id] {
			t.Errorf("ADR-%s가 sacredADRRegistry에 있으나 frontmatter protects: 가 비어있거나 사라짐 — sacred 집합에서 조용히 빠졌다. 의도된 제거라면 registry에서도 근거와 함께 지워라(그 diff가 곧 사람 검토 지점이다)", id)
		}
	}
}

// TestSacredADRRegistry_NewDeclarationMustRegister keeps the registry
// self-maintaining. Without it the roster would only ever protect the ADRs that
// existed when it was written, so any sacred ADR added later would silently
// miss the de-wiring guard above — the same drift that made ten hand-written
// per-ADR fixtures fail, reintroduced one level up (codex adversarial-review
// [high] on PR #74).
func TestSacredADRRegistry_NewDeclarationMustRegister(t *testing.T) {
	registered := map[string]bool{}
	for _, id := range sacredADRRegistry {
		if registered[id] {
			t.Fatalf("sacredADRRegistry에 %s가 중복 등재됨", id)
		}
		registered[id] = true
	}
	for id := range currentlyDeclaringADRIDs(t) {
		if !registered[id] {
			t.Errorf("ADR-%s가 protects: 를 선언했으나 sacredADRRegistry에 미등재 — 같은 PR에서 등재하라. 등재해야 이후 이 선언이 조용히 제거되는 것을 막을 수 있다", id)
		}
	}
}

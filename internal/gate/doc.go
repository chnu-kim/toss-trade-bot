// Package gate implements the pure, unit-testable decision logic behind the
// ADR-0008 / ADR-0011 verdict gate: the mechanical rules a privileged,
// base-defined GitHub Actions job (.github/workflows/verdict-gate.yml) uses
// to turn a codex/Claude leg's raw output into a required-check outcome.
//
// This package is deliberately I/O-free. It never calls the GitHub API,
// never shells out to codex/claude, and never reads a diff off disk — every
// function takes already-fetched data (a Verdict, a slice of DiffFile, a
// VerdictHistory) and returns a decision. The workflow's cmd/verdict-gate
// binary is the thin I/O layer that gathers that data (via GitHub API calls,
// never by checking out or executing PR-branch content) and calls into this
// package; keeping the logic here, instead of in that binary or inline in
// the workflow YAML, is what makes ADR-0011 point 4(e)/(f)/(g)'s rules
// testable at all.
//
// Sub-concerns, one file each:
//
//   - verdict.go: the ADR-0008 point 1 structured verdict schema
//     (ParseVerdict) — a leg outputs only Decision approve/reject, never
//     Indeterminate.
//   - sanity.go: the ADR-0011 point 4(e)(iii) independent sanity cross-check
//     (SanityCheck) — schema validity alone never makes an Approve verdict
//     trustworthy; this is the second, mandatory gate.
//   - outcome.go: Outcome, the tri-state (Approve/Reject/Indeterminate)
//     result a verdict-check settles on after sanity, distinct from the raw
//     bi-state Decision a leg emits.
//   - eligibility.go: the ADR-0011 point 4(f) AND guard (head repo == base
//     repo AND author == mechanu[bot]) — fail-closed for anything else.
//   - riskclassification.go / pattern.go: the ADR-0008 point 5 path-based
//     risk mapping (ClassifyChangedPaths) — unmapped defaults to
//     RiskCritical, never RiskNonCritical.
//   - retry.go / gateconfig.go: the ADR-0011 point 4(e)(iv) SHA-sticky +
//     PR-level (N) + repo-wide (M) retry policy (ShouldProduceVerdict), the
//     ADR-0011 point 4(g) per-verdict-check leg combination
//     (CombineLegOutcomes), and the closed InterventionSignal type that
//     makes forbidden unstick sources (labels, issue comments,
//     repository_dispatch) structurally unrepresentable rather than merely
//     un-implemented.
//
// Every rule in this package traces to a specific ADR-0008/ADR-0011 point —
// see each file's doc comments and #48's PR description for the mapping.
package gate

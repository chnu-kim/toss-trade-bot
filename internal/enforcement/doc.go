// Package enforcement implements the ADR-0009 point 8 presence-check.
//
// ADR-0009 makes ADR authorship/approval autonomous (loop engineering) but
// carves out two sacred invariants (live-execution-human-gate,
// enforcement-integrity) that a loop can never weaken on its own. Point 8 adds
// a meta-safeguard on top of that carve-out: "Accepted" is a statement about
// the decision, not about whether the mechanisms that enforce it are actually
// standing. A loop must not read "ADR-0009 is Accepted" as "I may now act
// autonomously" — it must first prove, mechanically, that the three
// enforcement pillars are live:
//
//   - (a) .github/CODEOWNERS exists and covers every sacred path with an
//     owner (internal/enforcement.CheckCodeowners).
//   - (b) main's branch protection actually requires a CODEOWNERS review
//     (internal/enforcement.(*GitHubClient).CheckBranchProtection).
//   - (c) the loop's PR-authoring identity has genuinely moved to the GitHub
//     App, proven by read-only observation (ADR-0011 point 10): (c-1) the
//     PR-creation workflow exists on the protected branch AND (c-2) a recent
//     loop-created PR is actually authored by the App's bot identity
//     (internal/enforcement.CheckIdentity). Key/credential possession is
//     explicitly NOT identity evidence — the earlier App-JWT GET /app probe
//     was withdrawn as a semantic false positive.
//
// This package never treats "could not verify" as "verified true". A network
// error, a non-200 response, a malformed payload, or a missing dependency all
// collapse to Satisfied=false with a logged reason — fail-closed, per ADR-0009
// point 8: "증거 없음을 이미 안전함으로 해석하지 않는다."
//
// This package intentionally does not decide when it is called or what a
// caller does with an unsatisfied Result — that wiring (dispatch-issue /
// architect skills invoking this before autonomous work) is left to a future
// issue. It only answers the yes/no question truthfully.
package enforcement

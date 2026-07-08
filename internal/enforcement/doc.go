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
//   - (c) the identity the loop would author commits/PRs as has genuinely
//     flipped from the human reviewer to the GitHub App
//     (internal/enforcement.CheckIdentity).
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
//
// This package also hosts InstallationTokenMinter (installtoken.go), the
// credential-minting half of ADR-0009 point 5: once check (c) above proves
// the App identity has taken over, something still has to hand git/gh an
// actual installation access token to author commits/PRs as that identity.
// Minting reuses signAppJWT — same signing logic as CheckIdentity's App JWT,
// a different GitHub endpoint. It is a separate concern from the
// presence-check pillars (it does not appear in Result) and, as of #43, is
// deliberately untested against a live App private key — see
// internal/enforcement/installtoken.go.
package enforcement

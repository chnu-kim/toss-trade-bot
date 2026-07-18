package order

import (
	"crypto/sha256"
	"encoding/base64"
)

// clientOrderIDDerivedLen is the fixed length of a derived clientOrderId. It is
// well under ClientOrderIDMaxLen (36) so the value always satisfies the server
// constraint with headroom, while 32 base64url chars (192 bits of the digest)
// keep the server-side dedup key collision-resistant far beyond what strategy
// intentId volume needs.
const clientOrderIDDerivedLen = 32

// DeriveClientOrderID maps a strategy intentId to the deterministic
// clientOrderId the submit path sends to Toss (ADR-0002 point 4).
//
// The value is a pure function of intentId: SHA-256 of the intentId, base64url
// (RFC 4648 §5, no padding) encoded, truncated to clientOrderIDDerivedLen. Two
// properties matter and are pinned by tests:
//
//   - Deterministic & journal-independent — the same intentId always yields the
//     same clientOrderId, reproducible from the intentId alone even if the
//     write-ahead journal is lost (ADR-0002 point 4). The clientOrderId is only a
//     server-side duplicate-submit key, never a post-hoc lookup key.
//   - Charset-legal by construction — base64url's alphabet is exactly
//     [A-Za-z0-9-_], and 32 chars is <= the 36-char server limit, so every
//     derived value passes ValidateClientOrderID (the #33 input contract) no
//     matter what bytes (':' , spaces, non-ASCII) the strategy put in intentId.
//     The hash launders arbitrary strategy identifiers into the legal charset.
func DeriveClientOrderID(intentID string) string {
	sum := sha256.Sum256([]byte(intentID))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:clientOrderIDDerivedLen]
}

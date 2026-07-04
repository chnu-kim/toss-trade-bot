package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// Idempotency keys are synthesized deterministically per event class (ADR-0006
// point 3). They reuse ADR-0002's identities (intentId / orderId) rather than
// inventing new ones; the audit layer does not mint identity. Keys are the merge
// handle for at-least-once idempotent consumption — the sink itself is
// append-only and does no write-time dedup.

// orderLifecycleKey keys on orderId once acquired, else on intentId, always with
// the marker so distinct transitions stay distinct. The "order:" / "intent:"
// prefixes keep the two namespaces from ever colliding.
func orderLifecycleKey(intentID, orderID, marker string) string {
	if orderID != "" {
		return "order:" + orderID + ":" + marker
	}
	return "intent:" + intentID + ":" + marker
}

// fillKey versions a cumulative execution snapshot by orderId plus a digest of
// the financial fields. Toss exposes no per-fill id (measured), so a same-
// quantity fee/tax/settlement correction changes the digest and lands as a new
// record instead of being deduped by cumulative quantity alone (ADR-0006).
func fillKey(orderID string, snap FillSnapshot) string {
	return "fill:" + orderID + ":" + fillDigest(snap)
}

// errorKey scopes to intentId, else orderId, else "global", then folds in the
// operation, error class, and the durable append sequence. The sequence — the
// record's committed durable position, not a separate counter — is what keeps
// two otherwise-identical occurrences from collapsing (ADR-0006).
func errorKey(intentID, orderID, operation, errorClass string, seq int64) string {
	scope := "global"
	switch {
	case intentID != "":
		scope = "intent:" + intentID
	case orderID != "":
		scope = "order:" + orderID
	}
	return "error:" + scope + ":" + operation + ":" + errorClass + ":" + strconv.FormatInt(seq, 10)
}

// fillDigest is a deterministic SHA-256 over the six financial fields. Each field
// is length-prefixed before hashing so no field's content can imitate a boundary
// into the next — two distinct snapshots can never digest equal by concatenation
// ambiguity (guarded by TestFillDigestSeparatorInjection).
func fillDigest(snap FillSnapshot) string {
	h := sha256.New()
	fields := [...]string{
		snap.FilledQuantity,
		snap.AverageFilledPrice,
		snap.FilledAmount,
		snap.Commission,
		snap.Tax,
		snap.SettlementDate,
	}
	var lenBuf [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(f)))
		h.Write(lenBuf[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

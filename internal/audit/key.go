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

// lpField length-prefixes s with its decimal byte length followed by ':' — a
// netstring-style encoding. A left-to-right decoder reads the leading digit run
// (terminated by the first ':', and a decimal length can never itself contain
// ':'), then consumes exactly that many content bytes, then repeats; that
// decoder is a total left-inverse of this encoding, so concatenating
// lpField-encoded fields is injective regardless of what ':' or digits appear
// inside any field's content. This mirrors fillDigest's internal
// length-prefixing discipline, applied to the outer key composition
// (orderLifecycleKey / fillKey) so an orderId/intentId containing ':' can never
// make two distinct (id, marker) pairs collapse to the same key (issue #23,
// hardening ADR-0006 point 3's key-uniqueness intent).
func lpField(s string) string {
	return strconv.Itoa(len(s)) + ":" + s
}

// orderLifecycleKey keys on orderId once acquired, else on intentId, always with
// the marker so distinct transitions stay distinct. The "order:" / "intent:"
// prefixes keep the two namespaces from ever colliding, and each variable-length
// field is lpField-encoded so raw id/marker content (including embedded ':')
// can never shift the id/marker boundary and collapse two distinct pairs.
func orderLifecycleKey(intentID, orderID, marker string) string {
	if orderID != "" {
		return "order:" + lpField(orderID) + lpField(marker)
	}
	return "intent:" + lpField(intentID) + lpField(marker)
}

// fillKey versions a cumulative execution snapshot by orderId plus a digest of
// the financial fields. Toss exposes no per-fill id (measured), so a same-
// quantity fee/tax/settlement correction changes the digest and lands as a new
// record instead of being deduped by cumulative quantity alone (ADR-0006).
// orderId is lpField-encoded for consistency with orderLifecycleKey's outer-key
// discipline; fillDigest is always a fixed-length hex string with no ':', so
// this join could not actually be forced to collide via orderId content alone,
// but the same regularity is applied for uniformity (issue #23).
func fillKey(orderID string, snap FillSnapshot) string {
	return "fill:" + lpField(orderID) + fillDigest(snap)
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

package audit

import "testing"

// TestOrderLifecycleKeyReusesADR0002Identities pins ADR-0006's key reuse of
// ADR-0002 identities: once an orderId is acquired the key keys on orderId, and
// before acquisition it keys on intentId. Marker participates so distinct
// transitions of the same order stay distinct records.
func TestOrderLifecycleKeyReusesADR0002Identities(t *testing.T) {
	// Before orderId acquisition: keyed on intentId + marker.
	pre := orderLifecycleKey("intent-1", "", "prepared")
	preSame := orderLifecycleKey("intent-1", "", "prepared")
	if pre != preSame {
		t.Fatalf("pre-acquisition key not deterministic: %q vs %q", pre, preSame)
	}

	// Different marker on the same intent → different record.
	if got := orderLifecycleKey("intent-1", "", "submit-attempted"); got == pre {
		t.Errorf("marker not part of key: prepared and submit-attempted collided (%q)", got)
	}

	// After acquisition: keyed on orderId (not intentId).
	post := orderLifecycleKey("intent-1", "order-9", "acked")
	postDiffIntent := orderLifecycleKey("intent-DIFFERENT", "order-9", "acked")
	if post != postDiffIntent {
		t.Errorf("post-acquisition key must depend on orderId, not intentId: %q vs %q", post, postDiffIntent)
	}

	// Pre- and post-acquisition keys for the same logical intent are distinct
	// namespaces (intent: vs order:), never colliding.
	if pre == post {
		t.Errorf("pre and post keys collided: %q", pre)
	}
}

// TestFillKeyIsVersionedByFinancialDigest is the ADR-0006 fill contract: the key
// is orderId + a digest of the financial fields, so re-polling an identical
// snapshot merges (same key), but a same-quantity fee/tax/settlement correction
// produces a NEW key — a new record — instead of being deduped away.
func TestFillKeyIsVersionedByFinancialDigest(t *testing.T) {
	base := FillSnapshot{
		FilledQuantity:     "10",
		AverageFilledPrice: "100.5",
		FilledAmount:       "1005",
		Commission:         "1.00",
		Tax:                "0.50",
		SettlementDate:     "2026-07-08",
	}

	// Identical snapshot re-polled → identical key (natural dedup handle).
	if fillKey("order-9", base) != fillKey("order-9", base) {
		t.Fatal("fill key not deterministic for identical snapshot")
	}

	// Same cumulative quantity, corrected financial fields → new key each.
	corrections := map[string]FillSnapshot{
		"commission": mut(base, func(s *FillSnapshot) { s.Commission = "1.20" }),
		"tax":        mut(base, func(s *FillSnapshot) { s.Tax = "0.60" }),
		"settlement": mut(base, func(s *FillSnapshot) { s.SettlementDate = "2026-07-09" }),
		"amount":     mut(base, func(s *FillSnapshot) { s.FilledAmount = "1006" }),
		"avgprice":   mut(base, func(s *FillSnapshot) { s.AverageFilledPrice = "100.6" }),
	}
	baseKey := fillKey("order-9", base)
	for name, snap := range corrections {
		if snap.FilledQuantity != base.FilledQuantity {
			t.Fatalf("%s test mutated quantity by mistake", name)
		}
		if got := fillKey("order-9", snap); got == baseKey {
			t.Errorf("%s correction did not change key (same-quantity correction would be lost): %q", name, got)
		}
	}

	// orderId participates: same snapshot on a different order is a different key.
	if fillKey("order-OTHER", base) == baseKey {
		t.Error("fill key must include orderId")
	}
}

// TestErrorKeyCarriesSequence is the ADR-0006 error contract: the key is
// (scope|operation|class|sequence) where scope resolves intentId, else orderId,
// else "global". The sequence makes otherwise-identical occurrences distinct.
func TestErrorKeyCarriesSequence(t *testing.T) {
	// Sequence distinguishes two identical-looking occurrences.
	k0 := errorKey("intent-1", "", "submit", "timeout", 0)
	k1 := errorKey("intent-1", "", "submit", "timeout", 1)
	if k0 == k1 {
		t.Errorf("sequence not part of error key: seq 0 and 1 collided (%q)", k0)
	}

	// Scope precedence: intentId wins over orderId.
	kIntent := errorKey("intent-1", "order-9", "submit", "timeout", 5)
	kIntentNoOrder := errorKey("intent-1", "", "submit", "timeout", 5)
	if kIntent != kIntentNoOrder {
		t.Errorf("intentId must take scope precedence over orderId: %q vs %q", kIntent, kIntentNoOrder)
	}

	// Falls back to orderId when no intentId.
	kOrder := errorKey("", "order-9", "submit", "timeout", 5)
	if kOrder == kIntent {
		t.Errorf("order-scoped and intent-scoped keys must differ: %q", kOrder)
	}

	// Falls back to "global" when neither is present.
	kGlobal := errorKey("", "", "token-refresh", "unauthorized", 5)
	kGlobalSame := errorKey("", "", "token-refresh", "unauthorized", 5)
	if kGlobal != kGlobalSame {
		t.Fatal("global error key not deterministic")
	}
	if kGlobal == kOrder {
		t.Errorf("global and order-scoped keys must differ: %q", kGlobal)
	}

	// operation and errorClass both participate.
	if errorKey("", "", "op-A", "class-X", 5) == errorKey("", "", "op-B", "class-X", 5) {
		t.Error("operation not part of error key")
	}
	if errorKey("", "", "op-A", "class-X", 5) == errorKey("", "", "op-A", "class-Y", 5) {
		t.Error("errorClass not part of error key")
	}
}

// TestFillDigestSeparatorInjection guards the digest against field-boundary
// ambiguity: concatenating fields must not let content from one field imitate a
// split into the next (which would make two different snapshots digest equal).
func TestFillDigestSeparatorInjection(t *testing.T) {
	a := FillSnapshot{FilledQuantity: "1", AverageFilledPrice: "23"}
	b := FillSnapshot{FilledQuantity: "12", AverageFilledPrice: "3"}
	if fillDigest(a) == fillDigest(b) {
		t.Errorf("digest is ambiguous across field boundaries: %q == %q", fillDigest(a), fillDigest(b))
	}
}

func mut(s FillSnapshot, f func(*FillSnapshot)) FillSnapshot {
	f(&s)
	return s
}

// TestOrderLifecycleKeyColonInIdentifiersDoesNotCollide is the issue #23 outer-
// key hardening AC: before length-prefixing, "order:" + orderID + ":" + marker
// raw-joined its fields, so a colon inside orderID/intentID could make two
// distinct (id, marker) pairs concatenate to the identical string. This is
// exactly that adversarial pair — the marker/id boundary shifts by one colon —
// which must now produce distinct keys.
func TestOrderLifecycleKeyColonInIdentifiersDoesNotCollide(t *testing.T) {
	// Post-acquisition (orderId-scoped) branch: ("order:9","acked") vs
	// ("order","9:acked") raw-joined to the same "order:order:9:acked" string.
	a := orderLifecycleKey("", "order:9", "acked")
	b := orderLifecycleKey("", "order", "9:acked")
	if a == b {
		t.Fatalf("colon-embedded orderID collided with a differently-split (orderID, marker) pair: %q", a)
	}

	// Same shape of attack against the pre-acquisition (intentId-scoped) branch.
	c := orderLifecycleKey("intent:1", "", "prepared")
	d := orderLifecycleKey("intent", "", "1:prepared")
	if c == d {
		t.Fatalf("colon-embedded intentID collided with a differently-split (intentID, marker) pair: %q", c)
	}
}

// TestFillKeyColonInOrderIDStaysDistinct hardens fillKey's outer join to the
// same length-prefix discipline as orderLifecycleKey, for consistency (issue
// #23 "통일"). Note: fillDigest is always a fixed 64-char hex string containing
// no ':', so the pre-hardening raw join ("fill:"+orderID+":"+digest) could not
// actually be forced to collide via orderID content alone — this test pins
// uniform treatment, not a fixed prior vulnerability.
func TestFillKeyColonInOrderIDStaysDistinct(t *testing.T) {
	snap := FillSnapshot{FilledQuantity: "1", AverageFilledPrice: "2", FilledAmount: "3", Commission: "4", Tax: "5", SettlementDate: "6"}
	a := fillKey("order:9", snap)
	b := fillKey("order", snap)
	if a == b {
		t.Fatalf("different orderIDs (one colon-embedded) collided: %q", a)
	}
}

// TestLPFieldIsInjective is the general property behind the two tests above: no
// two distinct (a, b) string pairs may lpField-concatenate to the same output,
// regardless of ':' or digits embedded in either field's content.
func TestLPFieldIsInjective(t *testing.T) {
	cases := []struct{ a1, b1, a2, b2 string }{
		{"order:9", "acked", "order", "9:acked"},
		{"", "x", "0:", "x"},
		{"5:abc", "d", "5", "abc:d"},
	}
	for _, c := range cases {
		left := lpField(c.a1) + lpField(c.b1)
		right := lpField(c.a2) + lpField(c.b2)
		if (c.a1 != c.a2 || c.b1 != c.b2) && left == right {
			t.Errorf("lpField concatenation collided for distinct pairs (%q,%q) vs (%q,%q): %q", c.a1, c.b1, c.a2, c.b2, left)
		}
	}
}

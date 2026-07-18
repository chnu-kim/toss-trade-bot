package order

import "testing"

// TestDeriveClientOrderIDDeterministic pins ADR-0002 point 4: the clientOrderId
// is a deterministic function of the intentId alone, so the same value is
// reproducible from the intentId even if the journal is lost.
func TestDeriveClientOrderIDDeterministic(t *testing.T) {
	const id = "strat-alpha:2026-07-18:AAPL:0001"
	a := DeriveClientOrderID(id)
	b := DeriveClientOrderID(id)
	if a != b {
		t.Fatalf("DeriveClientOrderID not deterministic: %q vs %q", a, b)
	}
	if a == "" {
		t.Fatalf("DeriveClientOrderID returned empty for %q", id)
	}
}

// TestDeriveClientOrderIDValid pins that every derived value satisfies the Toss
// server constraint measured in #33 (ValidateClientOrderID), including intentIds
// that carry ':' or non-ASCII bytes the raw charset would reject — the hash
// launders arbitrary strategy identifiers into the legal charset.
func TestDeriveClientOrderIDValid(t *testing.T) {
	ids := []string{
		"i1",
		"",
		"strat-alpha:2026-07-18:AAPL:0001",
		"주문-한글-intent",
		"with spaces and / slashes + plus",
		"a-very-long-intent-id-that-far-exceeds-the-thirty-six-character-server-limit-0123456789",
	}
	for _, id := range ids {
		got := DeriveClientOrderID(id)
		if err := ValidateClientOrderID(got); err != nil {
			t.Errorf("DeriveClientOrderID(%q)=%q fails ValidateClientOrderID: %v", id, got, err)
		}
		if len(got) > ClientOrderIDMaxLen {
			t.Errorf("DeriveClientOrderID(%q)=%q length %d exceeds %d", id, got, len(got), ClientOrderIDMaxLen)
		}
	}
}

// TestDeriveClientOrderIDDistinct pins that distinct intentIds derive distinct
// clientOrderIds (server-side dedup must not collapse two different intents).
func TestDeriveClientOrderIDDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"i1", "i2", "i3", "strat:a", "strat:b", "AAPL-0001", "AAPL-0002"} {
		got := DeriveClientOrderID(id)
		if prev, ok := seen[got]; ok {
			t.Fatalf("collision: %q and %q both derive %q", prev, id, got)
		}
		seen[got] = id
	}
}

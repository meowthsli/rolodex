package utils

import "testing"

// TestNameTokenSubset verifies the partial-name rule used for same-link entity
// resolution: partial must be a proper token-subset of full, and full must carry
// at least two tokens. This is what lets "Alex"/"Karp" resolve to "Alex Karp".
func TestNameTokenSubset(t *testing.T) {
	cases := []struct {
		partial, full string
		want          bool
	}{
		// Proper subset of a multi-token name.
		{"Alex", "Alex Karp", true},
		{"Karp", "Alex Karp", true},
		{"Karpa", "Alex Karp", false},
		{"Smith", "Alex Karp", false},
		// Proper subset independent of token order / case / transliteration.
		{"karp", "ALEX KARP", true},
		// The same name in any order is NOT a proper subset of itself.
		{"Karp Alex", "Alex Karp", false},
		// The full side must have >=2 tokens.
		{"Alex", "Karp", false},
		{"Alex", "Alex", false},
		// A name is never a proper subset of itself.
		{"Alex Karp", "Alex Karp", false},
		// Superset direction is not reported (partial must be the smaller one).
		{"Alex Karp", "Alex", false},
	}
	for _, c := range cases {
		if got := NameTokenSubset(c.partial, c.full); got != c.want {
			t.Errorf("NameTokenSubset(%q, %q) = %v, want %v", c.partial, c.full, got, c.want)
		}
	}
}

// TestCanonKeyKeepsAlexLiteral verifies that the English given name "Alex" is
// canonized to "aleks" (via the x->ks transliteration fold) and does NOT become
// the Russian name Алексей ("aleksei"). The distinction matters so a person like
// "Alex Karp" canonizes to "aleks_karp", distinct from anyone named
// Алексей/Aleksei, while the Russian name still unifies across its Latin
// spellings.
func TestCanonKeyKeepsAlexLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Alex", "aleks"},
		{"ALEX", "aleks"},
		{"Alex Karp", "aleks_karp"},
		{"Karp Alex", "aleks_karp"},
		// The Russian name Алексей and its Latin spellings still canonize to
		// "aleksei" and remain distinct from "aleks".
		{"Алексей", "aleksei"},
		{"Aleksei", "aleksei"},
		{"Alexey", "aleksei"},
		{"Alexei", "aleksei"},
	}
	for _, c := range cases {
		if got := CanonKey(c.in); got != c.want {
			t.Errorf("CanonKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// "Alex" (English given name) and "Алексей" (Russian name) must not collide.
	if CanonKey("Alex") == CanonKey("Алексей") {
		t.Error("CanonKey(Alex) must not equal CanonKey(Алексей)")
	}
	// But Cyrillic "Алексей" and its Latin spellings must all agree.
	if CanonKey("Алексей") != CanonKey("Alexey") || CanonKey("Алексей") != CanonKey("Aleksei") {
		t.Error("Алексей must unify across its Latin spellings")
	}
}

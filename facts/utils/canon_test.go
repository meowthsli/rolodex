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

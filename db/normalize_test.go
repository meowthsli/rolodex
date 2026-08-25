package db

import "testing"

// TestNormalizeURL checks that normalizeURL strips http/https prefixes across
// various casings, leaves scheme-less URLs untouched, and orders query
// parameters alphabetically by key regardless of their original order.
func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.com", "example.com"},
		{"http://example.com", "example.com"},
		{"HTTPS://Example.com", "example.com"},
		{"https://www.Example.com", "example.com"},
		{"https://example.com:443/", "example.com"},
		{"https://example.com/path/#frag", "example.com/path"},
		{"https://example.com/path//a/./b", "example.com/path/a/b"},
		{"example.com", "example.com"},
		{"https://example.com/path?q=1", "example.com/path?q=1"},
		{"https://example.com?b=2&a=1", "example.com?a=1&b=2"},
		{"http://example.com?a=1&b=2", "example.com?a=1&b=2"},
		{"https://example.com?z=9&y=8&x=7", "example.com?x=7&y=8&z=9"},
		{"https://example.com?b=2&a=1&c=3", "example.com?a=1&b=2&c=3"},
		{"https://example.com?noeq", "example.com?noeq="},
		{"https://0x42660793/", "66.102.7.147"},
		{"https://0102.0146.07.0223/", "66.102.7.147"},
		{"https://1113982867/", "66.102.7.147"},
	}
	for _, tc := range cases {
		if got := normalizeURL(tc.in); got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

package main

import (
	"strings"

	"github.com/PuerkitoBio/purell"
)

// normalizeFlags is the absolute-maximum canonicalization rule set
// (purell.FlagsAllGreedy): everything in the unsafe set plus decoding of
// obfuscated IP hosts (decimal/octal/hex) and removal of unnecessary host
// dots.
const normalizeFlags purell.NormalizationFlags = purell.FlagsAllGreedy

// normalizeURL produces a canonical form of the URL via purell, then strips
// the scheme prefix so the value can be stored scheme-less. If the input
// cannot be parsed, it is returned as-is (scheme still stripped) rather than
// panicking.
func normalizeURL(raw string) string {
	canonical, err := purell.NormalizeURLString(raw, normalizeFlags)
	if err != nil {
		canonical = raw
	}
	if i := strings.Index(canonical, "://"); i >= 0 {
		canonical = canonical[i+len("://"):]
	}
	return canonical
}

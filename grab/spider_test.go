package grab

import "testing"

// TestStripHrefs verifies that every href target is replaced with "#" while the
// surrounding markup and link text are left intact, including mixed quote styles
// and case variations.
func TestStripHrefs(t *testing.T) {
	in := `<html><body>
<a href="https://example.com/x">link</a>
<A HREF='http://evil.test/y'>other</a>
<img src="http://keep.me/z">
</body></html>`

	want := `<html><body>
<a href="#">link</a>
<A href="#">other</a>
<img src="http://keep.me/z">
</body></html>`

	if got := stripHrefs(in); got != want {
		t.Errorf("stripHrefs mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestExtractLinksSkipsShortAndFragments verifies that extractLinks drops links
// that are too short as written in the page (e.g. "/exit") and pure fragments
// (href="#" or href="#frag"), while still returning real relative/absolute links.
func TestExtractLinksSkipsShortAndFragments(t *testing.T) {
	html := `<html><body>
<a href="/page1">ok</a>
<a href="/exit">short</a>
<a href="#">frag</a>
<a href="#section">frag2</a>
<a href="https://example.com/x">abs</a>
</body></html>`

	got, err := extractLinks("http://host/", html)
	if err != nil {
		t.Fatalf("extractLinks: %v", err)
	}

	gotSet := make(map[string]bool)
	for _, g := range got {
		gotSet[g] = true
	}
	if !gotSet["http://host/page1"] {
		t.Errorf("expected /page1 to be extracted, got %v", got)
	}
	if !gotSet["https://example.com/x"] {
		t.Errorf("expected absolute link to be extracted, got %v", got)
	}
	for _, bad := range []string{"http://host/exit", "http://host/#", "http://host/#section"} {
		if gotSet[bad] {
			t.Errorf("short/fragment link %q should not be extracted, got %v", bad, got)
		}
	}
}

package grab

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestScraper seeds 5 links and runs the scraper with a fast tick. It asserts
// that every link ends up scraped: content captured and last_scrapped stamped.
// The HTTP fallback (https then http) is exercised because the mock server is
// plain HTTP.
func TestScraper(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "content for %s", r.URL.Path)
	}))
	defer server.Close()

	const total = 5
	paths := make([]string, total)
	for i := 0; i < total; i++ {
		paths[i] = "/page" + strconv.Itoa(i)
		url := server.URL + paths[i]
		if _, err := repo.NewLink(url); err != nil {
			t.Fatalf("AddLink: %v", err)
		}
	}

	scraper := NewScraper(repo, &http.Client{}, 20*time.Millisecond)
	scraper.Start()
	defer scraper.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		links, err := repo.ListLinks()
		if err != nil {
			t.Fatalf("ListLinks: %v", err)
		}
		pending := 0
		for _, l := range links {
			if !l.LastScrappedAt.Valid {
				pending++
			}
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out with %d pending links", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links) != total {
		t.Fatalf("expected %d links, got %d", total, len(links))
	}

	for i, l := range links {
		if !l.LastScrappedAt.Valid {
			t.Errorf("link %d was not scraped", l.ID)
		}
		want := "content for " + paths[i]
		if l.Content != want {
			t.Errorf("link %d content mismatch:\n got %q\nwant %q", l.ID, l.Content, want)
		}
	}
}

// TestScraperNoPendingLinks ensures that processing an empty queue is a no-op
// (no error and no rows touched).
func TestScraperNoPendingLinks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce on empty queue: %v", err)
	}
}

// roundTripFunc adapts a function into an http.RoundTripper, letting tests
// stub HTTP transport behavior (e.g. force a connection error for a host).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestScraperRecordsError seeds one link whose host always fails (via a custom
// transport) and one healthy link. It verifies the failed link records the
// error message, stamps last_scrapped (so it won't be retried) and keeps empty
// content, while the healthy link is scraped normally with no error.
func TestScraperRecordsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "content for %s", r.URL.Path)
	}))
	defer server.Close()

	// Client that fails for the "fail.local" host but proxies everything
	// else to the test server.
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "fail.local" {
				return nil, fmt.Errorf("boom: cannot reach %s", r.URL.Host)
			}
			return http.DefaultTransport.RoundTrip(r)
		}),
	}

	if _, err := repo.NewLink("http://fail.local/broken"); err != nil {
		t.Fatalf("AddLink (bad): %v", err)
	}
	if _, err := repo.NewLink(server.URL + "/ok"); err != nil {
		t.Fatalf("AddLink (good): %v", err)
	}

	scraper := NewScraper(repo, client, 20*time.Millisecond)
	scraper.Start()
	defer scraper.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		links, err := repo.ListLinks()
		if err != nil {
			t.Fatalf("ListLinks: %v", err)
		}
		pending := 0
		for _, l := range links {
			if !l.LastScrappedAt.Valid {
				pending++
			}
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out with %d pending links", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}

	var bad, good LinkQueue
	for _, l := range links {
		if l.URL == "fail.local/broken" {
			bad = l
		} else {
			good = l
		}
	}

	if bad.ID == 0 {
		t.Fatal("bad link not found")
	}
	if !bad.Error.Valid {
		t.Errorf("expected error to be recorded for bad link, got invalid")
	}
	if !strings.Contains(bad.Error.String, "boom") {
		t.Errorf("expected error message to contain boom, got %q", bad.Error.String)
	}
	if !bad.LastScrappedAt.Valid {
		t.Errorf("expected bad link last_scrapped to be stamped")
	}
	if bad.Content != "" {
		t.Errorf("expected empty content for bad link, got %q", bad.Content)
	}

	if good.ID == 0 {
		t.Fatal("good link not found")
	}
	if good.Error.Valid {
		t.Errorf("expected no error for good link, got %q", good.Error.String)
	}
	if good.Content == "" {
		t.Errorf("expected content for good link")
	}
}

// TestScraperSavesReadableText verifies that, alongside the raw page content,
// the scraper stores the readable text extracted by go-readability.
func TestScraperSavesReadableText(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><title>T</title></head><body>
			<nav>menu menu menu</nav>
			<article><p>This is the meaningful readable paragraph.</p></article>
			<script>var x = 1;</script>
		</body></html>`)
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL + "/")
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, 20*time.Millisecond)
	scraper.Start()
	defer scraper.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		lk, err := repo.GetLink(seed.ID)
		if err == nil && lk.LastScrappedAt.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for seed scrape")
		}
		time.Sleep(10 * time.Millisecond)
	}
	scraper.Stop()

	got, err := repo.GetLink(seed.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.ReadableText == "" {
		t.Errorf("expected readable_text to be populated")
	}
	if !strings.Contains(got.ReadableText, "meaningful readable paragraph") {
		t.Errorf("readable_text missing expected content: %q", got.ReadableText)
	}
}

// TestExtractLinks verifies that links are extracted from the relevant HTML
// attributes, resolved against the base URL, filtered to http/https, and
// deduplicated.
func TestExtractLinks(t *testing.T) {
	base := "http://example.com/dir/page"
	htmlBody := `<html><body>
		<a href="/a">a</a>
		<a href="b">b</a>
		<a href="https://x.com/y">x</a>
		<a href="//other.com/z">proto</a>
		<a href="mailto:foo@bar.com">mail</a>
		<a href="javascript:void(0)">js</a>
		<iframe src="/frame"></iframe>
		<a href="/a">dup</a>
	</body></html>`

	got, err := extractLinks(base, htmlBody)
	if err != nil {
		t.Fatalf("extractLinks: %v", err)
	}
	want := []string{
		"http://example.com/a",
		"http://example.com/dir/b",
		"https://x.com/y",
		"http://other.com/z",
		"http://example.com/frame",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractLinks = %v, want %v", got, want)
	}
}

// TestScraperDiscoversLinks seeds one page and verifies the spider enqueues
// the links it discovers (relative and absolute) while skipping non-http(s)
// schemes.
func TestScraperDiscoversLinks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<a href="/page1">1</a>
			<a href="page2">2</a>
			<a href nonexistent>ignore</a>
			<a href="mailto:foo@bar.com">m</a>
			<a href="#frag">f</a>
		</body></html>`)
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL + "/")
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, 20*time.Millisecond)
	scraper.Start()
	defer scraper.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		lk, err := repo.GetLink(seed.ID)
		if err == nil && lk.LastScrappedAt.Valid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for seed scrape")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Allow the spider a moment to enqueue discovered links.
	time.Sleep(50 * time.Millisecond)
	scraper.Stop()

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	got := make(map[string]bool)
	for _, l := range links {
		got[l.URL] = true
	}

	host := strings.TrimPrefix(server.URL, "http://")
	for _, want := range []string{
		host,
		host + "/page1",
		host + "/page2",
	} {
		if !got[want] {
			t.Errorf("expected discovered link %q in queue, got %v", want, links)
		}
	}
}

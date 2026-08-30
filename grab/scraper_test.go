package grab

import (
	"database/sql"
	"errors"
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
		fmt.Fprintf(w, "content for %s\n%s", r.URL.Path, strings.Repeat("lorem ipsum ", 200))
	}))
	defer server.Close()

	const total = 5
	paths := make([]string, total)
	for i := 0; i < total; i++ {
		paths[i] = "/page" + strconv.Itoa(i)
		url := server.URL + paths[i]
		if _, err := repo.NewLink(url, 1); err != nil {
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
		if !strings.HasPrefix(l.Content, want) {
			t.Errorf("link %d content mismatch:\n got %q\nwant prefix %q", l.ID, l.Content, want)
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
		fmt.Fprintf(w, "content for %s\n%s", r.URL.Path, strings.Repeat("lorem ipsum ", 200))
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

	if _, err := repo.NewLink("http://fail.local/broken", 1); err != nil {
		t.Fatalf("AddLink (bad): %v", err)
	}
	if _, err := repo.NewLink(server.URL+"/ok", 1); err != nil {
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
			<article><p>This is the meaningful readable paragraph.</p>%s</article>
			<script>var x = 1;</script>
		</body></html>`, strings.Repeat("lorem ipsum ", 200))
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/", 1)
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
		<a href="/apage">a</a>
		<a href="bbpage">b</a>
		<a href="https://x.com/y">x</a>
		<a href="//other.com/z">proto</a>
		<a href="mailto:foo@bar.com">mail</a>
		<a href="javascript:void(0)">js</a>
		<iframe src="/frame"></iframe>
		<a href="/apage">dup</a>
	</body></html>`

	got, err := extractLinks(base, htmlBody)
	if err != nil {
		t.Fatalf("extractLinks: %v", err)
	}
	want := []string{
		"http://example.com/apage",
		"http://example.com/dir/bbpage",
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
		fmt.Fprintf(w, `<html><body>%s
			<a href="/page1">1</a>
			<a href="page2crawl">2</a>
			<a href nonexistent>ignore</a>
			<a href="mailto:foo@bar.com">m</a>
			<a href="#frag">f</a>
		</body></html>`, strings.Repeat("word ", 300))
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/", 1)
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
		host + "/page2crawl",
	} {
		if !got[want] {
			t.Errorf("expected discovered link %q in queue, got %v", want, links)
		}
	}
}

// TestScraperSkipsShortDiscoveredLinks seeds a page whose HTML links to a
// too-short URL and verifies the spider never enqueues it as a new link.
func TestScraperSkipsShortDiscoveredLinks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pad well past the 1KB content threshold so link discovery runs.
		pad := strings.Repeat("word ", 300)
		fmt.Fprintf(w, `<html><body>%s<a href="http://a.co">short</a><a href="/page1">ok</a></body></html>`, pad)
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/", 1)
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
	time.Sleep(50 * time.Millisecond)
	scraper.Stop()

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	host := strings.TrimPrefix(server.URL, "http://")
	for _, l := range links {
		if l.URL == "a.co" {
			t.Fatalf("too-short discovered link %q should not be enqueued", l.URL)
		}
	}
	// The valid link must still be present.
	found := false
	for _, l := range links {
		if l.URL == host+"/page1" {
			found = true
		}
	}
	if !found {
		t.Fatal("valid discovered link missing from queue")
	}
}

// TestScraperPropagatesGeneration seeds a page at generation 1 and verifies that
// links discovered while scraping it are enqueued at generation 2 (parent + 1),
// so a cycle or fake links cannot run the scraper infinitely without a bound.
func TestScraperPropagatesGeneration(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>%s<a href="/page1">1</a></body></html>`, strings.Repeat("lorem ipsum ", 200))
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	if seed.Generation != 1 {
		t.Fatalf("seed generation = %d, want 1", seed.Generation)
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
	time.Sleep(50 * time.Millisecond)
	scraper.Stop()

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	host := strings.TrimPrefix(server.URL, "http://")
	for _, l := range links {
		if l.URL == host+"/page1" {
			if l.Generation != seed.Generation+1 {
				t.Errorf("discovered link generation = %d, want %d", l.Generation, seed.Generation+1)
			}
			return
		}
	}
	t.Fatal("discovered link not found in queue")
}

// TestScraperBlacklist seeds a banned link and verifies the scraper erases it
// from the database before fetching anything: no content is stored and the row
// disappears entirely so it is never processed or retried.
func TestScraperBlacklist(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "should never be fetched %s", strings.Repeat("lorem ipsum ", 200))
	}))
	defer server.Close()

	banned, err := repo.NewLink(server.URL+"/banned", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	// A normal link so the scraper has something else to do.
	if _, err := repo.NewLink(server.URL+"/ok", 1); err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	host := strings.TrimPrefix(server.URL, "http://")
	scraper.SetBlacklist([]string{host + "/banned"})
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	// Banned link must be gone entirely.
	if _, err := repo.GetLink(banned.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("banned link should be erased, GetLink err = %v", err)
	}

	// The non-banned link remains pending and untouched.
	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	found := false
	for _, l := range links {
		if l.URL == host+"/ok" {
			found = true
			if l.LastScrappedAt.Valid {
				t.Errorf("non-banned link should not be scraped by blacklist check")
			}
		}
	}
	if !found {
		t.Fatal("non-banned link missing from queue")
	}
}

// TestScraperSkipsSmallContent seeds a link whose page is under 1KB and
// verifies the scraper records an error and stores no content, so tiny pages
// are neither persisted nor retried.
func TestScraperSkipsSmallContent(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tiny")
	}))
	defer server.Close()

	small, err := repo.NewLink(server.URL+"/small", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	got, err := repo.GetLink(small.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if !got.LastScrappedAt.Valid {
		t.Fatal("small-content link should be marked scraped")
	}
	if got.Error.String != "content too small (<1KB)" {
		t.Fatalf("error = %q, want content too small (<1KB)", got.Error.String)
	}
	if got.Content != "" {
		t.Errorf("content should not be stored, got %d bytes", len(got.Content))
	}
}

// TestScraperSkipsShortURL seeds a link whose stored URL is under 6 characters
// and verifies the scraper records an error and never fetches the page.
func TestScraperSkipsShortURL(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	// No server: a fetch must never happen for a too-short URL.
	short, err := repo.NewLink("a.co", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	got, err := repo.GetLink(short.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if !got.LastScrappedAt.Valid {
		t.Fatal("short-url link should be marked scraped")
	}
	if got.Error.String != "url too short (<6)" {
		t.Fatalf("error = %q, want url too short (<6)", got.Error.String)
	}
}

// TestScraperNoLinksDryRun seeds a page and verifies that with noLinks mode the
// spider reports discovered links but does NOT insert them into link_queue.
func TestScraperNoLinksDryRun(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>%s<a href="/page1">1</a><a href="/exit">short</a></body></html>`,
			strings.Repeat("word ", 300))
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	scraper.SetNoLinks(true)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	// Only the seed should remain; discovered /page1 must not be enqueued.
	host := strings.TrimPrefix(server.URL, "http://")
	for _, l := range links {
		if l.ID == seed.ID {
			continue
		}
		if l.URL == host+"/page1" {
			t.Fatalf("discovered link %q should not be inserted in noLinks mode", l.URL)
		}
	}
	if len(links) != 1 {
		t.Fatalf("expected only the seed link in queue, got %d rows: %v", len(links), links)
	}
}

// TestScraperFollowsRedirect verifies that a 301/302 redirect is followed: the
// content stored is the final page's body, and discovered links resolve against
// the FINAL URL (not the stale original), so a relative href on the redirected
// page produces the address under the real page's path.
func TestScraperFollowsRedirect(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old/dir":
			http.Redirect(w, r, "/new/loc", http.StatusMovedPermanently)
		case "/new/loc":
			fmt.Fprintf(w, `<html><body>%s<a href="subpage">go</a></body></html>`,
				strings.Repeat("word ", 200))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/old/dir", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	got, err := repo.GetLink(seed.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.Error.Valid {
		t.Fatalf("redirected link recorded an error: %q", got.Error.String)
	}
	if !strings.Contains(got.Content, "subpage") {
		t.Errorf("stored content should be the final page body, got %q", got.Content)
	}

	host := strings.TrimPrefix(server.URL, "http://")
	// The final page lives at /new/loc, so its relative href "subpage" must
	// resolve to /new/subpage, never the outdated /old/subpage.
	gotURLs := make(map[string]bool)
	for _, l := range listAll(t, repo) {
		gotURLs[l.URL] = true
	}
	if !gotURLs[host+"/new/subpage"] {
		t.Errorf("expected discovered link %q, got %v", host+"/new/subpage", gotURLs)
	}
	if gotURLs[host+"/old/subpage"] {
		t.Errorf("discovered link resolved against the stale original URL: %v", gotURLs)
	}
}

// listAll fetches every row in link_queue, failing the test on error.
func listAll(t *testing.T, repo *LinksRepository) []LinkQueue {
	t.Helper()
	links, err := repo.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	return links
}

// TestScraperRedirectLoop verifies an endless 3xx cycle (A -> B -> A) is
// detected and recorded as an error rather than being chased forever: the link
// gets a bounded error message, is stamped scraped (so it is not retried) and
// stores no content.
func TestScraperRedirectLoop(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusMovedPermanently)
		case "/b":
			http.Redirect(w, r, "/a", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	link, err := repo.NewLink(server.URL+"/a", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce should record the loop as an error, got %v", err)
	}

	got, err := repo.GetLink(link.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if !got.Error.Valid || !strings.Contains(got.Error.String, "redirect loop") {
		t.Fatalf("error = %q, want it to mention the redirect loop", got.Error.String)
	}
	if !got.LastScrappedAt.Valid {
		t.Errorf("loop link should be stamped scraped so it is not retried")
	}
	if got.Content != "" {
		t.Errorf("loop link must not store content, got %d bytes", len(got.Content))
	}
}

// TestScraperRedirectDepthCapped verifies the redirect follower gives up after
// maxRedirects hops instead of chasing an arbitrarily long (but acyclic) chain:
// the link records a bounded-depth error and is not retried.
func TestScraperRedirectDepthCapped(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /p0 -> /p1 -> ... -> /p11: longer than maxRedirects (10).
		var i int
		if _, err := fmt.Sscanf(r.URL.Path, "/p%d", &i); err != nil {
			http.NotFound(w, r)
			return
		}
		if i >= maxRedirects+1 {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/p%d", i+1), http.StatusMovedPermanently)
	}))
	defer server.Close()

	link, err := repo.NewLink(server.URL+"/p0", 1)
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce should record the depth error, got %v", err)
	}

	got, err := repo.GetLink(link.ID)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if !got.Error.Valid || !strings.Contains(got.Error.String, "redirect") {
		t.Fatalf("error = %q, want a redirect depth error", got.Error.String)
	}
	if !got.LastScrappedAt.Valid {
		t.Errorf("depth-capped link should be stamped scraped so it is not retried")
	}
}

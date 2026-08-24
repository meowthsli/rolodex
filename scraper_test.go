package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
		if _, err := repo.AddLink(url); err != nil {
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
			if !l.LastScrapped.Valid {
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
		if !l.LastScrapped.Valid {
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

	if _, err := repo.AddLink("http://fail.local/broken"); err != nil {
		t.Fatalf("AddLink (bad): %v", err)
	}
	if _, err := repo.AddLink(server.URL + "/ok"); err != nil {
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
			if !l.LastScrapped.Valid {
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
	if !bad.LastScrapped.Valid {
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

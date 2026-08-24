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

func TestScraper(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "content for %s", r.URL.Path)
	}))
	defer server.Close()

	const total = 5
	for i := 0; i < total; i++ {
		url := server.URL + "/page" + strconv.Itoa(i)
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

	for _, l := range links {
		if !l.LastScrapped.Valid {
			t.Errorf("link %d was not scraped", l.ID)
		}
		want := "content for " + strings.TrimPrefix(l.URL, server.URL)
		if l.Content != want {
			t.Errorf("link %d content mismatch:\n got %q\nwant %q", l.ID, l.Content, want)
		}
	}
}

func TestScraperNoPendingLinks(t *testing.T) {
	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	scraper := NewScraper(repo, &http.Client{}, time.Hour)
	if err := scraper.scrapeOnce(); err != nil {
		t.Fatalf("scrapeOnce on empty queue: %v", err)
	}
}

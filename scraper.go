package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

// Scraper periodically fetches the next unscraped link, retrieves its content
// over HTTP and records the result back into the repository.
type Scraper struct {
	repo   *LinksRepository
	client *http.Client
	tick   time.Duration
	stop   chan struct{}
}

// NewScraper builds a Scraper. A nil client defaults to http.DefaultClient,
// and a non-positive tick defaults to 5 seconds.
func NewScraper(repo *LinksRepository, client *http.Client, tick time.Duration) *Scraper {
	if client == nil {
		client = http.DefaultClient
	}
	if tick <= 0 {
		tick = 5 * time.Second
	}
	return &Scraper{repo: repo, client: client, tick: tick}
}

// Start launches the scraping loop in a background goroutine.
func (s *Scraper) Start() {
	s.stop = make(chan struct{})
	go s.run()
}

// Stop terminates the scraping loop.
func (s *Scraper) Stop() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

func (s *Scraper) run() {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			if err := s.scrapeOnce(); err != nil {
				log.Printf("scrape error: %v", err)
			}
		}
	}
}

// scrapeOnce processes a single pending link.
func (s *Scraper) scrapeOnce() error {
	link, err := s.repo.GetNextPendingLink()
	if err != nil {
		return err
	}
	if link.ID == 0 {
		return nil
	}

	var resp *http.Response
	var getErr error
	chosenScheme := ""
	for _, scheme := range []string{"https", "http"} {
		resp, getErr = s.client.Get(scheme + "://" + link.URL)
		if getErr == nil {
			chosenScheme = scheme
			break
		}
	}
	if getErr != nil {
		return s.repo.SaveScrapeError(link.ID, getErr.Error())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.repo.SaveScrapeError(link.ID, err.Error())
	}

	content := string(body)
	if err := s.repo.SaveScrapeResult(link.ID, content); err != nil {
		return err
	}

	// Spider: discover links in the page and enqueue any that are new.
	base := chosenScheme + "://" + link.URL
	discovered, err := extractLinks(base, content)
	if err != nil {
		return err
	}
	for _, u := range discovered {
		if _, err := s.repo.NewLink(u); err != nil && !errors.Is(err, ErrLinkExists) {
			return err
		}
	}

	return nil
}

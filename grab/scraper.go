package grab

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Scraper periodically fetches the next unscraped link, retrieves its content
// over HTTP and records the result back into the repository.
type Scraper struct {
	repo   *LinksRepository
	client *http.Client
	tick   time.Duration
	stop   chan struct{}
	// done is closed by the run loop when it exits, so Stop can wait for the
	// in-flight scrape to finish before the caller tears down the database.
	done chan struct{}
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
	log.Printf("scraper started (tick: %s)", s.tick)
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
}

// Stop terminates the scraping loop and blocks until the run loop has fully
// exited, so the caller can safely close the database afterwards without racing
// an in-flight scrape.
func (s *Scraper) Stop() {
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop = nil
	}
}

func (s *Scraper) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			log.Println("scraper stopped")
			return
		case <-ticker.C:
		}
		// Re-check stop before scraping: the select above can pick a ticker tick
		// even when stop is already closed (both ready), and we must not start a
		// scrape during shutdown.
		select {
		case <-s.stop:
			return
		default:
		}
		if err := s.scrapeOnce(); err != nil {
			log.Printf("scrape error: %v", err)
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
	fetchedURL := ""
	for _, scheme := range []string{"https", "http"} {
		fetchedURL = scheme + "://" + link.URL
		resp, getErr = s.client.Get(fetchedURL)
		if getErr == nil {
			chosenScheme = scheme
			break
		}
	}
	if getErr != nil {
		log.Printf("fetch failed for link id=%d (%s): %v; recording error", link.ID, fetchedURL, getErr)
		return s.repo.SaveScrapeError(link.ID, getErr.Error())
	}
	defer resp.Body.Close()

	log.Printf("scraping link id=%d url=%s", link.ID, fetchedURL)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read failed for link id=%d (%s): %v; recording error", link.ID, fetchedURL, err)
		return s.repo.SaveScrapeError(link.ID, err.Error())
	}

	content := string(body)

	// Spider: extract readable text and discover links, then persist once.
	base := chosenScheme + "://" + link.URL
	readable := ""
	if baseURL, err := url.Parse(base); err == nil {
		if r, rerr := extractReadable(baseURL, content); rerr == nil {
			readable = r
		}
	}

	if err := s.repo.SaveScrapeResult(link.ID, content, readable); err != nil {
		return err
	}

	discovered, err := extractLinks(base, content)
	if err != nil {
		return err
	}
	added, skipped := 0, 0
	for _, u := range discovered {
		if _, err := s.repo.NewLink(u, link.Generation+1); err != nil {
			if errors.Is(err, ErrLinkExists) {
				skipped++
				continue
			}
			return err
		}
		added++
	}

	log.Printf("scraped link id=%d: content=%d bytes, readable=%d bytes, discovered=%d (new=%d, skipped=%d)",
		link.ID, len(content), len(readable), len(discovered), added, skipped)

	return nil
}

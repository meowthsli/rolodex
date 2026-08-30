package grab

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Scraper periodically fetches the next unscraped link, retrieves its content
// over HTTP and records the result back into the repository.
type Scraper struct {
	repo   *LinksRepository
	client *http.Client
	tick   time.Duration
	// blacklist holds normalized URL prefixes. A link whose stored URL starts
	// with any prefix is dropped before being fetched or stored.
	blacklist []string
	// noLinks, when set, makes the spider print discovered links to stdout
	// instead of inserting them into link_queue (a dry-run mode).
	noLinks bool
	stop    chan struct{}
	// done is closed by the run loop when it exits, so Stop can wait for the
	// in-flight scrape to finish before the caller tears down the database.
	done chan struct{}
}

// minURLLen is the shortest a stored (scheme-less) URL may be to be considered a
// real, fetchable page. Shorter links are neither fetched nor extracted.
const minURLLen = 6

// maxRedirects is the largest number of hops the scraper will follow before it
// gives up. The bound doubles as the safety net that stops a redirect cycle
// (A -> B -> A -> ...) from looping forever even before the explicit loop check
// below has a chance to notice the repeated URL.
const maxRedirects = 10

// errRedirectLoop is returned by the scraper's CheckRedirect when the same URL
// reappears in the redirect chain, signaling an endless 3xx cycle. LinkQueue
// records it as a scrape error so the offending link is not retried.
var errRedirectLoop = errors.New("redirect loop detected")

// NewScraper builds a Scraper. A nil client defaults to http.DefaultClient, and
// a non-positive tick defaults to 5 seconds.
func NewScraper(repo *LinksRepository, client *http.Client, tick time.Duration) *Scraper {
	if client == nil {
		client = http.DefaultClient
	}
	if tick <= 0 {
		tick = 5 * time.Second
	}
	// Wrap the caller's client so redirect following is bounded and loop-safe:
	// never chase more than maxRedirects hops, and abort as soon as a URL
	// already seen in the chain is requested again (A -> B -> A). The original
	// CheckRedirect, if any, still gets a chance to veto the hop.
	wrapped := *client
	prevCheckRedirect := wrapped.CheckRedirect
	wrapped.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("redirect: stopped after %d requests", maxRedirects)
		}
		for _, prev := range via {
			if req.URL.String() == prev.URL.String() {
				return errRedirectLoop
			}
		}
		if prevCheckRedirect != nil {
			return prevCheckRedirect(req, via)
		}
		return nil
	}
	client = &wrapped
	return &Scraper{repo: repo, client: client, tick: tick}
}

// SetBlacklist installs a list of banned URL prefixes. Entries should be
// normalized (scheme-less) URLs; matching is done via string prefix against
// each link's stored URL.

// SetNoLinks enables dry-run mode: discovered links are printed to stdout
// rather than inserted into link_queue.
func (s *Scraper) SetNoLinks(on bool) {
	s.noLinks = on
}

// SetBlacklist installs a list of banned URL prefixes. Entries should be
// normalized (scheme-less) URLs; matching is done via string prefix against
// each link's stored URL.
func (s *Scraper) SetBlacklist(entries []string) {
	s.blacklist = entries
}

// LoadBlacklist reads blacklist entries from a file (one prefix per line; blank
// lines and lines starting with '#' are ignored) and returns them normalized
// so they compare directly against the scheme-less URLs stored in link_queue.
func LoadBlacklist(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, normalizeURL(line))
	}
	return out, nil
}

// isBlacklisted reports whether url starts with any installed blacklist prefix.
func (s *Scraper) isBlacklisted(url string) bool {
	for _, p := range s.blacklist {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	return false
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

	// Blacklist: a banned link is erased from the database immediately, before
	// any fetch or content storage, so it is never processed or retried.
	if s.isBlacklisted(link.URL) {
		log.Printf("link id=%d url=%s is blacklisted; erasing from DB", link.ID, link.URL)
		if err := s.repo.DeleteLink(link.ID); err != nil {
			return err
		}
		return nil
	}

	// Too short to be a real page URL: don't fetch it, just record an error so
	// it is not retried.
	if len(link.URL) < minURLLen {
		log.Printf("link id=%d url=%s is too short (<%d); recording error", link.ID, link.URL, minURLLen)
		return s.repo.SaveScrapeError(link.ID, "url too short (<6)")
	}

	var resp *http.Response
	var getErr error
	fetchedURL := ""
	for _, scheme := range []string{"https", "http"} {
		fetchedURL = scheme + "://" + link.URL
		resp, getErr = s.client.Get(fetchedURL)
		if getErr == nil {
			break
		}
	}
	if getErr != nil {
		log.Printf("fetch failed for link id=%d (%s): %v; recording error", link.ID, fetchedURL, getErr)
		return s.repo.SaveScrapeError(link.ID, getErr.Error())
	}
	defer resp.Body.Close()

	// The page that actually answered may differ from the requested URL: 3xx
	// redirects (301/302) are followed automatically and resp.Request.URL is the
	// final hop. Use it as the base for readability extraction and link
	// discovery so relative URLs on the redirected page resolve against the page
	// that really served the content, not the stale original address.
	base := fetchedURL
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL.String()
	}
	if base != fetchedURL {
		log.Printf("scraping link id=%d url=%s (followed redirect from %s)", link.ID, base, fetchedURL)
	} else {
		log.Printf("scraping link id=%d url=%s", link.ID, fetchedURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read failed for link id=%d (%s): %v; recording error", link.ID, fetchedURL, err)
		return s.repo.SaveScrapeError(link.ID, err.Error())
	}

	content := string(body)

	if strings.Contains(content, "qauth.js") {
		// TODO: add chrome in headless mode
	}

	// Too small to be useful: don't store content (and don't spider it). Mark
	// the link with an error so it is not re-fetched or analyzed.
	const minContentBytes = 1024
	if len(content) < minContentBytes {

		log.Printf("scraped link id=%d: content %d bytes < %d; skipping storage",
			link.ID, len(content), minContentBytes)
		return s.repo.SaveScrapeError(link.ID, "content too small (<1KB)")
	}

	// Spider: extract readable text and discover links, then persist once.
	readable := ""
	if baseURL, err := url.Parse(base); err == nil {
		if r, rerr := extractReadable(baseURL, content); rerr == nil {
			readable = r
		}
	}

	if err := s.repo.SaveScrapeResult(link.ID, content, readable); err != nil {
		return err
	}

	if link.Generation <= 2 {
		discovered, err := extractLinks(base, content)
		if err != nil {
			return err
		}
		added, skipped := 0, 0
		for _, u := range discovered {
			// Don't enqueue links that are too short to be real pages.
			if len(normalizeURL(u)) < minURLLen {
				skipped++
				continue
			}
			if s.noLinks {
				// Dry-run mode: report the discovered link instead of
				// inserting it into link_queue.
				fmt.Printf("discovered link: %s\n", u)
				added++
				continue
			}
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
	} else {
		log.Printf("scraped link id=%d: content=%d bytes, readable=%d bytes (generation %d > 2, skipping link discovery)",
			link.ID, len(content), len(readable), link.Generation)
	}

	return nil
}

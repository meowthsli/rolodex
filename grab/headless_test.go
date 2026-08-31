package grab

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFindChromeEnvOverride verifies CHROME_BIN takes precedence when locating
// the Chrome executable, so deployments can point the scraper at a specific
// browser regardless of what is installed system-wide.
func TestFindChromeEnvOverride(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake chrome: %v", err)
	}
	t.Setenv("CHROME_BIN", fake)

	got, err := findChrome()
	if err != nil {
		t.Fatalf("findChrome: %v", err)
	}
	if got != fake {
		t.Errorf("findChrome = %q, want %q", got, fake)
	}
}

// requireChrome skips the test when no Chrome-compatible browser is available,
// since the remaining tests drive a real headless browser over CDP.
func requireChrome(t *testing.T) {
	t.Helper()
	if _, err := findChrome(); err != nil {
		t.Skipf("skipping: no chrome/chromium available: %v", err)
	}
}

// TestFetchWithHeadlessChrome starts a real local page whose inline script
// overwrites an element's text and verifies fetchWithHeadlessChrome returns the
// rendered DOM: the browser must have executed the JavaScript, not the raw
// downloaded HTML.
func TestFetchWithHeadlessChrome(t *testing.T) {
	requireChrome(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><title>T</title></head><body>
			<p id="target">hello plain</p>
			<script>document.getElementById("target").textContent = "hello from js";</script>
		</body></html>`)
	}))
	defer server.Close()

	got, err := fetchWithHeadlessChrome(server.URL)
	if err != nil {
		t.Fatalf("fetchWithHeadlessChrome: %v", err)
	}
	if !strings.Contains(got, "hello from js") {
		t.Errorf("rendered HTML missing JS-executed content: %q", got)
	}
	if strings.Contains(got, "hello plain") {
		t.Errorf("rendered HTML still contains the un-rendered marker: %q", got)
	}
}

// TestFetchWithHeadlessChromeTimeout verifies a browser that never gets an
// answer is torn down when the render deadline passes: the caller receives a
// timeout error quickly instead of waiting forever, i.e. Chrome is always
// closed even on a hang.
func TestFetchWithHeadlessChromeTimeout(t *testing.T) {
	requireChrome(t)

	// A listener that accepts the connection but never replies: the page's load
	// event never fires, which would hang a plain fetch.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	hangURL := "http://" + ln.Addr().String() + "/hang"

	orig := headlessRenderTimeout
	headlessRenderTimeout = 500 * time.Millisecond
	defer func() { headlessRenderTimeout = orig }()

	start := time.Now()
	_, err = fetchWithHeadlessChrome(hangURL)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to mention the timeout", err.Error())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout not honored: took %s", elapsed)
	}
}

// TestScraperRendersQAuthPageWithHeadlessChrome seeds a page whose plain HTTP
// body is only a JS-challenge shell: it mentions qauth.js and builds its real,
// large content in the browser. The scraper must spot the marker, render the
// page with headless Chrome and store that rendered content.
func TestScraperRendersQAuthPageWithHeadlessChrome(t *testing.T) {
	requireChrome(t)

	db := setupTestDB(t)
	repo := NewLinksRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><script src="qauth.js"></script></head><body>
			<script>document.body.innerHTML = "<article><p>rendered-by-chrome: " + new Array(300).join("word ") + "</p></article>";</script>
		</body></html>`)
	}))
	defer server.Close()

	seed, err := repo.NewLink(server.URL+"/gated", 1)
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
		t.Fatalf("gated link recorded an error: %q", got.Error.String)
	}
	if !strings.Contains(got.Content, "rendered-by-chrome") {
		t.Errorf("stored content should be the rendered DOM, got %q", got.Content)
	}
	if len(got.Content) < 1024 {
		t.Errorf("stored content should pass the 1KB threshold, got %d bytes", len(got.Content))
	}
	if got.ReadableText == "" {
		t.Errorf("expected readable_text to be extracted from the rendered content")
	}
}
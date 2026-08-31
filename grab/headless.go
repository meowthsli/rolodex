package grab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// qauthJSMarker is the script filename that signals a page is gated behind a
// JavaScript anti-bot / anti-scraping challenge. Such pages return only an
// empty shell to a plain HTTP fetch and must be executed by a real browser
// before their actual content is available.
const qauthJSMarker = "qauth.js"

// headlessRenderSettle is how long chromedp waits after the page's load event
// before capturing the DOM, giving deferred/async scripts (typical of
// qauth.js-gated pages) time to produce the real content.
const headlessRenderSettle = 3 * time.Second

// headlessRenderTimeout bounds the whole browser run — launch, render and DOM
// capture. When it expires the context is cancelled, which makes chromedp kill
// the browser process, so a stuck page cannot leave Chrome hanging open.
var headlessRenderTimeout = 60 * time.Second

// chromePaths are absolute install locations probed for a Chrome-compatible
// executable, used when none of the PATH names below matches.
var chromePaths = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	"/usr/bin/google-chrome",
	"/usr/bin/google-chrome-stable",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
}

// chromeNames are executables looked up via PATH.
var chromeNames = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"chrome",
	"microsoft-edge",
	"msedge",
	"brave-browser",
}

// findChrome returns the path of an available Chrome-compatible executable.
// The CHROME_BIN environment variable takes precedence; if it is unset or
// points at a missing file, known install paths and PATH lookups are tried.
func findChrome() (string, error) {
	if bin := os.Getenv("CHROME_BIN"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	for _, p := range chromePaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, n := range chromeNames {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no chrome/chromium executable found (set CHROME_BIN)")
}

// fetchWithHeadlessChrome opens headless Chrome via chromedp, navigates to
// rawURL, lets deferred scripts settle and returns the fully rendered document
// HTML. The browser is guaranteed to be closed when the function returns:
// chromedp tears the process down when the allocator context is cancelled (the
// deferred cancelAll/cancel below), and the render timeout hard-cancels it if a
// page hangs. A throwaway user-data-dir profile keeps any concurrently running
// Chrome instance undisturbed.
func fetchWithHeadlessChrome(rawURL string) (string, error) {
	chrome, err := findChrome()
	if err != nil {
		return "", err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-gpu", true),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-extensions", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancelTimeout := context.WithTimeout(ctx, headlessRenderTimeout)
	defer cancelTimeout()

	var html string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(rawURL),
		chromedp.Sleep(headlessRenderSettle),
		chromedp.Evaluate(`document.documentElement.outerHTML`, &html),
	); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("headless chrome render timed out after %s", headlessRenderTimeout)
		}
		return "", fmt.Errorf("headless chrome: %w", err)
	}

	html = strings.TrimSpace(html)
	if html == "" {
		return "", errors.New("headless chrome returned an empty document")
	}
	return html, nil
}

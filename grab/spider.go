package grab

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/gelembjuk/articletext"
	"golang.org/x/net/html"
)

// hrefPattern matches an href attribute with a quoted value (single or double
// quotes), used to strip link targets before readability extraction.
var hrefPattern = regexp.MustCompile(`(?i)href\s*=\s*("([^"]*)"|'([^']*)')`)

// stripHrefs replaces every href target with "#" so the readability extractor
// never surfaces raw link URLs as readable content. The surrounding markup and
// link text are preserved; only the destination is neutralized.
func stripHrefs(html string) string {
	return hrefPattern.ReplaceAllString(html, `href="#"`)
}

// linkAttrs maps HTML element names to the attribute that may hold a link
// target. Only navigational hyperlinks and embedded documents (sources of
// text content) are followed; media, script and other resource references are
// intentionally excluded.
var linkAttrs = map[string]string{
	"a":      "href",
	"area":   "href",
	"iframe": "src",
}

// extractLinks parses htmlBody and returns absolute http/https URLs found in
// link-bearing attributes, resolved against base. Non-http(s) schemes (e.g.
// mailto:, javascript:) and duplicate URLs are omitted.
func extractLinks(base, htmlBody string) ([]string, error) {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil, err
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if attr, ok := linkAttrs[n.Data]; ok {
				for _, a := range n.Attr {
					if a.Key == attr && a.Val != "" {
						raw := strings.TrimSpace(a.Val)
						// Skip links that cannot lead anywhere useful: pure
						// fragments (# / #frag) and URLs shorter than minURLLen
						// as written in the page (e.g. "/exit", "#").
						if raw == "" || strings.HasPrefix(raw, "#") || len(raw) < minURLLen {
							continue
						}
						if ref, err := resolveLink(baseURL, raw); err == nil && ref != "" {
							if _, dup := seen[ref]; !dup {
								seen[ref] = struct{}{}
								out = append(out, ref)
							}
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out, nil
}

// resolveLink resolves a raw attribute value against base, returning only
// http/https URLs. Other schemes resolve to the empty string.
func resolveLink(base *url.URL, raw string) (string, error) {
	rel, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(rel)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", nil
	}
	return resolved.String(), nil
}

// extractReadable returns the main readable text content of an HTML page using
// the articletext library. Link targets are neutralized (href="#") first so the
// extracted text never includes raw URLs.
func extractReadable(_ *url.URL, htmlBody string) (string, error) {
	return articletext.GetArticleText(strings.NewReader(stripHrefs(htmlBody)))
}

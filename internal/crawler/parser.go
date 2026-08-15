package crawler

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ExtractLinks streams and tokenizes an HTML document, returning the
// absolute URLs referenced by anchor (<a href>) tags, resolved against
// base. It uses html.Tokenizer directly rather than building a full DOM
// tree, so memory use stays proportional to a single tag at a time instead
// of the whole document, which matters when auditing large pages.
//
// Malformed HTML is parsed on a best-effort basis, matching browser
// behavior; a non-nil error is only returned for unrecoverable I/O errors
// encountered while reading body, and any links found before the error are
// still returned alongside it.
func ExtractLinks(body io.Reader, base *url.URL) ([]string, error) {
	tokenizer := html.NewTokenizer(body)
	var links []string

	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != io.EOF {
				return links, err
			}
			return links, nil

		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data != "a" {
				continue
			}
			href, ok := attrValue(token, "href")
			if !ok {
				continue
			}
			resolved, ok := resolveHref(base, href)
			if !ok {
				continue
			}
			links = append(links, resolved)
		}
	}
}

// attrValue returns the value of the named attribute on token, if present.
func attrValue(token html.Token, name string) (string, bool) {
	for _, a := range token.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

// resolveHref resolves a raw href value against base, filtering out
// fragment-only links (e.g. "#section") and non-HTTP(S) schemes such as
// mailto:, tel:, javascript:, and data: URIs, none of which are crawlable
// web resources.
func resolveHref(base *url.URL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false
	}

	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}

	resolved := base.ResolveReference(ref)

	switch strings.ToLower(resolved.Scheme) {
	case "http", "https":
	default:
		return "", false
	}

	return resolved.String(), true
}

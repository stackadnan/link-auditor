package crawler

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	base, err := url.Parse("https://example.com/blog/post")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}

	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "absolute and relative links",
			html: `<html><body>
				<a href="https://other.com/page">absolute</a>
				<a href="/root-relative">root relative</a>
				<a href="sibling">path relative</a>
				<a href="../parent">parent relative</a>
			</body></html>`,
			want: []string{
				"https://other.com/page",
				"https://example.com/root-relative",
				"https://example.com/blog/sibling",
				"https://example.com/parent",
			},
		},
		{
			name: "fragment-only and empty hrefs are skipped",
			html: `<a href="#top">top</a><a href="">empty</a><a>no href</a>`,
			want: nil,
		},
		{
			name: "fragment on a real path is retained (stripping happens later, in NormalizeURL)",
			html: `<a href="/page#section">jump</a>`,
			want: []string{"https://example.com/page#section"},
		},
		{
			name: "non-http schemes are skipped",
			html: `
				<a href="mailto:hello@example.com">mail</a>
				<a href="tel:+15551234567">tel</a>
				<a href="javascript:void(0)">js</a>
				<a href="data:text/plain;base64,aGVsbG8=">data</a>
				<a href="https://example.com/keep">keep</a>`,
			want: []string{"https://example.com/keep"},
		},
		{
			name: "self-closing anchor tag",
			html: `<a href="/self-closed"/>`,
			want: []string{"https://example.com/self-closed"},
		},
		{
			name: "duplicate hrefs on the same page are both returned",
			html: `<a href="/dup">one</a><a href="/dup">two</a>`,
			want: []string{"https://example.com/dup", "https://example.com/dup"},
		},
		{
			name: "malformed HTML is parsed on a best-effort basis",
			html: `<div><a href="/unclosed">no closing tag<div><a href="/second">second</a>`,
			want: []string{"https://example.com/unclosed", "https://example.com/second"},
		},
		{
			name: "non-anchor tags with href are ignored",
			html: `<link rel="stylesheet" href="/style.css"><a href="/actual-link">link</a>`,
			want: []string{"https://example.com/actual-link"},
		},
		{
			name: "empty document",
			html: ``,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractLinks(strings.NewReader(tt.html), base)
			if err != nil {
				t.Fatalf("ExtractLinks() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractLinks() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// Note: the "fragment on a real path" case above resolves "/page#section"
// against the base URL and drops the fragment during resolution's string
// form only insofar as ResolveReference retains it; the crawler's own
// NormalizeURL (tested separately) is what strips fragments for
// deduplication purposes. This test simply confirms ExtractLinks does not
// discard a link merely because it carries a fragment.
func TestExtractLinks_FragmentIsPresentBeforeNormalization(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	got, err := ExtractLinks(strings.NewReader(`<a href="/page#section">x</a>`), base)
	if err != nil {
		t.Fatalf("ExtractLinks() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 link, got %d: %#v", len(got), got)
	}
	if got[0] != "https://example.com/page#section" {
		t.Errorf("ExtractLinks() = %q, want fragment retained pre-normalization", got[0])
	}
}

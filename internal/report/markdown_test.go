package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

func TestMarkdownFormatter_Generate(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/", StatusCode: 200, LinkType: crawler.Internal, Crawled: true},
		{URL: "https://example.com/missing", StatusCode: 404, LinkType: crawler.Internal},
		{URL: "https://example.com/redirect", StatusCode: 302, RedirectTo: "https://example.com/new", LinkType: crawler.Internal},
	}
	summary := BuildSummary("https://example.com/", results, 1500*time.Millisecond, false)

	var buf bytes.Buffer
	f := &MarkdownFormatter{}
	if err := f.Generate(&buf, results, summary, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"# Link Audit Report",
		"## Summary",
		"| Target | https://example.com/ |",
		"| Pages crawled | 1 |",
		"| Links checked | 3 |",
		"## Links",
		"https://example.com/missing",
		"→ https://example.com/new",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SSL Certificate") {
		t.Error("output should not contain an SSL section when sslInfo is nil")
	}
}

func TestMarkdownFormatter_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	f := &MarkdownFormatter{}
	if err := f.Generate(&buf, nil, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "No links were checked") {
		t.Errorf("expected an empty-results notice, got:\n%s", buf.String())
	}
}

func TestMarkdownFormatter_PagesLimitedNotice(t *testing.T) {
	summary := Summary{PagesLimited: true}
	var buf bytes.Buffer
	f := &MarkdownFormatter{}
	if err := f.Generate(&buf, nil, summary, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "--max-pages") {
		t.Errorf("expected a --max-pages limited notice, got:\n%s", buf.String())
	}
}

func TestMarkdownFormatter_SSLSection(t *testing.T) {
	tests := []struct {
		name string
		info *checker.SSLInfo
		want string
	}{
		{
			name: "healthy certificate",
			info: &checker.SSLInfo{Host: "example.com", DaysRemaining: 90, NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
			want: "✅ Healthy",
		},
		{
			name: "expired certificate",
			info: &checker.SSLInfo{Host: "example.com", Expired: true},
			want: "❌ Expired",
		},
		{
			name: "expiring soon",
			info: &checker.SSLInfo{Host: "example.com", ExpiringSoon: true},
			want: "⚠️ Expiring soon",
		},
		{
			name: "check failed",
			info: &checker.SSLInfo{Host: "example.com", Error: "connection refused"},
			want: "❌ Certificate check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := &MarkdownFormatter{}
			if err := f.Generate(&buf, nil, Summary{}, tt.info); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output does not contain %q:\n%s", tt.want, buf.String())
			}
		})
	}
}

// TestEscapeMarkdown verifies pipes and newlines (which would otherwise
// break a GFM table row) are neutralized.
func TestEscapeMarkdown(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"a|b", "a\\|b"},
		{"line1\nline2", "line1 line2"},
		{"a|b\nc|d", "a\\|b c\\|d"},
	}
	for _, tt := range tests {
		if got := escapeMarkdown(tt.in); got != tt.want {
			t.Errorf("escapeMarkdown(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestMarkdownFormatter_SkipReasonNoted verifies a skipped link's reason
// (ignore-external, robots.txt) shows up in the Notes column instead of
// being silently blank.
func TestMarkdownFormatter_SkipReasonNoted(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://blocked.example.com/", Skipped: true, SkipReason: "robots.txt", LinkType: crawler.Internal},
	}
	var buf bytes.Buffer
	f := &MarkdownFormatter{}
	if err := f.Generate(&buf, results, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "skipped: robots.txt") {
		t.Errorf("expected the skip reason in the report, got:\n%s", buf.String())
	}
}

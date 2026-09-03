package report

import (
	"testing"
	"time"

	"github.com/stackadnan/link-auditor/internal/crawler"
)

func TestBuildSummary(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/", StatusCode: 200, LinkType: crawler.Internal, Crawled: true},
		{URL: "https://example.com/ok2", StatusCode: 204, LinkType: crawler.Internal},
		{URL: "https://example.com/redirect", StatusCode: 302, LinkType: crawler.Internal},
		{URL: "https://example.com/missing", StatusCode: 404, LinkType: crawler.Internal},
		{URL: "https://example.com/error", Error: "DNS lookup failed", LinkType: crawler.Internal},
		{URL: "https://other.com/", StatusCode: 200, LinkType: crawler.External},
		{URL: "https://skipped.example.com/", Skipped: true, SkipReason: "ignore-external", LinkType: crawler.External},
	}

	s := BuildSummary("https://example.com/", results, 2500*time.Millisecond, true)

	if s.Target != "https://example.com/" {
		t.Errorf("Target = %q, want %q", s.Target, "https://example.com/")
	}
	if s.TotalChecked != 7 {
		t.Errorf("TotalChecked = %d, want 7", s.TotalChecked)
	}
	if s.PagesCrawled != 1 {
		t.Errorf("PagesCrawled = %d, want 1", s.PagesCrawled)
	}
	if s.OK != 3 {
		t.Errorf("OK = %d, want 3 (200, 204, and the external 200)", s.OK)
	}
	if s.Redirects != 1 {
		t.Errorf("Redirects = %d, want 1", s.Redirects)
	}
	if s.Broken != 2 {
		t.Errorf("Broken = %d, want 2 (404 and the DNS error)", s.Broken)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.Skipped)
	}
	if s.Internal != 5 {
		t.Errorf("Internal = %d, want 5", s.Internal)
	}
	if s.External != 2 {
		t.Errorf("External = %d, want 2", s.External)
	}
	if s.Duration != 2500*time.Millisecond {
		t.Errorf("Duration = %v, want 2.5s", s.Duration)
	}
	if !s.PagesLimited {
		t.Error("PagesLimited = false, want true")
	}
}

func TestBuildSummary_Empty(t *testing.T) {
	s := BuildSummary("https://example.com/", nil, time.Second, false)
	if s.TotalChecked != 0 || s.OK != 0 || s.Broken != 0 {
		t.Errorf("BuildSummary(nil) = %+v, want all-zero counts", s)
	}
	if s.PagesLimited {
		t.Error("PagesLimited = true, want false")
	}
}

func TestNewFormatter(t *testing.T) {
	tests := []struct {
		output  string
		want    string // Go type name, checked via a type switch below
		wantErr bool
	}{
		{"table", "*report.TableFormatter", false},
		{"", "*report.TableFormatter", false}, // empty defaults to table
		{"json", "*report.JSONFormatter", false},
		{"markdown", "*report.MarkdownFormatter", false},
		{"md", "*report.MarkdownFormatter", false},
		{"yaml", "", true},
		{"TABLE", "", true}, // case-sensitive: not one of the documented aliases
	}

	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			f, err := NewFormatter(tt.output, false)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewFormatter(%q) error = %v, wantErr %v", tt.output, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			switch tt.want {
			case "*report.TableFormatter":
				if _, ok := f.(*TableFormatter); !ok {
					t.Errorf("NewFormatter(%q) = %T, want *TableFormatter", tt.output, f)
				}
			case "*report.JSONFormatter":
				if _, ok := f.(*JSONFormatter); !ok {
					t.Errorf("NewFormatter(%q) = %T, want *JSONFormatter", tt.output, f)
				}
			case "*report.MarkdownFormatter":
				if _, ok := f.(*MarkdownFormatter); !ok {
					t.Errorf("NewFormatter(%q) = %T, want *MarkdownFormatter", tt.output, f)
				}
			}
		})
	}
}

func TestNewFormatter_NoColorOnlyAffectsTable(t *testing.T) {
	f, err := NewFormatter("table", true)
	if err != nil {
		t.Fatalf("NewFormatter() error = %v", err)
	}
	tf, ok := f.(*TableFormatter)
	if !ok {
		t.Fatalf("NewFormatter(table, true) = %T, want *TableFormatter", f)
	}
	if !tf.NoColor {
		t.Error("NoColor = false, want true when exportingToFile is true")
	}
}

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		name string
		r    crawler.Result
		want string
	}{
		{"skipped takes priority", crawler.Result{Skipped: true, StatusCode: 500, Error: "x"}, "SKIP"},
		{"network error", crawler.Result{Error: "connection refused"}, "ERR"},
		{"status code", crawler.Result{StatusCode: 404}, "404"},
		{"zero status without error", crawler.Result{}, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusLabel(tt.r); got != tt.want {
				t.Errorf("StatusLabel(%+v) = %q, want %q", tt.r, got, tt.want)
			}
		})
	}
}

func TestSortResults(t *testing.T) {
	input := []crawler.Result{
		{URL: "https://example.com/z", StatusCode: 200},
		{URL: "https://example.com/a", StatusCode: 404},
		{URL: "https://example.com/m", StatusCode: 200},
		{URL: "https://example.com/b", StatusCode: 500},
	}

	got := SortResults(input)

	want := []string{
		"https://example.com/a", // broken, alphabetically first among broken
		"https://example.com/b", // broken
		"https://example.com/m", // healthy, alphabetically first among healthy
		"https://example.com/z", // healthy
	}
	if len(got) != len(want) {
		t.Fatalf("SortResults() returned %d results, want %d", len(got), len(want))
	}
	for i, url := range want {
		if got[i].URL != url {
			t.Errorf("SortResults()[%d].URL = %q, want %q", i, got[i].URL, url)
		}
	}

	// The input slice must not be mutated.
	if input[0].URL != "https://example.com/z" {
		t.Error("SortResults() mutated its input slice")
	}
}

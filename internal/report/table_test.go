package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

func TestTableFormatter_Generate(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/", StatusCode: 200, LinkType: crawler.Internal, Crawled: true},
		{URL: "https://example.com/missing", StatusCode: 404, LinkType: crawler.Internal},
	}
	summary := BuildSummary("https://example.com/", results, time.Second, false)

	var buf bytes.Buffer
	f := &TableFormatter{NoColor: true}
	if err := f.Generate(&buf, results, summary, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"Link Audit Report",
		"Target:     https://example.com/",
		"Pages:      1 crawled",
		"Summary",
		"https://example.com/missing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestTableFormatter_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	f := &TableFormatter{NoColor: true}
	if err := f.Generate(&buf, nil, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "No links were checked") {
		t.Errorf("expected an empty-results notice, got:\n%s", buf.String())
	}
}

func TestTableFormatter_NoColorSuppressesANSI(t *testing.T) {
	var buf bytes.Buffer
	f := &TableFormatter{NoColor: true}
	results := []crawler.Result{{URL: "https://example.com/", StatusCode: 500}}
	if err := f.Generate(&buf, results, BuildSummary("https://example.com/", results, 0, false), nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("output contains ANSI escape codes despite NoColor=true:\n%q", buf.String())
	}
}

func TestTableFormatter_PagesLimitedNotice(t *testing.T) {
	var buf bytes.Buffer
	f := &TableFormatter{NoColor: true}
	if err := f.Generate(&buf, nil, Summary{PagesLimited: true}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "--max-pages") {
		t.Errorf("expected a --max-pages limited notice, got:\n%s", buf.String())
	}
}

func TestTableFormatter_SSLSection(t *testing.T) {
	tests := []struct {
		name string
		info *checker.SSLInfo
		want string
	}{
		{"healthy", &checker.SSLInfo{Host: "example.com"}, "HEALTHY"},
		{"expired", &checker.SSLInfo{Host: "example.com", Expired: true}, "EXPIRED"},
		{"expiring soon", &checker.SSLInfo{Host: "example.com", ExpiringSoon: true}, "EXPIRING SOON"},
		{"failed", &checker.SSLInfo{Host: "example.com", Error: "connection refused"}, "FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := &TableFormatter{NoColor: true}
			if err := f.Generate(&buf, nil, Summary{}, tt.info); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if !strings.Contains(buf.String(), tt.want) {
				t.Errorf("output does not contain %q:\n%s", tt.want, buf.String())
			}
		})
	}
}

func TestTableFormatter_SkipReasonNoted(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/x", Skipped: true, SkipReason: "ignore-external", LinkType: crawler.External},
	}
	var buf bytes.Buffer
	f := &TableFormatter{NoColor: true}
	if err := f.Generate(&buf, results, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !strings.Contains(buf.String(), "skipped: ignore-external") {
		t.Errorf("expected the skip reason in the report, got:\n%s", buf.String())
	}
}

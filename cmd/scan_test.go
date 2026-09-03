package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/report"
)

func validScanOptions() *scanOptions {
	return &scanOptions{
		concurrency:    20,
		depth:          3,
		timeout:        10,
		maxPages:       0,
		maxBodySize:    10 << 20,
		output:         "table",
		checkSSL:       true,
		sslWarningDays: 30,
		failOn:         []string{"broken"},
	}
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*scanOptions)
		wantErr bool
	}{
		{"defaults are valid", func(*scanOptions) {}, false},
		{"concurrency zero is invalid", func(o *scanOptions) { o.concurrency = 0 }, true},
		{"concurrency negative is invalid", func(o *scanOptions) { o.concurrency = -1 }, true},
		{"depth zero is valid (root only)", func(o *scanOptions) { o.depth = 0 }, false},
		{"depth negative is invalid", func(o *scanOptions) { o.depth = -1 }, true},
		{"timeout zero is invalid", func(o *scanOptions) { o.timeout = 0 }, true},
		{"max-pages zero is valid (unlimited)", func(o *scanOptions) { o.maxPages = 0 }, false},
		{"max-pages positive is valid", func(o *scanOptions) { o.maxPages = 100 }, false},
		{"max-pages negative is invalid", func(o *scanOptions) { o.maxPages = -1 }, true},
		{"max-body-size zero is invalid", func(o *scanOptions) { o.maxBodySize = 0 }, true},
		{"max-body-size negative is invalid", func(o *scanOptions) { o.maxBodySize = -1 }, true},
		{"ssl-warning-days zero is valid", func(o *scanOptions) { o.sslWarningDays = 0 }, false},
		{"ssl-warning-days negative is invalid", func(o *scanOptions) { o.sslWarningDays = -1 }, true},
		{"blank user-agent is invalid", func(o *scanOptions) { o.userAgent = "   " }, true},
		{"empty user-agent means default, is valid", func(o *scanOptions) { o.userAgent = "" }, false},
		{"explicit user-agent is valid", func(o *scanOptions) { o.userAgent = "custom/1.0" }, false},
		{"fail-on broken is valid", func(o *scanOptions) { o.failOn = []string{"broken"} }, false},
		{"fail-on any is valid", func(o *scanOptions) { o.failOn = []string{"any"} }, false},
		{"fail-on multiple valid values", func(o *scanOptions) { o.failOn = []string{"broken", "ssl"} }, false},
		{"fail-on unknown value is invalid", func(o *scanOptions) { o.failOn = []string{"nonsense"} }, true},
		{"fail-on empty list is valid (never fails)", func(o *scanOptions) { o.failOn = nil }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := validScanOptions()
			tt.mutate(opts)
			err := validateOptions(opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateOptions() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, errInvalidUsage) {
				t.Errorf("validateOptions() error %v does not wrap errInvalidUsage", err)
			}
		})
	}
}

func TestShouldFail(t *testing.T) {
	brokenSummary := report.Summary{Broken: 1}
	redirectSummary := report.Summary{Redirects: 1}
	cleanSummary := report.Summary{OK: 5}
	expiredSSL := &checker.SSLInfo{Expired: true}
	expiringSSL := &checker.SSLInfo{ExpiringSoon: true}
	failedSSL := &checker.SSLInfo{Error: "connection refused"}
	healthySSL := &checker.SSLInfo{}

	tests := []struct {
		name    string
		failOn  []string
		summary report.Summary
		ssl     *checker.SSLInfo
		want    bool
	}{
		{"broken policy with broken links fails", []string{"broken"}, brokenSummary, nil, true},
		{"broken policy with only redirects does not fail", []string{"broken"}, redirectSummary, nil, false},
		{"redirect policy with redirects fails", []string{"redirect"}, redirectSummary, nil, true},
		{"redirect policy with only broken links does not fail", []string{"redirect"}, brokenSummary, nil, false},
		{"ssl policy with expired cert fails", []string{"ssl"}, cleanSummary, expiredSSL, true},
		{"ssl policy with expiring-soon cert fails", []string{"ssl"}, cleanSummary, expiringSSL, true},
		{"ssl policy with a failed check fails", []string{"ssl"}, cleanSummary, failedSSL, true},
		{"ssl policy with a healthy cert does not fail", []string{"ssl"}, cleanSummary, healthySSL, false},
		{"ssl policy with no SSL info does not fail", []string{"ssl"}, cleanSummary, nil, false},
		{"any policy with broken links fails", []string{"any"}, brokenSummary, nil, true},
		{"any policy with redirects fails", []string{"any"}, redirectSummary, nil, true},
		{"any policy with expired ssl fails", []string{"any"}, cleanSummary, expiredSSL, true},
		{"any policy with a clean scan does not fail", []string{"any"}, cleanSummary, healthySSL, false},
		{"multiple policies, any match fails", []string{"redirect", "ssl"}, brokenSummary, healthySSL, false},
		{"multiple policies, one matches fails", []string{"redirect", "ssl"}, redirectSummary, healthySSL, true},
		{"empty policy never fails", nil, brokenSummary, expiredSSL, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFail(tt.failOn, tt.summary, tt.ssl); got != tt.want {
				t.Errorf("shouldFail(%v, %+v, %+v) = %v, want %v", tt.failOn, tt.summary, tt.ssl, got, tt.want)
			}
		})
	}
}

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"bare hostname defaults to https", "example.com", "https://example.com", false},
		{"explicit https is preserved", "https://example.com", "https://example.com", false},
		{"explicit http is preserved", "http://example.com", "http://example.com", false},
		{"path is preserved", "example.com/path", "https://example.com/path", false},
		{"missing host is an error", "https://", "", true},
		{"unparseable URL is an error", "http://[::1", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTarget(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeTarget(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("normalizeTarget(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHostForSSL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"https target returns host", "https://example.com/page", "example.com", false},
		{"https target with port", "https://example.com:8443/page", "example.com:8443", false},
		{"http target is rejected", "http://example.com", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hostForSSL(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("hostForSSL(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("hostForSSL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEffectiveUserAgent(t *testing.T) {
	original := Version
	Version = "1.2.3"
	defer func() { Version = original }()

	if got, want := effectiveUserAgent(""), "link-auditor/1.2.3"; got != want {
		t.Errorf("effectiveUserAgent(\"\") = %q, want %q", got, want)
	}
	if got, want := effectiveUserAgent("custom/9.9"), "custom/9.9"; got != want {
		t.Errorf("effectiveUserAgent(explicit) = %q, want %q", got, want)
	}
}

// TestRunScan_EndToEnd exercises the scan subcommand against a real local
// httptest server, covering the table, JSON, and Markdown output formats,
// --export-file, and a custom --user-agent, end to end through the CLI
// (rather than calling internal functions directly).
func TestRunScan_EndToEnd(t *testing.T) {
	var gotUserAgent string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/broken">broken</a>`)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	for _, format := range []string{"table", "json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			cmd := newRootCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{
				"scan", site.URL,
				"--allow-private-addresses",
				"--check-ssl=false",
				"--output", format,
				"--user-agent", "e2e-test/1.0",
			})

			err := cmd.Execute()
			if !errors.Is(err, errBlockingFindings) {
				t.Fatalf("Execute() error = %v, want errBlockingFindings (the /broken link)", err)
			}
			if out.Len() == 0 {
				t.Error("expected report output, got none")
			}
			if gotUserAgent != "e2e-test/1.0" {
				t.Errorf("request User-Agent = %q, want %q", gotUserAgent, "e2e-test/1.0")
			}
		})
	}
}

// TestRunScan_ExportFile verifies --export-file writes the report to disk
// instead of stdout, and that a clean scan (no broken links) exits nil.
func TestRunScan_ExportFile(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>ok</body></html>`)
	}))
	defer site.Close()

	dir := t.TempDir()
	exportPath := filepath.Join(dir, "report.json")

	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"scan", site.URL,
		"--allow-private-addresses",
		"--check-ssl=false",
		"--output", "json",
		"--export-file", exportPath,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil (no broken links)", err)
	}

	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("reading export file: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("export file is not valid JSON: %v", err)
	}
	if !strings.Contains(out.String(), "Report written to") {
		t.Errorf("stdout = %q, want a confirmation that the report was written", out.String())
	}
}

// TestRunScan_MaxPages verifies the --max-pages flag is honored end to end
// and that the summary reports the crawl as limited.
func TestRunScan_MaxPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/b">b</a>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"scan", site.URL,
		"--allow-private-addresses",
		"--check-ssl=false",
		"--concurrency", "1",
		"--max-pages", "2",
		"--output", "json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	var parsed struct {
		Summary report.Summary `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if parsed.Summary.TotalChecked != 2 {
		t.Errorf("total_checked = %d, want 2", parsed.Summary.TotalChecked)
	}
	if !parsed.Summary.PagesLimited {
		t.Error("pages_limited = false, want true")
	}
}

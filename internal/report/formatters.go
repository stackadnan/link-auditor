// Package report renders a completed scan's results into a specific output
// format (table, JSON, or Markdown) via the Formatter strategy interface.
package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

// Formatter generates a report for a completed scan, writing it to w.
// Implementations must not retain w after Generate returns.
type Formatter interface {
	Generate(w io.Writer, results []crawler.Result, summary Summary, sslInfo *checker.SSLInfo) error
}

// Summary aggregates scan-wide statistics, computed once by BuildSummary
// and shared across every report format so each Formatter doesn't need to
// recompute it independently.
type Summary struct {
	// Target is the normalized URL the scan started from.
	Target       string `json:"target"`
	TotalChecked int    `json:"total_checked"`
	// PagesCrawled counts internal HTML pages whose body was parsed for
	// outbound links (crawler.Result.Crawled), a narrower count than
	// TotalChecked: every checked link is a "link", but only a crawled
	// page is also a "page".
	PagesCrawled int           `json:"pages_crawled"`
	OK           int           `json:"ok"`
	Redirects    int           `json:"redirects"`
	Broken       int           `json:"broken"`
	Skipped      int           `json:"skipped"`
	Internal     int           `json:"internal"`
	External     int           `json:"external"`
	Duration     time.Duration `json:"duration_ns"`
	// PagesLimited is true when --max-pages caused one or more discovered
	// URLs to be dropped rather than checked, i.e. the results below are a
	// partial view of the site rather than its entirety.
	PagesLimited bool `json:"pages_limited,omitempty"`
}

// BuildSummary aggregates a completed scan's results into a Summary.
// pagesLimited should be the crawler's own Crawler.PagesLimited() for this
// run, since whether the page budget was exhausted cannot be derived from
// results alone (a dropped URL never becomes a Result).
func BuildSummary(target string, results []crawler.Result, duration time.Duration, pagesLimited bool) Summary {
	s := Summary{Target: target, Duration: duration, PagesLimited: pagesLimited}
	for _, r := range results {
		s.TotalChecked++
		if r.Crawled {
			s.PagesCrawled++
		}

		switch {
		case r.Skipped:
			s.Skipped++
		case r.Broken():
			s.Broken++
		case r.StatusCode >= 300 && r.StatusCode < 400:
			s.Redirects++
		case r.StatusCode >= 200 && r.StatusCode < 300:
			s.OK++
		}

		if r.LinkType == crawler.Internal {
			s.Internal++
		} else {
			s.External++
		}
	}
	return s
}

// NewFormatter returns the Formatter registered for the given output name
// (table, json, or markdown). exportingToFile should be true whenever the
// caller is about to write the report to a file rather than a terminal; the
// TableFormatter uses it to suppress ANSI color codes that would otherwise
// pollute the saved file.
func NewFormatter(output string, exportingToFile bool) (Formatter, error) {
	switch output {
	case "", "table":
		return &TableFormatter{NoColor: exportingToFile}, nil
	case "json":
		return &JSONFormatter{}, nil
	case "markdown", "md":
		return &MarkdownFormatter{}, nil
	default:
		return nil, fmt.Errorf("unknown output format %q (want table, json, or markdown)", output)
	}
}

// StatusLabel returns a short, format-agnostic label for a result's
// outcome: its numeric status code, "ERR" for network/DNS/timeout
// failures, or "SKIP" for links excluded via --ignore-external.
func StatusLabel(r crawler.Result) string {
	switch {
	case r.Skipped:
		return "SKIP"
	case r.Error != "":
		return "ERR"
	default:
		return strconv.Itoa(r.StatusCode)
	}
}

// SortResults orders results with broken links first (the most actionable
// entries), then alphabetically by URL, without mutating the input slice.
func SortResults(results []crawler.Result) []crawler.Result {
	sorted := make([]crawler.Result, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Broken() != sorted[j].Broken() {
			return sorted[i].Broken()
		}
		return sorted[i].URL < sorted[j].URL
	})
	return sorted
}

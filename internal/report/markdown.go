package report

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

// MarkdownFormatter renders a scan as GitHub-flavored Markdown, suitable
// for posting as a pull request comment or committing as an audit log.
type MarkdownFormatter struct{}

// Generate implements Formatter.
func (f *MarkdownFormatter) Generate(w io.Writer, results []crawler.Result, summary Summary, sslInfo *checker.SSLInfo) error {
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "# Link Audit Report\n\n")
	fmt.Fprintf(bw, "_Generated %s_\n\n", time.Now().UTC().Format(time.RFC1123))

	writeMarkdownSummary(bw, summary)
	writeMarkdownSSL(bw, sslInfo)

	fmt.Fprintf(bw, "## Links\n\n")
	if len(results) == 0 {
		fmt.Fprintf(bw, "_No links were checked._\n")
		return bw.Flush()
	}

	fmt.Fprintf(bw, "| Status | URL | Type | Depth | Time | Notes |\n")
	fmt.Fprintf(bw, "|---|---|---|---|---|---|\n")
	for _, r := range SortResults(results) {
		fmt.Fprintf(bw, "| %s | %s | %s | %d | %s | %s |\n",
			StatusLabel(r), escapeMarkdown(r.URL), r.LinkType, r.Depth,
			r.ResponseTime.Round(time.Millisecond), markdownNotes(r))
	}

	return bw.Flush()
}

func writeMarkdownSummary(w io.Writer, s Summary) {
	fmt.Fprintf(w, "## Summary\n\n")
	fmt.Fprintf(w, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(w, "| Target | %s |\n", escapeMarkdown(s.Target))
	fmt.Fprintf(w, "| Pages crawled | %d |\n", s.PagesCrawled)
	fmt.Fprintf(w, "| Links checked | %d |\n", s.TotalChecked)
	fmt.Fprintf(w, "| OK (2xx) | %d |\n", s.OK)
	fmt.Fprintf(w, "| Redirects (3xx) | %d |\n", s.Redirects)
	fmt.Fprintf(w, "| Broken (4xx/5xx/errors) | %d |\n", s.Broken)
	fmt.Fprintf(w, "| Skipped | %d |\n", s.Skipped)
	fmt.Fprintf(w, "| Internal / External | %d / %d |\n", s.Internal, s.External)
	fmt.Fprintf(w, "| Duration | %s |\n", s.Duration.Round(time.Millisecond))
	if s.PagesLimited {
		fmt.Fprintf(w, "| Limited | ⚠️ crawl stopped at the `--max-pages` limit; results are a partial view of the site |\n")
	}
	fmt.Fprintf(w, "\n")
}

func writeMarkdownSSL(w io.Writer, sslInfo *checker.SSLInfo) {
	if sslInfo == nil {
		return
	}

	fmt.Fprintf(w, "## SSL Certificate\n\n")
	if sslInfo.Error != "" {
		fmt.Fprintf(w, "❌ Certificate check failed for `%s`: %s\n\n", sslInfo.Host, sslInfo.Error)
		return
	}

	status := "✅ Healthy"
	switch {
	case sslInfo.Expired:
		status = "❌ Expired"
	case sslInfo.ExpiringSoon:
		status = "⚠️ Expiring soon"
	}

	fmt.Fprintf(w, "| Field | Value |\n|---|---|\n")
	fmt.Fprintf(w, "| Host | %s |\n", sslInfo.Host)
	fmt.Fprintf(w, "| Status | %s |\n", status)
	fmt.Fprintf(w, "| Issuer | %s |\n", sslInfo.Issuer)
	fmt.Fprintf(w, "| Expires | %s |\n", sslInfo.NotAfter.Format("2006-01-02"))
	fmt.Fprintf(w, "| Days remaining | %d |\n\n", sslInfo.DaysRemaining)
}

func markdownNotes(r crawler.Result) string {
	switch {
	case r.Skipped && r.SkipReason != "":
		return "skipped: " + escapeMarkdown(r.SkipReason)
	case r.Error != "":
		return escapeMarkdown(r.Error)
	case r.RedirectTo != "":
		return "→ " + escapeMarkdown(r.RedirectTo)
	default:
		return ""
	}
}

// escapeMarkdown neutralizes characters that would otherwise break a GFM
// table row (pipes) or its single-line-per-row structure (newlines).
func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer("|", "\\|", "\n", " ")
	return replacer.Replace(s)
}

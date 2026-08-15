package report

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

// TableFormatter renders a scan as a colored, human-readable table on the
// terminal, followed by a summary block and an SSL certificate health
// card.
type TableFormatter struct {
	// NoColor disables ANSI color codes, for use when the table is being
	// written somewhere other than an interactive terminal (e.g. a file
	// via --export-file). fatih/color additionally auto-detects the
	// NO_COLOR environment variable and non-TTY stdout on its own; this
	// field covers the one case it cannot: writing to an explicit export
	// file while stdout itself is still attached to a terminal.
	NoColor bool
}

// Generate implements Formatter.
func (f *TableFormatter) Generate(w io.Writer, results []crawler.Result, summary Summary, sslInfo *checker.SSLInfo) error {
	fmt.Fprintln(w, f.style(color.Bold, "Link Audit Report"))
	fmt.Fprintln(w)

	if err := f.renderLinks(w, results); err != nil {
		return err
	}

	fmt.Fprintln(w)
	f.renderSummary(w, summary)

	if sslInfo != nil {
		fmt.Fprintln(w)
		f.renderSSL(w, sslInfo)
	}

	return nil
}

func (f *TableFormatter) renderLinks(w io.Writer, results []crawler.Result) error {
	if len(results) == 0 {
		fmt.Fprintln(w, "No links were checked.")
		return nil
	}

	t := tablewriter.NewWriter(w)
	t.Header("STATUS", "URL", "TYPE", "TIME", "NOTES")

	for _, r := range SortResults(results) {
		row := []interface{}{
			f.statusCell(r),
			r.URL,
			string(r.LinkType),
			r.ResponseTime.Round(time.Millisecond).String(),
			tableNotes(r),
		}
		if err := t.Append(row...); err != nil {
			return fmt.Errorf("rendering table row for %s: %w", r.URL, err)
		}
	}

	return t.Render()
}

// statusCell renders a result's status label colored by severity: green
// for success, yellow for redirects, red for broken links and errors, and
// dim for skipped links.
func (f *TableFormatter) statusCell(r crawler.Result) string {
	label := StatusLabel(r)
	switch {
	case r.Skipped:
		return f.style(color.Faint, label)
	case r.Broken():
		return f.style(color.FgRed, label)
	case r.StatusCode >= 300 && r.StatusCode < 400:
		return f.style(color.FgYellow, label)
	default:
		return f.style(color.FgGreen, label)
	}
}

func tableNotes(r crawler.Result) string {
	switch {
	case r.Error != "":
		return r.Error
	case r.RedirectTo != "":
		return "-> " + r.RedirectTo
	default:
		return ""
	}
}

func (f *TableFormatter) renderSummary(w io.Writer, s Summary) {
	fmt.Fprintln(w, f.style(color.Bold, "Summary"))
	fmt.Fprintf(w, "  Checked:    %d  (%s internal / %s external)\n",
		s.TotalChecked, f.style(color.FgCyan, strconv.Itoa(s.Internal)), f.style(color.FgCyan, strconv.Itoa(s.External)))
	fmt.Fprintf(w, "  OK:         %s\n", f.style(color.FgGreen, strconv.Itoa(s.OK)))
	fmt.Fprintf(w, "  Redirects:  %s\n", f.style(color.FgYellow, strconv.Itoa(s.Redirects)))
	fmt.Fprintf(w, "  Broken:     %s\n", f.style(color.FgRed, strconv.Itoa(s.Broken)))
	if s.Skipped > 0 {
		fmt.Fprintf(w, "  Skipped:    %d\n", s.Skipped)
	}
	fmt.Fprintf(w, "  Duration:   %s\n", s.Duration.Round(time.Millisecond))
}

func (f *TableFormatter) renderSSL(w io.Writer, info *checker.SSLInfo) {
	fmt.Fprintln(w, f.style(color.Bold, "SSL Certificate"))

	if info.Error != "" {
		fmt.Fprintf(w, "  %s %s: %s\n", f.style(color.FgRed, "FAILED"), info.Host, info.Error)
		return
	}

	status, statusAttr := "HEALTHY", color.FgGreen
	switch {
	case info.Expired:
		status, statusAttr = "EXPIRED", color.FgRed
	case info.ExpiringSoon:
		status, statusAttr = "EXPIRING SOON", color.FgYellow
	}

	fmt.Fprintf(w, "  Host:           %s\n", info.Host)
	fmt.Fprintf(w, "  Status:         %s\n", f.style(statusAttr, status))
	fmt.Fprintf(w, "  Issuer:         %s\n", info.Issuer)
	fmt.Fprintf(w, "  Expires:        %s\n", info.NotAfter.Format("2006-01-02"))
	fmt.Fprintf(w, "  Days remaining: %d\n", info.DaysRemaining)
}

// style conditionally applies an ANSI attribute to text, respecting
// f.NoColor as well as fatih/color's own NO_COLOR/TTY auto-detection.
func (f *TableFormatter) style(attr color.Attribute, text string) string {
	if f.NoColor {
		return text
	}
	return color.New(attr).Sprint(text)
}

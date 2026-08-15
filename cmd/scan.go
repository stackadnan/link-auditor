package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
	"github.com/stackadnan/link-auditor/internal/report"
)

// errBrokenLinksFound is a sentinel returned by runScan when the crawl
// completed successfully but discovered one or more broken links. Execute()
// uses it to set a non-zero exit code without printing a spurious
// "Error: ..." line, since finding broken links is the tool working as
// intended, not a failure of the tool itself.
var errBrokenLinksFound = errors.New("broken links found")

// scanOptions holds the parsed flag values for the scan subcommand.
type scanOptions struct {
	concurrency    int
	depth          int
	timeout        int
	output         string
	exportFile     string
	checkSSL       bool
	ignoreExternal bool
	verbose        bool
}

func newScanCmd() *cobra.Command {
	opts := &scanOptions{}

	cmd := &cobra.Command{
		Use:   "scan <target_url>",
		Short: "Crawl a website, checking links and SSL certificate health",
		Long: `Scan recursively crawls a target website starting from <target_url>,
following internal links up to --depth hops away. Every discovered URL
(internal and, unless --ignore-external is set, external) is requested
once and classified as healthy, redirected, or broken. When the target is
served over HTTPS, its TLS certificate expiration is audited as well.`,
		Example: `  link-auditor scan https://example.com
  link-auditor scan example.com --depth 5 --concurrency 40
  link-auditor scan https://example.com -o json --export-file report.json
  link-auditor scan https://example.com --ignore-external -v`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(cmd, args[0], opts)
		},
	}

	flags := cmd.Flags()
	flags.IntVarP(&opts.concurrency, "concurrency", "c", 20, "Number of concurrent worker goroutines")
	flags.IntVarP(&opts.depth, "depth", "d", 3, "Maximum recursive crawl depth")
	flags.IntVarP(&opts.timeout, "timeout", "t", 10, "HTTP request timeout per page, in seconds")
	flags.StringVarP(&opts.output, "output", "o", "table", "Output format: table, json, markdown")
	flags.StringVar(&opts.exportFile, "export-file", "", "Path to save the report to (default: stdout)")
	flags.BoolVar(&opts.checkSSL, "check-ssl", true, "Check the SSL/TLS certificate expiration on the target host")
	flags.BoolVar(&opts.ignoreExternal, "ignore-external", false, "Do not verify the status of external domain links")
	flags.BoolVarP(&opts.verbose, "verbose", "v", false, "Print debug logs during crawling")

	return cmd
}

func runScan(cmd *cobra.Command, target string, opts *scanOptions) error {
	if err := validateOptions(opts); err != nil {
		return err
	}

	targetURL, err := normalizeTarget(target)
	if err != nil {
		return fmt.Errorf("invalid target URL %q: %w", target, err)
	}

	// report.NewFormatter also validates --output up front, before any
	// network activity happens, so a typo in the flag fails fast.
	formatter, err := report.NewFormatter(opts.output, opts.exportFile != "")
	if err != nil {
		return err
	}

	stderr := cmd.ErrOrStderr()
	logger := newCLILogger(opts.verbose, stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchInterrupt(ctx, cancel, stderr)

	cfg := crawler.Config{
		Concurrency:    opts.concurrency,
		MaxDepth:       opts.depth,
		Timeout:        time.Duration(opts.timeout) * time.Second,
		IgnoreExternal: opts.ignoreExternal,
		Logger:         logger,
	}

	logger.Debugf("starting scan of %s (concurrency=%d depth=%d timeout=%ds ignore-external=%t)",
		targetURL, opts.concurrency, opts.depth, opts.timeout, opts.ignoreExternal)

	start := time.Now()
	results, err := crawler.New(cfg).Run(ctx, targetURL)
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Errorf("crawl failed: %w", err)
	}
	logger.Debugf("crawl finished in %s: %d URLs checked", elapsed.Round(time.Millisecond), len(results))

	sslInfo := maybeCheckSSL(opts, targetURL, logger)

	writer, closeWriter, err := resolveWriter(opts.exportFile)
	if err != nil {
		return err
	}
	defer closeWriter()

	summary := report.BuildSummary(results, elapsed)
	if err := formatter.Generate(writer, results, summary, sslInfo); err != nil {
		return fmt.Errorf("failed to generate report: %w", err)
	}

	if opts.exportFile != "" {
		successColor := color.New(color.FgGreen)
		successColor.Fprintf(cmd.OutOrStdout(), "Report written to %s\n", opts.exportFile)
	}

	if summary.Broken > 0 {
		return errBrokenLinksFound
	}
	return nil
}

func validateOptions(opts *scanOptions) error {
	if opts.concurrency < 1 {
		return fmt.Errorf("--concurrency must be at least 1, got %d", opts.concurrency)
	}
	if opts.depth < 0 {
		return fmt.Errorf("--depth must be zero or greater, got %d", opts.depth)
	}
	if opts.timeout < 1 {
		return fmt.Errorf("--timeout must be at least 1 second, got %d", opts.timeout)
	}
	return nil
}

// maybeCheckSSL performs the SSL/TLS audit for targetURL's host when
// --check-ssl is enabled and the target is served over HTTPS. Failures are
// logged in verbose mode and otherwise surfaced through the returned
// SSLInfo's Error field rather than aborting the scan, since a broken SSL
// probe should not prevent the link report from being generated.
func maybeCheckSSL(opts *scanOptions, targetURL string, logger *cliLogger) *checker.SSLInfo {
	if !opts.checkSSL {
		return nil
	}
	host, err := hostForSSL(targetURL)
	if err != nil {
		logger.Debugf("skipping SSL check: %v", err)
		return nil
	}
	logger.Debugf("checking SSL certificate for %s", host)
	info, err := checker.CheckCertificate(host, time.Duration(opts.timeout)*time.Second)
	if err != nil {
		logger.Debugf("SSL check failed: %v", err)
	}
	return info
}

// watchInterrupt cancels ctx and prints a one-time notice the first time
// the process receives SIGINT, allowing in-flight requests to wind down
// gracefully instead of being killed outright.
func watchInterrupt(ctx context.Context, cancel context.CancelFunc, stderr io.Writer) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	go func() {
		defer signal.Stop(sigCh)
		select {
		case <-sigCh:
			fmt.Fprintln(stderr, "\nInterrupt received, finishing in-flight requests...")
			cancel()
		case <-ctx.Done():
		}
	}()
}

// normalizeTarget ensures raw has a scheme, defaulting to https://, and
// validates that it parses into an absolute URL with a host.
func normalizeTarget(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("URL must include a host")
	}
	return u.String(), nil
}

// hostForSSL extracts the host:port to probe for a TLS certificate from
// targetURL, rejecting non-HTTPS targets since they have no certificate to
// audit.
func hostForSSL(targetURL string) (string, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("target scheme is %q, not https", u.Scheme)
	}
	return u.Host, nil
}

// resolveWriter returns the destination for the generated report: the file
// at path if non-empty, otherwise stdout. The returned close function is
// always safe to call and must be deferred by the caller.
func resolveWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create export file %q: %w", path, err)
	}
	return f, func() { f.Close() }, nil
}

// cliLogger is a small adapter implementing crawler.Logger that prints
// timestamped debug lines to w when enabled, and does nothing otherwise.
type cliLogger struct {
	enabled bool
	w       io.Writer
}

func newCLILogger(enabled bool, w io.Writer) *cliLogger {
	return &cliLogger{enabled: enabled, w: w}
}

// Debugf implements crawler.Logger.
func (l *cliLogger) Debugf(format string, args ...interface{}) {
	if !l.enabled {
		return
	}
	prefix := color.New(color.FgCyan).Sprint("[debug]")
	fmt.Fprintf(l.w, "%s %s %s\n", prefix, time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

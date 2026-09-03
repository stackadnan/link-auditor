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

// errBlockingFindings is a sentinel returned by runScan when the crawl
// completed successfully but the configured --fail-on policy considers its
// findings blocking. Execute() uses it to set exit code 1 without printing
// a spurious "Error: ..." line, since finding broken links (or whatever the
// policy watches for) is the tool working as intended, not a failure of the
// tool itself.
var errBlockingFindings = errors.New("blocking findings detected")

// failOnChoices are the recognized values for --fail-on.
var failOnChoices = []string{"broken", "redirect", "ssl", "any"}

// scanOptions holds the parsed flag values for the scan subcommand.
type scanOptions struct {
	concurrency           int
	depth                 int
	timeout               int
	maxPages              int
	maxBodySize           int64
	userAgent             string
	output                string
	exportFile            string
	checkSSL              bool
	sslWarningDays        int
	ignoreExternal        bool
	respectRobots         bool
	allowPrivateAddresses bool
	failOn                []string
	verbose               bool
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
served over HTTPS, its TLS certificate expiration is audited as well.

By default, connections to loopback, private, and other non-public network
addresses are refused (see --allow-private-addresses) to prevent the crawl
-- which follows redirects and links it does not control -- from being used
to reach internal infrastructure.`,
		Example: `  link-auditor scan https://example.com
  link-auditor scan example.com --depth 5 --concurrency 40
  link-auditor scan https://example.com -o json --export-file report.json
  link-auditor scan https://example.com --ignore-external -v
  link-auditor scan https://example.com --max-pages 500 --fail-on any
  link-auditor scan http://localhost:8080 --allow-private-addresses`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return fmt.Errorf("%w: %v", errInvalidUsage, err)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(cmd, args[0], opts)
		},
	}

	flags := cmd.Flags()
	flags.IntVarP(&opts.concurrency, "concurrency", "c", 20, "Number of concurrent worker goroutines")
	flags.IntVarP(&opts.depth, "depth", "d", 3, "Maximum recursive crawl depth")
	flags.IntVarP(&opts.timeout, "timeout", "t", 10, "HTTP request timeout per page, in seconds")
	flags.IntVar(&opts.maxPages, "max-pages", 0, "Maximum number of distinct URLs to crawl (0 = unlimited)")
	flags.Int64Var(&opts.maxBodySize, "max-body-size", 10<<20, "Maximum response body size to read per request, in bytes")
	flags.StringVar(&opts.userAgent, "user-agent", "", "User-Agent header sent with every request (default \"link-auditor/<version>\")")
	flags.StringVarP(&opts.output, "output", "o", "table", "Output format: table, json, markdown")
	flags.StringVar(&opts.exportFile, "export-file", "", "Path to save the report to (default: stdout)")
	flags.BoolVar(&opts.checkSSL, "check-ssl", true, "Check the SSL/TLS certificate expiration on the target host")
	flags.IntVar(&opts.sslWarningDays, "ssl-warning-days", 30, "Days before expiration at which a certificate is flagged as expiring soon")
	flags.BoolVar(&opts.ignoreExternal, "ignore-external", false, "Do not verify the status of external domain links")
	flags.BoolVar(&opts.respectRobots, "respect-robots", false, "Skip internal URLs disallowed by the target's robots.txt")
	flags.BoolVar(&opts.allowPrivateAddresses, "allow-private-addresses", false, "Allow connecting to loopback, private, and other non-public addresses")
	flags.StringSliceVar(&opts.failOn, "fail-on", []string{"broken"}, "Findings that cause a non-zero exit: broken, redirect, ssl, any")
	flags.BoolVarP(&opts.verbose, "verbose", "v", false, "Print debug logs during crawling")

	return cmd
}

func runScan(cmd *cobra.Command, target string, opts *scanOptions) error {
	if err := validateOptions(opts); err != nil {
		return err
	}

	targetURL, err := normalizeTarget(target)
	if err != nil {
		return fmt.Errorf("%w: invalid target URL %q: %v", errInvalidUsage, target, err)
	}

	// report.NewFormatter also validates --output up front, before any
	// network activity happens, so a typo in the flag fails fast.
	formatter, err := report.NewFormatter(opts.output, opts.exportFile != "")
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidUsage, err)
	}

	stderr := cmd.ErrOrStderr()
	logger := newCLILogger(opts.verbose, stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchInterrupt(ctx, cancel, stderr)

	cfg := crawler.Config{
		Concurrency:           opts.concurrency,
		MaxDepth:              opts.depth,
		MaxPages:              opts.maxPages,
		MaxBodyBytes:          opts.maxBodySize,
		Timeout:               time.Duration(opts.timeout) * time.Second,
		UserAgent:             effectiveUserAgent(opts.userAgent),
		IgnoreExternal:        opts.ignoreExternal,
		RespectRobots:         opts.respectRobots,
		AllowPrivateAddresses: opts.allowPrivateAddresses,
		Logger:                logger,
	}

	logger.Debugf("starting scan of %s (concurrency=%d depth=%d timeout=%ds max-pages=%d ignore-external=%t respect-robots=%t allow-private=%t)",
		targetURL, opts.concurrency, opts.depth, opts.timeout, opts.maxPages, opts.ignoreExternal, opts.respectRobots, opts.allowPrivateAddresses)

	c := crawler.New(cfg)
	start := time.Now()
	results, err := c.Run(ctx, targetURL)
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Errorf("crawl failed: %w", err)
	}
	logger.Debugf("crawl finished in %s: %d URLs checked", elapsed.Round(time.Millisecond), len(results))

	sslInfo := maybeCheckSSL(opts, targetURL, logger)

	writer, closeWriter, err := resolveWriter(opts.exportFile, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	defer closeWriter()

	summary := report.BuildSummary(targetURL, results, elapsed, c.PagesLimited())
	if err := formatter.Generate(writer, results, summary, sslInfo); err != nil {
		return fmt.Errorf("failed to generate report: %w", err)
	}

	if opts.exportFile != "" {
		successColor := color.New(color.FgGreen)
		successColor.Fprintf(cmd.OutOrStdout(), "Report written to %s\n", opts.exportFile)
	}

	if shouldFail(opts.failOn, summary, sslInfo) {
		return errBlockingFindings
	}
	return nil
}

func validateOptions(opts *scanOptions) error {
	switch {
	case opts.concurrency < 1:
		return fmt.Errorf("%w: --concurrency must be at least 1, got %d", errInvalidUsage, opts.concurrency)
	case opts.depth < 0:
		return fmt.Errorf("%w: --depth must be zero or greater, got %d", errInvalidUsage, opts.depth)
	case opts.timeout < 1:
		return fmt.Errorf("%w: --timeout must be at least 1 second, got %d", errInvalidUsage, opts.timeout)
	case opts.maxPages < 0:
		return fmt.Errorf("%w: --max-pages must be zero (unlimited) or greater, got %d", errInvalidUsage, opts.maxPages)
	case opts.maxBodySize < 1:
		return fmt.Errorf("%w: --max-body-size must be at least 1 byte, got %d", errInvalidUsage, opts.maxBodySize)
	case opts.sslWarningDays < 0:
		return fmt.Errorf("%w: --ssl-warning-days must be zero or greater, got %d", errInvalidUsage, opts.sslWarningDays)
	case strings.TrimSpace(opts.userAgent) == "" && opts.userAgent != "":
		return fmt.Errorf("%w: --user-agent must not be blank", errInvalidUsage)
	}
	for _, choice := range opts.failOn {
		if !isValidFailOn(choice) {
			return fmt.Errorf("%w: --fail-on %q is not one of %s", errInvalidUsage, choice, strings.Join(failOnChoices, ", "))
		}
	}
	return nil
}

func isValidFailOn(choice string) bool {
	for _, c := range failOnChoices {
		if choice == c {
			return true
		}
	}
	return false
}

// shouldFail applies opts.failOn against a completed scan's findings,
// deciding whether the CLI should exit non-zero. It is intentionally a flat
// set of independent checks rather than a boolean expression language: each
// policy value is evaluated on its own, and any one matching is enough to
// fail the scan.
func shouldFail(failOn []string, summary report.Summary, sslInfo *checker.SSLInfo) bool {
	for _, policy := range failOn {
		switch policy {
		case "any":
			if summary.Broken > 0 || summary.Redirects > 0 || sslIsProblem(sslInfo) {
				return true
			}
		case "broken":
			if summary.Broken > 0 {
				return true
			}
		case "redirect":
			if summary.Redirects > 0 {
				return true
			}
		case "ssl":
			if sslIsProblem(sslInfo) {
				return true
			}
		}
	}
	return false
}

// sslIsProblem reports whether the SSL/TLS audit found something a
// --fail-on ssl (or any) policy should treat as blocking: the certificate
// could not be retrieved, has expired, or is expiring soon.
func sslIsProblem(info *checker.SSLInfo) bool {
	return info != nil && (info.Error != "" || info.Expired || info.ExpiringSoon)
}

// effectiveUserAgent returns explicit if non-empty, otherwise the default
// User-Agent derived from the CLI's own version mechanism (see root.go's
// Version, which ldflags sets at build time), so the value shown by
// `link-auditor version` and the value sent on the wire never drift apart.
func effectiveUserAgent(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return fmt.Sprintf("link-auditor/%s", Version)
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
	sslOpts := checker.Options{
		WarningThreshold:      time.Duration(opts.sslWarningDays) * 24 * time.Hour,
		AllowPrivateAddresses: opts.allowPrivateAddresses,
	}
	info, err := checker.CheckCertificate(host, time.Duration(opts.timeout)*time.Second, sslOpts)
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
// at path if non-empty, otherwise stdout (the command's configured output
// stream, so report output can be captured in tests rather than always
// going to the process's real os.Stdout). The returned close function is
// always safe to call and must be deferred by the caller.
func resolveWriter(path string, stdout io.Writer) (io.Writer, func(), error) {
	if path == "" {
		return stdout, func() {}, nil
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

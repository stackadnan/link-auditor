// Package crawler implements a concurrent, worker-pool-based website
// crawler used to discover and check the status of every link reachable
// from a starting URL.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LinkType classifies a discovered link relative to the host of the scan's
// starting URL.
type LinkType string

const (
	// Internal marks links whose host matches the target site's host.
	Internal LinkType = "internal"
	// External marks links pointing to a different host.
	External LinkType = "external"
)

// maxRedirectBodyBytes bounds how much of a non-parsed response body is
// drained to allow connection reuse, protecting against pathologically
// large responses on links the crawler has no intention of reading fully.
const maxRedirectBodyBytes = 1 << 20 // 1 MiB

// Result captures the outcome of checking a single URL.
type Result struct {
	// URL is the normalized URL that was requested.
	URL string `json:"url"`
	// ParentURL is the page on which this URL was discovered, empty for
	// the scan's root URL.
	ParentURL string `json:"parent_url,omitempty"`
	// Depth is the number of link hops from the root URL.
	Depth int `json:"depth"`
	// LinkType indicates whether URL is on the same host as the root URL.
	LinkType LinkType `json:"link_type"`
	// StatusCode is the HTTP response status code. It is zero if the
	// request never completed (see Error).
	StatusCode int `json:"status_code"`
	// Error describes a network-level failure (DNS, timeout, connection
	// refused, etc.). It is empty when an HTTP response was received,
	// regardless of that response's status code.
	Error string `json:"error,omitempty"`
	// RedirectTo is the resolved Location header target, set only when
	// StatusCode is a 3xx redirect.
	RedirectTo string `json:"redirect_to,omitempty"`
	// ResponseTime is how long the request took to complete.
	ResponseTime time.Duration `json:"response_time_ns"`
	// Skipped is true when the URL was not requested at all, e.g. an
	// external link with --ignore-external set.
	Skipped bool `json:"skipped,omitempty"`
}

// Broken reports whether the result represents a link a human should
// investigate: a network/DNS/timeout failure, or an HTTP 4xx/5xx status.
// Redirects and skipped links are never considered broken.
func (r Result) Broken() bool {
	if r.Skipped {
		return false
	}
	if r.Error != "" {
		return true
	}
	return r.StatusCode >= 400
}

// Job represents a single URL awaiting a status check, discovered at a
// given crawl depth from a parent page.
type Job struct {
	URL       string
	ParentURL string
	Depth     int
}

// Logger is a minimal structured-logging interface used by the crawler to
// emit debug output when verbose mode is enabled. *log.Logger does not
// satisfy this interface directly; callers typically provide a small
// adapter (see cmd.cliLogger).
type Logger interface {
	Debugf(format string, args ...interface{})
}

// NopLogger discards all debug output. It is the default when Config.Logger
// is left nil.
type NopLogger struct{}

// Debugf implements Logger.
func (NopLogger) Debugf(string, ...interface{}) {}

// Config controls the behavior of a Crawler.
type Config struct {
	// Concurrency is the number of worker goroutines processing jobs.
	Concurrency int
	// MaxDepth is the maximum number of link hops from the root URL that
	// the crawler will follow when discovering new internal links. A
	// value of 0 checks only the root URL itself.
	MaxDepth int
	// Timeout bounds how long a single HTTP request (including any
	// connection setup) is allowed to take.
	Timeout time.Duration
	// IgnoreExternal, when true, skips status checks for links that point
	// to a different host than the scan target.
	IgnoreExternal bool
	// Logger receives debug output when verbose logging is enabled. If
	// nil, a no-op logger is used.
	Logger Logger
}

// Crawler performs a concurrent breadth-first crawl of a website, checking
// every discovered link exactly once for reachability.
type Crawler struct {
	cfg    Config
	client *http.Client
	state  *State
	logger Logger

	jobs    chan Job
	results chan Result
	pending sync.WaitGroup // outstanding jobs: enqueued but not yet processed
	workers sync.WaitGroup // live worker goroutines
}

// New constructs a Crawler from the supplied Config, applying sane defaults
// for any zero-valued fields.
func New(cfg Config) *Crawler {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = NopLogger{}
	}

	return &Crawler{
		cfg:    cfg,
		logger: logger,
		state:  NewState(),
		client: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Redirects are handled manually (see handleRedirect) so
				// that 3xx responses are reported as their own status
				// category instead of being silently followed and hidden
				// behind their final destination's status code.
				return http.ErrUseLastResponse
			},
		},
	}
}

// Run crawls the site rooted at rawTargetURL and blocks until the crawl
// completes or ctx is cancelled. It returns one Result per unique URL
// discovered, including the root URL itself.
func (c *Crawler) Run(ctx context.Context, rawTargetURL string) ([]Result, error) {
	rootURL, err := url.Parse(rawTargetURL)
	if err != nil {
		return nil, fmt.Errorf("parsing target URL: %w", err)
	}
	if rootURL.Host == "" {
		return nil, errors.New("target URL must be absolute and include a host")
	}
	rootHost := strings.ToLower(rootURL.Hostname())

	normalizedRoot, err := NormalizeURL(rootURL.String())
	if err != nil {
		return nil, fmt.Errorf("normalizing target URL: %w", err)
	}

	c.jobs = make(chan Job, c.cfg.Concurrency*4)
	c.results = make(chan Result, c.cfg.Concurrency*4)

	for i := 0; i < c.cfg.Concurrency; i++ {
		c.workers.Add(1)
		go c.worker(ctx, rootHost)
	}

	// Close results once every worker has exited, which is what lets the
	// collection loop below terminate.
	go func() {
		c.workers.Wait()
		close(c.results)
	}()

	// The root job must be enqueued (which synchronously bumps c.pending)
	// before the goroutine below calls c.pending.Wait(), otherwise Wait
	// could observe a momentarily-zero counter and close the jobs channel
	// before the first job is ever sent.
	c.state.MarkVisited(normalizedRoot)
	c.enqueue(ctx, Job{URL: normalizedRoot, Depth: 0})

	// Close jobs once every enqueued job (including those discovered
	// during the crawl) has been processed, which is what lets the
	// workers above return.
	go func() {
		c.pending.Wait()
		close(c.jobs)
	}()

	var collected []Result
	for result := range c.results {
		collected = append(collected, result)
	}

	if err := ctx.Err(); err != nil {
		return collected, fmt.Errorf("scan interrupted: %w", err)
	}
	return collected, nil
}

// enqueue schedules job for processing without blocking the caller. It
// synchronously increments the pending counter, then hands off delivery to
// a dedicated goroutine so that a full jobs channel never blocks a worker
// that is trying to discover more work (which would otherwise be able to
// deadlock: every worker stuck sending, none left receiving).
func (c *Crawler) enqueue(ctx context.Context, job Job) {
	c.pending.Add(1)
	go func() {
		select {
		case c.jobs <- job:
		case <-ctx.Done():
			c.pending.Done()
		}
	}()
}

// worker consumes jobs until the jobs channel is closed and drained, or ctx
// is cancelled, checking each URL and discovering further links from
// internal HTML pages.
func (c *Crawler) worker(ctx context.Context, rootHost string) {
	defer c.workers.Done()
	for {
		select {
		case job, ok := <-c.jobs:
			if !ok {
				return
			}
			c.process(ctx, job, rootHost)
		case <-ctx.Done():
			return
		}
	}
}

// process performs the HTTP check for a single job, emits its Result, and
// enqueues any newly discovered internal links for further crawling.
func (c *Crawler) process(ctx context.Context, job Job, rootHost string) {
	defer c.pending.Done()

	result := Result{
		URL:       job.URL,
		ParentURL: job.ParentURL,
		Depth:     job.Depth,
		LinkType:  classify(job.URL, rootHost),
	}

	if result.LinkType == External && c.cfg.IgnoreExternal {
		result.Skipped = true
		c.logger.Debugf("skipping external link (--ignore-external): %s", job.URL)
		c.emit(ctx, result)
		return
	}

	c.logger.Debugf("checking [depth=%d type=%s] %s", job.Depth, result.LinkType, job.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, job.URL, nil)
	if err != nil {
		result.Error = err.Error()
		c.emit(ctx, result)
		return
	}
	req.Header.Set("User-Agent", "link-auditor/1.0 (+https://github.com/stackadnan/link-auditor)")

	start := time.Now()
	resp, err := c.client.Do(req)
	result.ResponseTime = time.Since(start)
	if err != nil {
		result.Error = describeError(err)
		c.logger.Debugf("error checking %s: %s", job.URL, result.Error)
		c.emit(ctx, result)
		return
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode

	switch {
	case isRedirect(resp.StatusCode):
		c.handleRedirect(ctx, job, resp, &result, rootHost)
		drain(resp.Body)
	case result.LinkType == Internal && job.Depth < c.cfg.MaxDepth && isCrawlableHTML(resp):
		c.discoverLinks(ctx, job, resp)
	default:
		// Drain (rather than merely close) the body so the underlying
		// connection can be returned to the client's pool for reuse.
		drain(resp.Body)
	}

	c.emit(ctx, result)
}

// handleRedirect records the resolved redirect target on result and, for
// internal links still within the configured depth budget, enqueues that
// target so the crawl can continue past the redirect rather than stopping
// at it.
func (c *Crawler) handleRedirect(ctx context.Context, job Job, resp *http.Response, result *Result, rootHost string) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}
	target, err := resp.Request.URL.Parse(location)
	if err != nil {
		c.logger.Debugf("could not resolve redirect target %q from %s: %v", location, job.URL, err)
		return
	}
	result.RedirectTo = target.String()

	if result.LinkType != Internal || job.Depth >= c.cfg.MaxDepth {
		return
	}
	normalized, err := NormalizeURL(target.String())
	if err != nil {
		return
	}
	if classify(normalized, rootHost) != Internal {
		return
	}
	if c.state.MarkVisited(normalized) {
		// A redirect does not count as an extra hop: the link the page
		// author wrote was at job.Depth, and the browser follows the
		// redirect transparently.
		c.enqueue(ctx, Job{URL: normalized, ParentURL: job.URL, Depth: job.Depth})
	}
}

// discoverLinks parses an HTML response body for anchor links and enqueues
// any newly discovered internal URLs.
func (c *Crawler) discoverLinks(ctx context.Context, job Job, resp *http.Response) {
	links, err := ExtractLinks(resp.Body, resp.Request.URL)
	if err != nil {
		c.logger.Debugf("error parsing HTML from %s: %v", job.URL, err)
	}
	for _, link := range links {
		normalized, err := NormalizeURL(link)
		if err != nil {
			continue
		}
		if c.state.MarkVisited(normalized) {
			c.enqueue(ctx, Job{URL: normalized, ParentURL: job.URL, Depth: job.Depth + 1})
		}
	}
}

// emit delivers a result to the results channel, respecting cancellation so
// that a shutting-down crawl can never block forever trying to send.
func (c *Crawler) emit(ctx context.Context, result Result) {
	select {
	case c.results <- result:
	case <-ctx.Done():
	}
}

// classify determines whether targetURL belongs to the same host as
// rootHost. Hostname comparison intentionally ignores port and scheme, so
// http://example.com and https://example.com:8443 are both internal to a
// scan rooted at example.com.
func classify(targetURL, rootHost string) LinkType {
	u, err := url.Parse(targetURL)
	if err != nil {
		return External
	}
	if strings.EqualFold(u.Hostname(), rootHost) {
		return Internal
	}
	return External
}

// isRedirect reports whether an HTTP status code is a 3xx redirect.
func isRedirect(status int) bool {
	return status >= 300 && status < 400
}

// isCrawlableHTML reports whether resp represents a successful response
// whose Content-Type indicates an HTML document worth parsing for further
// links.
func isCrawlableHTML(resp *http.Response) bool {
	if resp.StatusCode >= 400 {
		return false
	}
	contentType := resp.Header.Get("Content-Type")
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// drain discards up to maxRedirectBodyBytes of body, allowing the
// connection to be reused by the HTTP client's transport without paying
// the cost of buffering an unbounded response.
func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxRedirectBodyBytes))
}

// describeError normalizes low-level network errors (timeouts, DNS
// failures, connection refusals) into concise, human-readable messages
// suitable for display in a report.
func describeError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("DNS lookup failed: %s", dnsErr.Err)
	}
	return err.Error()
}

// NormalizeURL returns a canonical form of raw suitable for deduplication:
// the fragment is stripped, scheme and host are lower-cased, default ports
// (80 for http, 443 for https) are removed, and a trailing slash on
// non-root paths is dropped. This ensures that links such as
// "https://Example.com/Page/", "https://example.com:443/Page", and
// "https://example.com/Page#section" are all treated as the same URL.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Host[:strings.LastIndex(u.Host, ":")]
	}

	switch {
	case u.Path == "":
		u.Path = "/"
	case len(u.Path) > 1 && strings.HasSuffix(u.Path, "/"):
		u.Path = strings.TrimRight(u.Path, "/")
	}

	return u.String(), nil
}

package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips fragment",
			in:   "https://example.com/page#section",
			want: "https://example.com/page",
		},
		{
			name: "strips trailing slash on non-root path",
			in:   "https://example.com/page/",
			want: "https://example.com/page",
		},
		{
			name: "root path trailing slash is preserved",
			in:   "https://example.com/",
			want: "https://example.com/",
		},
		{
			name: "missing path becomes root",
			in:   "https://example.com",
			want: "https://example.com/",
		},
		{
			name: "lower-cases scheme and host",
			in:   "HTTPS://Example.COM/Page",
			want: "https://example.com/Page",
		},
		{
			name: "strips default https port",
			in:   "https://example.com:443/page",
			want: "https://example.com/page",
		},
		{
			name: "strips default http port",
			in:   "http://example.com:80/page",
			want: "http://example.com/page",
		},
		{
			name: "keeps non-default port",
			in:   "https://example.com:8443/page",
			want: "https://example.com:8443/page",
		},
		{
			name: "combines fragment and trailing slash stripping",
			in:   "https://example.com/page/#top",
			want: "https://example.com/page",
		},
		{
			name: "preserves query string",
			in:   "https://example.com/search?q=go",
			want: "https://example.com/search?q=go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeURL(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeURL_Invalid verifies that a malformed URL produces an error
// rather than a best-effort guess, since a silently-wrong normalization
// would defeat deduplication.
func TestNormalizeURL_Invalid(t *testing.T) {
	_, err := NormalizeURL("http://[::1")
	if err == nil {
		t.Fatal("NormalizeURL(\"http://[::1\") error = nil, want an error for the unterminated IPv6 host")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		rootHost  string
		want      LinkType
	}{
		{"same host", "https://example.com/page", "example.com", Internal},
		{"same host different case", "https://Example.COM/page", "example.com", Internal},
		{"same host different scheme", "http://example.com/page", "example.com", Internal},
		{"same host different port", "https://example.com:8443/page", "example.com", Internal},
		{"different host", "https://other.com/page", "example.com", External},
		{"subdomain is external", "https://blog.example.com/page", "example.com", External},
		{"unparseable URL", "://not a url", "example.com", External},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.targetURL, tt.rootHost); got != tt.want {
				t.Errorf("classify(%q, %q) = %v, want %v", tt.targetURL, tt.rootHost, got, tt.want)
			}
		})
	}
}

func TestResult_Broken(t *testing.T) {
	tests := []struct {
		name string
		r    Result
		want bool
	}{
		{"200 is not broken", Result{StatusCode: 200}, false},
		{"301 is not broken", Result{StatusCode: 301}, false},
		{"404 is broken", Result{StatusCode: 404}, true},
		{"500 is broken", Result{StatusCode: 500}, true},
		{"network error is broken", Result{Error: "connection refused"}, true},
		{"skipped is never broken, even with a bad status", Result{StatusCode: 500, Skipped: true}, false},
		{"skipped is never broken, even with an error", Result{Error: "boom", Skipped: true}, false},

		// The following lock in deliberate classification decisions for
		// status codes with non-obvious semantics (see Phase 17 of the
		// v0.2.0 plan and the README's "HTTP status classification"
		// section): they are regression tests for a decision, not just
		// coverage.
		{"204 No Content is not broken", Result{StatusCode: 204}, false},
		{"206 Partial Content is not broken", Result{StatusCode: 206}, false},
		{"304 Not Modified is not broken (a 3xx, not an error)", Result{StatusCode: 304}, false},
		{"401 Unauthorized is broken (an access-controlled link is still worth flagging)", Result{StatusCode: 401}, true},
		{"403 Forbidden is broken (an access-controlled link is still worth flagging)", Result{StatusCode: 403}, true},
		{"429 Too Many Requests is broken (rate limiting is surfaced, not silently ignored)", Result{StatusCode: 429}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Broken(); got != tt.want {
				t.Errorf("Broken() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCrawler_Run_FullSite spins up a small in-process HTTP server modeling
// a realistic site (a homepage linking to a healthy page, a broken page, a
// redirect, and an external link) and verifies that a full Run() produces
// exactly the expected results: every internal URL visited once, external
// links checked but not followed, and depth-limited pages excluded.
func TestCrawler_Run_FullSite(t *testing.T) {
	// external.invalid is reserved by RFC 2606 and is guaranteed to never
	// resolve, giving us a genuinely different host (unlike a second
	// httptest.Server, which would just bind to 127.0.0.1 on a different
	// port and so still classify as Internal) without any flaky
	// dependency on real internet connectivity.
	const externalURL = "http://external.invalid/"

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
			<a href="/healthy">healthy</a>
			<a href="/broken">broken</a>
			<a href="/redirect">redirect</a>
			<a href="%s">external</a>
			<a href="/healthy#dup">duplicate of healthy via fragment</a>
		</body></html>`, externalURL)
	})
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/deep">deep page</a></body></html>`)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/healthy", http.StatusFound)
	})
	mux.HandleFunc("/deep", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/too-deep">too deep</a></body></html>`)
	})
	mux.HandleFunc("/too-deep", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	site := httptest.NewServer(mux)
	defer site.Close()

	cfg := Config{
		Concurrency:           4,
		MaxDepth:              2, // root(0) -> healthy/broken/redirect(1) -> deep(2); too-deep(3) excluded
		Timeout:               5 * time.Second,
		AllowPrivateAddresses: true, // httptest.Server binds to 127.0.0.1
	}
	c := New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	byPath := make(map[string]Result)
	for _, r := range results {
		byPath[r.URL] = r
	}

	// Every internal URL should appear exactly once, proving deduplication
	// works even though "/healthy" is linked twice (once directly, once
	// via a fragment that normalizes to the same URL).
	wantPaths := []string{
		site.URL + "/",
		site.URL + "/healthy",
		site.URL + "/broken",
		site.URL + "/redirect",
		site.URL + "/deep",
		externalURL,
	}
	for _, path := range wantPaths {
		if _, ok := byPath[path]; !ok {
			t.Errorf("missing expected result for %s; got results: %+v", path, keys(byPath))
		}
	}

	if _, ok := byPath[site.URL+"/too-deep"]; ok {
		t.Errorf("/too-deep exceeds MaxDepth and should not have been crawled")
	}

	if got := len(results); got != len(wantPaths) {
		t.Errorf("got %d results, want %d (deduplication or depth limiting failed): %+v", got, len(wantPaths), keys(byPath))
	}

	if r := byPath[site.URL+"/broken"]; r.StatusCode != http.StatusNotFound || !r.Broken() {
		t.Errorf("/broken result = %+v, want StatusCode=404 and Broken()=true", r)
	}
	if r := byPath[site.URL+"/redirect"]; r.StatusCode != http.StatusFound || r.RedirectTo == "" {
		t.Errorf("/redirect result = %+v, want StatusCode=302 with RedirectTo set", r)
	}
	if r := byPath[site.URL+"/healthy"]; r.StatusCode != http.StatusOK || r.LinkType != Internal {
		t.Errorf("/healthy result = %+v, want StatusCode=200 and Internal", r)
	}
	r := byPath[externalURL]
	if r.LinkType != External {
		t.Errorf("external result = %+v, want LinkType=External", r)
	}
	if r.Error == "" {
		t.Errorf("external result = %+v, want a DNS resolution error since external.invalid never resolves", r)
	}
	if !r.Broken() {
		t.Errorf("external result = %+v, want Broken()=true", r)
	}
}

// TestCrawler_Run_IgnoreExternal verifies that external links are marked
// Skipped and never actually requested when IgnoreExternal is set.
func TestCrawler_Run_IgnoreExternal(t *testing.T) {
	// See TestCrawler_Run_FullSite for why external.invalid (RFC 2606) is
	// used instead of a second httptest.Server: it guarantees a distinct
	// host without depending on real network connectivity. Here that also
	// doubles as proof that the link was never actually requested: a real
	// request to a non-resolving host would set Result.Error, so an empty
	// Error confirms process() took the Skipped branch and returned
	// before ever dialing out.
	const externalURL = "http://external.invalid/"

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<a href="%s">external</a>`, externalURL)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	c := New(Config{Concurrency: 2, MaxDepth: 1, Timeout: 5 * time.Second, IgnoreExternal: true, AllowPrivateAddresses: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var externalResult *Result
	for i := range results {
		if results[i].LinkType == External {
			externalResult = &results[i]
		}
	}
	if externalResult == nil {
		t.Fatal("expected an external result to be present (skipped, not omitted)")
	}
	if !externalResult.Skipped {
		t.Errorf("external result Skipped = false, want true")
	}
	if externalResult.Error != "" {
		t.Errorf("external result Error = %q, want empty (the link should never have been requested)", externalResult.Error)
	}
}

// TestCrawler_Run_ContextCancellation verifies that cancelling the context
// causes Run to return promptly instead of hanging, even against a server
// that never responds.
func TestCrawler_Run_ContextCancellation(t *testing.T) {
	block := make(chan struct{})

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never respond until the test cleans up
	}))
	// Deferred in this order so close(block) runs BEFORE site.Close():
	// defers unwind LIFO, and Close() blocks until outstanding handlers
	// return, which this one only does once block is closed. Deferring
	// site.Close() first (so it unwinds last) avoids a deadlock at test
	// cleanup.
	defer site.Close()
	defer close(block)

	c := New(Config{Concurrency: 2, MaxDepth: 1, Timeout: time.Minute, AllowPrivateAddresses: true})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := c.Run(ctx, site.URL); err == nil {
			t.Error("Run() error = nil, want a context cancellation error")
		}
	}()

	time.Sleep(50 * time.Millisecond) // let the request start
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of context cancellation")
	}
}

// TestCrawler_Run_MaxPages verifies that MaxPages bounds the number of
// distinct URLs admitted into the crawl, that the boundary is exact (not
// "roughly" enforced), and that PagesLimited reports the truncation. The
// site is a strict linear chain (root -> a -> b -> c) and Concurrency is 1
// so discovery order is deterministic: budget exhaustion always lands on
// the same URL.
func TestCrawler_Run_MaxPages(t *testing.T) {
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
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/c">c</a>`)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	c := New(Config{
		Concurrency:           1,
		MaxDepth:              10,
		MaxPages:              3,
		Timeout:               5 * time.Second,
		AllowPrivateAddresses: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := len(results); got != 3 {
		t.Errorf("got %d results, want exactly 3 (the MaxPages budget): %+v", got, keys(toByPath(results)))
	}
	if !c.PagesLimited() {
		t.Error("PagesLimited() = false, want true: the chain has more than 3 pages")
	}
	if _, ok := toByPath(results)[site.URL+"/c"]; ok {
		t.Error("/c should have been dropped once the 3-page budget was exhausted")
	}
}

// TestCrawler_Run_MaxBodySize verifies that the crawler stops reading a
// response body once Config.MaxBodyBytes is reached: a link placed after
// the cutoff is not discovered, while one placed before it is, and the
// crawl completes without buffering the full (much larger) body.
func TestCrawler_Run_MaxBodySize(t *testing.T) {
	const limit = 200

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/within-limit">early</a>`)
		fmt.Fprint(w, strings.Repeat(" ", 10*limit)) // pad well past the cutoff
		fmt.Fprint(w, `<a href="/beyond-limit">late</a>`)
	})
	mux.HandleFunc("/within-limit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/beyond-limit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	c := New(Config{
		Concurrency:           2,
		MaxDepth:              1,
		MaxBodyBytes:          limit,
		Timeout:               5 * time.Second,
		AllowPrivateAddresses: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	byPath := toByPath(results)
	if _, ok := byPath[site.URL+"/within-limit"]; !ok {
		t.Error("link before the body-size cutoff should have been discovered")
	}
	if _, ok := byPath[site.URL+"/beyond-limit"]; ok {
		t.Error("link after the body-size cutoff should not have been discovered")
	}
}

// TestCrawler_Run_UserAgent verifies that every request carries
// Config.UserAgent, and that leaving it empty falls back to
// defaultUserAgent rather than sending an empty header.
func TestCrawler_Run_UserAgent(t *testing.T) {
	tests := []struct {
		name      string
		configure string
		want      string
	}{
		{"explicit user agent is sent", "custom-agent/9.9", "custom-agent/9.9"},
		{"empty falls back to the package default", "", defaultUserAgent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("User-Agent")
			}))
			defer site.Close()

			c := New(Config{
				Concurrency:           1,
				Timeout:               5 * time.Second,
				UserAgent:             tt.configure,
				AllowPrivateAddresses: true,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if _, err := c.Run(ctx, site.URL); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("User-Agent header = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCrawler_Run_RespectRobots verifies that a URL disallowed by
// robots.txt is reported as Skipped with SkipReason "robots.txt" and, more
// importantly, is never actually requested -- the same proof-by-absent-
// error technique TestCrawler_Run_IgnoreExternal uses.
func TestCrawler_Run_RespectRobots(t *testing.T) {
	var privateWasHit bool

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/private/page">private</a><a href="/public">public</a>`)
	})
	mux.HandleFunc("/private/page", func(w http.ResponseWriter, r *http.Request) {
		privateWasHit = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/public", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	c := New(Config{
		Concurrency:           2,
		MaxDepth:              1,
		Timeout:               5 * time.Second,
		RespectRobots:         true,
		AllowPrivateAddresses: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	byPath := toByPath(results)
	private, ok := byPath[site.URL+"/private/page"]
	if !ok {
		t.Fatal("expected a (skipped) result for the disallowed URL, not omission")
	}
	if !private.Skipped || private.SkipReason != "robots.txt" {
		t.Errorf("private page result = %+v, want Skipped=true SkipReason=\"robots.txt\"", private)
	}
	if privateWasHit {
		t.Error("the disallowed URL should never have actually been requested")
	}
	if public, ok := byPath[site.URL+"/public"]; !ok || public.Skipped {
		t.Errorf("public page result = %+v, want present and not skipped", public)
	}
	if _, ok := byPath[site.URL+"/robots.txt"]; ok {
		t.Error("robots.txt itself should not appear as a crawled Result")
	}
}

// TestCrawler_Run_BlocksPrivateAddressByDefault is the crawler-level
// counterpart to internal/netguard's unit tests: it verifies the guard is
// actually wired into the HTTP client New() builds, not just correct in
// isolation. AllowPrivateAddresses defaults to false, so a target on
// loopback must be refused rather than silently connected to.
func TestCrawler_Run_BlocksPrivateAddressByDefault(t *testing.T) {
	c := New(Config{Concurrency: 1, Timeout: 2 * time.Second}) // AllowPrivateAddresses left false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := c.Run(ctx, "http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("Run() error = %v, want a nil error with the refusal captured in the result", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (the root URL, refused)", len(results))
	}
	if results[0].Error == "" || !results[0].Broken() {
		t.Errorf("root result = %+v, want a populated Error and Broken()=true", results[0])
	}
}

// TestCrawler_Run_RedirectChain documents and locks in the crawler's
// redirect-chain design: each hop is reported as its own Result (with its
// own status code and RedirectTo), rather than being collapsed into a
// single synthetic entry for the chain.
func TestCrawler_Run_RedirectChain(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/step2", http.StatusMovedPermanently) // 301
	})
	mux.HandleFunc("/step2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound) // 302
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	c := New(Config{
		Concurrency:           2,
		MaxDepth:              3,
		Timeout:               5 * time.Second,
		AllowPrivateAddresses: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := c.Run(ctx, site.URL)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	byPath := toByPath(results)
	root, ok := byPath[site.URL+"/"]
	if !ok || root.StatusCode != http.StatusMovedPermanently || root.RedirectTo != site.URL+"/step2" {
		t.Errorf("root result = %+v, want 301 redirecting to %s/step2", root, site.URL)
	}
	step2, ok := byPath[site.URL+"/step2"]
	if !ok || step2.StatusCode != http.StatusFound || step2.RedirectTo != site.URL+"/final" {
		t.Errorf("/step2 result = %+v, want 302 redirecting to %s/final", step2, site.URL)
	}
	final, ok := byPath[site.URL+"/final"]
	if !ok || final.StatusCode != http.StatusOK {
		t.Errorf("/final result = %+v, want 200", final)
	}
	if len(results) != 3 {
		t.Errorf("got %d results, want exactly 3: one per hop in the chain", len(results))
	}
}

func toByPath(results []Result) map[string]Result {
	byPath := make(map[string]Result, len(results))
	for _, r := range results {
		byPath[r.URL] = r
	}
	return byPath
}

func keys(m map[string]Result) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

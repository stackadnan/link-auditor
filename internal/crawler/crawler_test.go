package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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
		Concurrency: 4,
		MaxDepth:    2, // root(0) -> healthy/broken/redirect(1) -> deep(2); too-deep(3) excluded
		Timeout:     5 * time.Second,
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

	c := New(Config{Concurrency: 2, MaxDepth: 1, Timeout: 5 * time.Second, IgnoreExternal: true})
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

	c := New(Config{Concurrency: 2, MaxDepth: 1, Timeout: time.Minute})
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

func keys(m map[string]Result) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

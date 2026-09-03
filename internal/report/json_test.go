package report

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

func TestJSONFormatter_Generate(t *testing.T) {
	results := []crawler.Result{
		{URL: "https://example.com/b", StatusCode: 200, LinkType: crawler.Internal},
		{URL: "https://example.com/a", StatusCode: 404, LinkType: crawler.Internal},
	}
	summary := BuildSummary("https://example.com/", results, time.Second, false)
	sslInfo := &checker.SSLInfo{Host: "example.com", DaysRemaining: 42}

	var buf bytes.Buffer
	f := &JSONFormatter{}
	if err := f.Generate(&buf, results, summary, sslInfo); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var decoded struct {
		GeneratedAt time.Time        `json:"generated_at"`
		Summary     Summary          `json:"summary"`
		SSL         *checker.SSLInfo `json:"ssl"`
		Results     []crawler.Result `json:"results"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	if decoded.GeneratedAt.IsZero() {
		t.Error("generated_at was not populated")
	}
	if decoded.Summary.TotalChecked != 2 {
		t.Errorf("summary.total_checked = %d, want 2", decoded.Summary.TotalChecked)
	}
	if decoded.SSL == nil || decoded.SSL.Host != "example.com" {
		t.Errorf("ssl = %+v, want Host=example.com", decoded.SSL)
	}
	if len(decoded.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(decoded.Results))
	}
	// Results must be sorted (broken first, then alphabetically), same as
	// every other formatter, so JSON consumers see a stable ordering.
	if decoded.Results[0].URL != "https://example.com/a" || decoded.Results[0].StatusCode != 404 {
		t.Errorf("results[0] = %+v, want the broken /a link first", decoded.Results[0])
	}
}

// TestJSONFormatter_OmitsNilSSL verifies that the "ssl" key is entirely
// absent (not null) when no SSL check was performed, keeping the schema
// tidy for consumers that treat key-presence as meaningful.
func TestJSONFormatter_OmitsNilSSL(t *testing.T) {
	var buf bytes.Buffer
	f := &JSONFormatter{}
	if err := f.Generate(&buf, nil, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, present := raw["ssl"]; present {
		t.Error(`"ssl" key is present, want it omitted when sslInfo is nil`)
	}
}

// TestJSONFormatter_EmptyResults verifies that an empty scan still produces
// valid, well-formed JSON with a non-null (if empty) results array.
func TestJSONFormatter_EmptyResults(t *testing.T) {
	var buf bytes.Buffer
	f := &JSONFormatter{}
	if err := f.Generate(&buf, []crawler.Result{}, Summary{}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var decoded struct {
		Results []crawler.Result `json:"results"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if decoded.Results == nil {
		t.Error("results was decoded as null, want an empty array")
	}
}

// TestJSONFormatter_StableFieldNames locks in the top-level and summary
// field names as a stability contract: a CI pipeline parsing this JSON
// should never see a silently renamed key.
func TestJSONFormatter_StableFieldNames(t *testing.T) {
	var buf bytes.Buffer
	f := &JSONFormatter{}
	summary := BuildSummary("https://example.com/", []crawler.Result{{StatusCode: 200}}, time.Second, false)
	if err := f.Generate(&buf, []crawler.Result{{URL: "https://example.com/", StatusCode: 200}}, summary, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"generated_at", "summary", "results"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("top-level key %q is missing", key)
		}
	}

	summaryRaw, ok := raw["summary"].(map[string]interface{})
	if !ok {
		t.Fatal("summary is not an object")
	}
	for _, key := range []string{
		"target", "total_checked", "pages_crawled", "ok", "redirects",
		"broken", "skipped", "internal", "external", "duration_ns",
	} {
		if _, ok := summaryRaw[key]; !ok {
			t.Errorf("summary key %q is missing", key)
		}
	}
}

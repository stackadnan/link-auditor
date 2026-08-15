package report

import (
	"encoding/json"
	"io"
	"time"

	"github.com/stackadnan/link-auditor/internal/checker"
	"github.com/stackadnan/link-auditor/internal/crawler"
)

// JSONFormatter renders a scan as a single structured JSON document, suited
// for consumption by CI/CD pipelines and other tooling.
type JSONFormatter struct{}

// jsonReport is the top-level shape written by JSONFormatter. It exists
// separately from crawler.Result so the JSON schema (generated_at,
// summary, ssl, results) is explicit and stable regardless of how the
// internal types evolve.
type jsonReport struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Summary     Summary          `json:"summary"`
	SSL         *checker.SSLInfo `json:"ssl,omitempty"`
	Results     []crawler.Result `json:"results"`
}

// Generate implements Formatter.
func (f *JSONFormatter) Generate(w io.Writer, results []crawler.Result, summary Summary, sslInfo *checker.SSLInfo) error {
	report := jsonReport{
		GeneratedAt: time.Now().UTC(),
		Summary:     summary,
		SSL:         sslInfo,
		Results:     SortResults(results),
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

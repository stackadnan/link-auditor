// Package cmd wires up link-auditor's command-line interface using Cobra.
// It is responsible for flag parsing and process-level concerns (exit
// codes, signal handling); the actual crawling, SSL inspection, and report
// generation logic lives in the internal packages.
package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Build metadata. These are overwritten at build time via -ldflags, e.g.:
//
//	go build -ldflags "-X github.com/stackadnan/link-auditor/cmd.Version=v1.2.3"
//
// See .github/workflows/release.yml for the release build invocation.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "link-auditor",
	Short: "A high-performance concurrent link auditor and SSL/TLS checker",
	Long: `link-auditor is a fast, concurrent command-line tool that recursively
crawls a target website, detects broken links (4xx, 5xx, and network
errors), audits SSL/TLS certificate expiration, and produces reports in
table, JSON, or Markdown format.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. It is invoked once by main() and is the
// sole point where the process exit code is decided.
func Execute() {
	err := rootCmd.Execute()
	switch {
	case err == nil:
		return
	case errors.Is(err, errBrokenLinksFound):
		// Not a tool failure: the scan completed and simply found broken
		// links. The report itself already communicated the details, so a
		// bare non-zero exit is enough for CI/CD pipelines to detect it.
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(newScanCmd())
	rootCmd.AddCommand(newVersionCmd())
}

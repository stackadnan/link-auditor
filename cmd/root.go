// Package cmd wires up link-auditor's command-line interface using Cobra.
// It is responsible for flag parsing and process-level concerns (exit
// codes, signal handling); the actual crawling, SSL inspection, and report
// generation logic lives in the internal packages.
package cmd

import (
	"context"
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

// errInvalidUsage marks an error as CLI misuse (bad flag value, malformed
// argument, unknown flag) rather than a failure that occurred while
// actually performing a scan. exitCodeFor uses it to choose exit code 2.
var errInvalidUsage = errors.New("invalid usage")

var rootCmd = newRootCmd()

// newRootCmd builds a fresh root command with its subcommands attached. It
// exists separately from the rootCmd package variable so tests can
// construct an isolated command tree per test case instead of sharing the
// single global instance, which cobra/pflag do not reliably reset between
// repeated Execute() calls.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link-auditor",
		Short: "A high-performance concurrent link auditor and SSL/TLS checker",
		Long: `link-auditor is a fast, concurrent command-line tool that recursively
crawls a target website, detects broken links (4xx, 5xx, and network
errors), audits SSL/TLS certificate expiration, and produces reports in
table, JSON, or Markdown format.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newScanCmd())
	cmd.AddCommand(newVersionCmd())
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", errInvalidUsage, err)
	})
	return cmd
}

// Execute runs the root command. It is invoked once by main() and is the
// sole point where the process exit code is decided.
//
// Exit codes:
//
//	0   scan completed with nothing the --fail-on policy considers blocking
//	1   scan completed but blocking findings were detected, or another
//	    runtime error occurred
//	2   invalid CLI usage or configuration
//	130 interrupted (SIGINT), matching the POSIX 128+SIGINT convention
func Execute() {
	err := rootCmd.Execute()
	code, message := exitCodeFor(err)
	if message != "" {
		fmt.Fprintln(os.Stderr, "Error:", message)
	}
	os.Exit(code)
}

// exitCodeFor maps the error returned by rootCmd.Execute() to a process
// exit code and the message (if any) that should be printed alongside it.
// It is kept separate from Execute so the exit-code decision can be unit
// tested without going through os.Exit.
func exitCodeFor(err error) (code int, message string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, context.Canceled):
		// Not a tool failure: watchInterrupt (see scan.go) already printed
		// a notice when the SIGINT arrived, so nothing more needs saying.
		return 130, ""
	case errors.Is(err, errBlockingFindings):
		// Not a tool failure either: the scan completed and its findings
		// simply matched the configured --fail-on policy. The report
		// itself already communicated the details, so a bare non-zero
		// exit is enough for CI/CD pipelines to detect it.
		return 1, ""
	case errors.Is(err, errInvalidUsage):
		return 2, err.Error()
	default:
		return 1, err.Error()
	}
}

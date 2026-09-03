package cmd

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantCode    int
		wantMessage bool
	}{
		{"nil error is a clean exit", nil, 0, false},
		{
			"context cancellation is an interrupt, silently",
			fmt.Errorf("crawl failed: %w", fmt.Errorf("scan interrupted: %w", context.Canceled)),
			130, false,
		},
		{"blocking findings exit 1 silently", errBlockingFindings, 1, false},
		{"wrapped blocking findings still resolve to exit 1", fmt.Errorf("wrap: %w", errBlockingFindings), 1, false},
		{"invalid usage exits 2 with a message", fmt.Errorf("%w: bad flag", errInvalidUsage), 2, true},
		{"an unrelated error exits 1 with a message", errors.New("crawl failed: boom"), 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, message := exitCodeFor(tt.err)
			if code != tt.wantCode {
				t.Errorf("exitCodeFor(%v) code = %d, want %d", tt.err, code, tt.wantCode)
			}
			if (message != "") != tt.wantMessage {
				t.Errorf("exitCodeFor(%v) message = %q, want non-empty = %v", tt.err, message, tt.wantMessage)
			}
		})
	}
}

// TestNewRootCmd_InvalidArgCount verifies that calling scan with the wrong
// number of arguments is classified as invalid usage (exit code 2), not a
// generic runtime failure.
func TestNewRootCmd_InvalidArgCount(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"scan"})
	cmd.SetOut(new(discardWriter))
	cmd.SetErr(new(discardWriter))

	err := cmd.Execute()
	if !errors.Is(err, errInvalidUsage) {
		t.Fatalf("Execute() error = %v, want it to wrap errInvalidUsage", err)
	}
	if code, _ := exitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// TestNewRootCmd_UnknownFlag verifies that an unrecognized flag is also
// classified as invalid usage via the FlagErrorFunc wiring in newRootCmd.
func TestNewRootCmd_UnknownFlag(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"scan", "https://example.com", "--not-a-real-flag"})
	cmd.SetOut(new(discardWriter))
	cmd.SetErr(new(discardWriter))

	err := cmd.Execute()
	if !errors.Is(err, errInvalidUsage) {
		t.Fatalf("Execute() error = %v, want it to wrap errInvalidUsage", err)
	}
}

// TestNewRootCmd_InvalidFlagValue verifies that a semantically invalid flag
// value (caught by validateOptions, not by pflag's own type parsing) is
// also classified as invalid usage.
func TestNewRootCmd_InvalidFlagValue(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"scan", "https://example.com", "--concurrency", "0"})
	cmd.SetOut(new(discardWriter))
	cmd.SetErr(new(discardWriter))

	err := cmd.Execute()
	if !errors.Is(err, errInvalidUsage) {
		t.Fatalf("Execute() error = %v, want it to wrap errInvalidUsage", err)
	}
	if code, _ := exitCodeFor(err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// discardWriter is a minimal io.Writer that throws away everything written
// to it, used to keep test output quiet without importing io/ioutil or os.
type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }

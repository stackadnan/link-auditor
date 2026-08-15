package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit hash, and build date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "link-auditor %s\n  commit:     %s\n  built:      %s\n  go version: %s\n  platform:   %s/%s\n",
				Version, Commit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

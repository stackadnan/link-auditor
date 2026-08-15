// Command link-auditor is a high-performance, concurrent CLI for crawling
// websites, detecting broken links, and auditing TLS certificate health.
//
// See the scan subcommand ("link-auditor scan <url>") for usage.
package main

import "github.com/stackadnan/link-auditor/cmd"

func main() {
	cmd.Execute()
}

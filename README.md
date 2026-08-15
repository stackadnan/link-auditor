# link-auditor

[![Go Reference](https://pkg.go.dev/badge/github.com/stackadnan/link-auditor.svg)](https://pkg.go.dev/github.com/stackadnan/link-auditor)
[![Go Report Card](https://goreportcard.com/badge/github.com/stackadnan/link-auditor)](https://goreportcard.com/report/github.com/stackadnan/link-auditor)
[![CI](https://github.com/stackadnan/link-auditor/actions/workflows/release.yml/badge.svg)](https://github.com/stackadnan/link-auditor/actions/workflows/release.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](go.mod)

**link-auditor** is a fast, concurrent command-line tool that recursively crawls a website, finds broken links (4xx, 5xx, and network errors), audits SSL/TLS certificate expiration, and produces reports in table, JSON, or Markdown format — built for both interactive use and CI/CD pipelines.

```
$ link-auditor scan https://example.com

Link Audit Report

┌────────┬───────────────────────────────────┬──────────┬───────┬───────────────────────────────────────────┐
│ STATUS │                 URL                 │   TYPE   │ TIME  │                   NOTES                    │
├────────┼───────────────────────────────────┼──────────┼───────┼───────────────────────────────────────────┤
│ 200    │ https://example.com/                │ internal │ 152ms │                                             │
│ 301    │ https://iana.org/domains/example    │ external │ 814ms │ -> https://www.iana.org/domains/example    │
└────────┴───────────────────────────────────┴──────────┴───────┴───────────────────────────────────────────┘

Summary
  Checked:    2  (1 internal / 1 external)
  OK:         1
  Redirects:  1
  Broken:     0
  Duration:   966ms

SSL Certificate
  Host:           example.com
  Status:         HEALTHY
  Issuer:         Cloudflare TLS Issuing ECC CA 3
  Expires:        2026-10-27
  Days remaining: 72
```

*(Status codes are colored — green for 2xx, yellow for 3xx, red for 4xx/5xx/errors — in an actual terminal. Captured from a real run against example.com; your own scan's timings and certificate details will differ.)*

## Features

- **Concurrent worker-pool crawler** — a configurable number of goroutines pull from a shared job queue, so large sites are audited in a fraction of the time a sequential crawler would take.
- **Thread-safe deduplication** — every URL is normalized (lower-cased host, fragment stripped, default ports and trailing slashes removed) before being checked, so a page linked from fifty other pages is only ever requested once.
- **Full link classification** — every checked link is reported as OK (2xx), a redirect (3xx, with its resolved destination), broken (4xx/5xx or a network/DNS/timeout error), or skipped (external, with `--ignore-external`).
- **Redirects are followed intelligently** — a 3xx response is recorded as its own status (not silently hidden behind its destination's status code), and the crawler still continues past internal redirects to keep auditing the rest of the site.
- **SSL/TLS certificate auditing** — connects to the target host, inspects the presented certificate, and flags certificates that are already expired or expiring within 30 days.
- **Three report formats** — a colored terminal table for humans, structured JSON for CI/CD pipelines and tooling, and GitHub-flavored Markdown for pull request comments or committed audit logs.
- **CI/CD-friendly exit codes** — exits `1` when broken links are found (or the scan itself fails), and `0` when the site is clean, so `link-auditor` can be dropped straight into a build pipeline as a gate.
- **Graceful shutdown** — `Ctrl+C` lets in-flight requests finish instead of killing the process outright.

## Installation

### go install (requires Go 1.25+)

```sh
go install github.com/stackadnan/link-auditor@latest
```

### Download a release binary

Prebuilt binaries for Linux, macOS (Intel and Apple Silicon), and Windows are attached to every [GitHub Release](https://github.com/stackadnan/link-auditor/releases). Download the archive for your platform, extract it, and put the `link-auditor` (or `link-auditor.exe`) binary somewhere on your `PATH`.

### Build from source

```sh
git clone https://github.com/stackadnan/link-auditor.git
cd link-auditor
go build -o link-auditor .
```

## Usage

```
link-auditor scan <target_url> [flags]
```

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--concurrency` | `-c` | `20` | Number of concurrent worker goroutines |
| `--depth` | `-d` | `3` | Maximum recursive crawl depth |
| `--timeout` | `-t` | `10` | HTTP request timeout per page, in seconds |
| `--output` | `-o` | `table` | Output format: `table`, `json`, or `markdown` |
| `--export-file` | | `""` | Path to save the report to (defaults to stdout) |
| `--check-ssl` | | `true` | Check the SSL/TLS certificate expiration on the target host |
| `--ignore-external` | | `false` | Skip status checks for links to a different host |
| `--verbose` | `-v` | `false` | Print debug logs while crawling |

The target URL doesn't need an explicit scheme — `link-auditor scan example.com` is treated the same as `link-auditor scan https://example.com`.

### Examples

Scan a site with default settings:

```sh
link-auditor scan https://example.com
```

Crawl deeper and with more workers, for a large site:

```sh
link-auditor scan https://example.com --depth 5 --concurrency 50
```

Emit JSON for a CI pipeline and fail the build on broken links (link-auditor's own exit code already does this — see [Exit codes](#exit-codes)):

```sh
link-auditor scan https://example.com -o json --export-file report.json
```

Generate a Markdown report for a pull request comment:

```sh
link-auditor scan https://example.com -o markdown --export-file audit.md
```

Only audit the target site itself, skipping status checks on outbound links:

```sh
link-auditor scan https://example.com --ignore-external
```

Skip the SSL check entirely (e.g. for an internal HTTP-only service):

```sh
link-auditor scan http://internal.example.com --check-ssl=false
```

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Scan completed and no broken links were found |
| `1` | Either the scan found one or more broken links, or the tool itself failed (invalid flags, unreachable target, etc.) |

This makes `link-auditor scan ...` usable directly as a CI/CD pipeline gate — no output parsing required.

## How it works

```
main.go → cmd (Cobra CLI) → internal/crawler (worker pool) → internal/checker (SSL) → internal/report (formatter)
```

- **`internal/crawler`** owns a `Job`/`Result` channel pair and a pool of worker goroutines. Each worker pulls a job, makes the HTTP request, records the result, and — for internal HTML pages within the depth limit — streams the response through `golang.org/x/net/html`'s tokenizer to discover more links, without ever building a full DOM. A mutex-protected `State` map ensures each normalized URL is only ever enqueued once, no matter how many pages link to it.
- **`internal/checker`** opens a raw TLS connection to the target host and inspects the leaf certificate's validity window, independent of the crawl itself.
- **`internal/report`** implements a `Formatter` strategy interface with one implementation per output format (table, JSON, Markdown), all built from the same `[]crawler.Result` and `*checker.SSLInfo`.

See [`cmd/scan.go`](cmd/scan.go) for how these pieces are wired together, including graceful `SIGINT` handling and exit-code selection.

## Development

```sh
go build ./...      # compile everything
go vet ./...         # static analysis
go test ./...         # run the test suite
go test -race ./...   # run with the race detector (requires a C compiler)
gofmt -l .             # check formatting; add -w to fix in place
```

The test suite includes table-driven unit tests for URL normalization, host classification, HTML link extraction, and SSL certificate expiration/warning-threshold calculations, plus an end-to-end test that crawls an in-process `httptest.Server` and asserts on deduplication, depth limiting, and redirect handling.

## Contributing

Contributions are welcome.

1. Fork the repository and create a branch from `main`.
2. Make your change, keeping it focused — smaller, single-purpose pull requests are easier to review.
3. Add or update tests for any behavior change. `go test ./...` and `go vet ./...` must pass.
4. Run `gofmt -w .` before committing.
5. Open a pull request describing what changed and why.

Bug reports and feature requests are equally welcome via [GitHub Issues](https://github.com/stackadnan/link-auditor/issues) — please include the command you ran and, where relevant, the target site's structure (a minimal repro is ideal).

## License

[MIT](LICENSE)

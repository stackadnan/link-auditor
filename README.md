ki# link-auditor

**link-auditor** is a fast, concurrent Go command-line tool that crawls a website, finds broken links, audits SSL/TLS certificate expiration, and produces reports in table, JSON, or Markdown format.

## Why this project is useful

Use link-auditor to check:
- broken internal and external links
- redirect chains and status codes
- SSL/TLS certificate expiration
- crawl results for CI/CD quality gates

## Features

- Concurrent worker-pool crawler
- Thread-safe URL deduplication
- Full link classification: OK, redirect, broken, skipped
- Redirects are followed intelligently
- SSL/TLS certificate auditing
- Three report formats: terminal table, JSON, Markdown
- CI/CD-friendly exit codes

## Installation

### Install with Go

```bash
go install github.com/stackadnan/link-auditor@latest
```

### Build from source

```bash
git clone https://github.com/stackadnan/link-auditor.git
cd link-auditor
go build -o link-auditor .
```

## Usage

```bash
link-auditor scan <target_url> [flags]
```

Example:

```bash
link-auditor scan https://example.com
```

## Example flags

- `--concurrency` - number of worker goroutines
- `--depth` - recursive crawl depth
- `--timeout` - request timeout per page
- `--output` - `table`, `json`, or `markdown`
- `--export-file` - path to save the report
- `--check-ssl` - check SSL/TLS certificate expiration
- `--ignore-external` - skip external links
- `--verbose` - print debug logs

## Development

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -w .
```



## License

MIT

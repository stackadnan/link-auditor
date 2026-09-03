# link-auditor

<p align="center">
  <img src="docs/images/banner.png" alt="link-auditor" width="900">
</p>


**link-auditor** is a concurrent Go command-line tool that crawls a website, finds broken links, audits SSL/TLS certificate expiration, and produces reports in table, JSON, or Markdown format. It is designed to run both locally and inside CI/CD pipelines, with crawl limits and network-safety defaults suited to unattended use against untrusted, user-supplied targets.

## Why this project is useful

Use link-auditor to check:
- broken internal and external links
- redirect chains and status codes
- SSL/TLS certificate expiration
- crawl results for CI/CD quality gates

## Features

- Concurrent worker-pool crawler with a configurable page and response-size budget
- Thread-safe URL deduplication
- Full link classification: OK, redirect, broken, skipped
- Redirects are reported as their own status, not silently followed
- Optional robots.txt compliance
- SSL/TLS certificate auditing with a configurable expiration-warning window
- Three report formats: terminal table, JSON, Markdown
- Configurable `--fail-on` policy and CI/CD-friendly exit codes
- Refuses to connect to private/internal network addresses by default

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

Examples:

```bash
link-auditor scan https://example.com
link-auditor scan example.com --depth 5 --concurrency 40
link-auditor scan https://example.com -o json --export-file report.json
link-auditor scan https://example.com --max-pages 500 --fail-on any
link-auditor scan http://localhost:8080 --allow-private-addresses
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--concurrency`, `-c` | `20` | Number of concurrent worker goroutines |
| `--depth`, `-d` | `3` | Maximum recursive crawl depth (link hops from the root URL) |
| `--timeout`, `-t` | `10` | HTTP request timeout per page, in seconds |
| `--max-pages` | `0` (unlimited) | Maximum number of distinct URLs to crawl |
| `--max-body-size` | `10485760` (10 MiB) | Maximum response body size read per request, in bytes |
| `--user-agent` | `link-auditor/<version>` | `User-Agent` header sent with every request |
| `--output`, `-o` | `table` | Output format: `table`, `json`, or `markdown` |
| `--export-file` | *(stdout)* | Path to save the report to |
| `--check-ssl` | `true` | Check the SSL/TLS certificate expiration on the target host |
| `--ssl-warning-days` | `30` | Days before expiration at which a certificate is flagged as expiring soon |
| `--ignore-external` | `false` | Do not verify the status of external domain links |
| `--respect-robots` | `false` | Skip internal URLs disallowed by the target's `robots.txt` |
| `--allow-private-addresses` | `false` | Allow connecting to loopback, private, and other non-public addresses |
| `--fail-on` | `broken` | Findings that cause a non-zero exit: `broken`, `redirect`, `ssl`, `any` (repeatable / comma-separated) |
| `--verbose`, `-v` | `false` | Print debug logs during crawling |

## Output formats

**table** — a colored, human-readable summary for an interactive terminal. Colors are automatically suppressed when `--export-file` is used or output isn't a TTY (or `NO_COLOR` is set).

**json** — a single structured document intended as a stable machine-readable interface for tooling:

```json
{
  "generated_at": "...",
  "summary": { "target": "...", "total_checked": 12, "pages_crawled": 4, "ok": 9, "redirects": 1, "broken": 2, "skipped": 0, "internal": 10, "external": 2, "duration_ns": 512000000 },
  "ssl": { "host": "example.com", "issuer": "...", "days_remaining": 84, "expired": false, "expiring_soon": false },
  "results": [ { "url": "...", "status_code": 200, "link_type": "internal", "depth": 0, "response_time_ns": 12345678 } ]
}
```

Existing fields are not renamed across releases; new fields may be added. A `--max-pages` crawl that hit its budget sets `summary.pages_limited: true`, and each result carries `crawled: true` when it was an internal page whose body was parsed for further links (as opposed to a link that was only status-checked).

**markdown** — GitHub-flavored Markdown, suitable for a pull request comment or a CI job summary/artifact.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Scan completed with nothing the `--fail-on` policy considers blocking |
| `1` | Scan completed but blocking findings were detected, or another runtime error occurred |
| `2` | Invalid CLI usage or configuration (bad flag value, malformed target) |
| `130` | Interrupted (`SIGINT` / Ctrl-C) |

By default only broken links (`--fail-on broken`) cause a non-zero exit, matching pre-v0.2.0 behavior. Pass `--fail-on redirect`, `--fail-on ssl`, `--fail-on any`, or a comma-separated combination to make the gate stricter; the flag is a flat allow-list, not an expression language, so each value is evaluated independently and any one match fails the scan.

## CI/CD example (GitHub Actions)

```yaml
- name: Audit links
  run: |
    go install github.com/stackadnan/link-auditor@latest
    link-auditor scan https://example.com \
      --max-pages 500 \
      --output markdown \
      --export-file link-report.md \
      --fail-on broken

- name: Upload report
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: link-report
    path: link-report.md
```

`--fail-on broken` (the default) fails the step only on broken links; the `if: always()` upload step still publishes the report even when the scan step fails the build.

## SSL/TLS certificate checking

When the target is served over HTTPS, link-auditor connects to it and inspects the leaf certificate: issuer, subject, validity window, days remaining, and whether it is expired or within `--ssl-warning-days` of expiring. Certificate verification uses the standard library's default trust store and hostname (SNI) validation — it is never weakened or bypassed. A certificate that cannot be retrieved at all (DNS failure, connection refused, handshake error, timeout) is reported as a connection error, distinct from a certificate that was retrieved but has expired.

## Crawl limits

`--max-pages` bounds how many distinct URLs a crawl will admit; once the budget is reached, further discovered URLs are dropped (not deferred — the crawl does not "come back" for them) and the report's summary notes that the crawl was limited. `--max-body-size` bounds how many bytes of a single response the crawler reads, whether it's parsing the page for links or just draining an already-classified response to reuse the connection; a page larger than the limit is parsed on a best-effort basis using whatever was read; links appearing after the cutoff are not discovered, the same way a browser would still render most of a truncated page.

## robots.txt behavior

`--respect-robots` fetches `/robots.txt` once from the scan target's own host and skips internal URLs disallowed under the `User-agent: *` group. This is a deliberately small subset of the Robots Exclusion Protocol:

- Only `User-agent` and `Disallow` lines are recognized; `Allow` directives and non-wildcard user-agent groups are not supported.
- Matching is a plain path-prefix test (no wildcards in the `Disallow` value itself).
- `robots.txt` only governs the crawl's own root host; external links are checked once for their status regardless of the external site's `robots.txt`, the same way a human clicking the link would be.
- If `robots.txt` cannot be retrieved (missing, network error, timeout, non-200 status), the crawl proceeds unrestricted rather than failing closed — an unreachable `robots.txt` is far more often a misconfigured or absent file than a deliberate blanket block.
- `robots.txt` itself is never added to the report as a checked link.

## Redirects

A 3xx response is reported as its own result (status code, response time, and the resolved `Location` target in `redirect_to`) rather than being silently followed and hidden behind its final destination. When an internal redirect target is still within the configured `--depth` budget, the crawler continues past it: each hop in a redirect chain becomes its own separate result in the report, not a single collapsed entry, and following a redirect does not itself consume a depth hop (the hop budget is about the links a page author wrote, not the redirects between them).

## HTTP status classification

```
2xx           = healthy
3xx           = redirect
4xx / 5xx     = broken
network error = broken
```

A few codes have semantics worth calling out explicitly rather than leaving to the general rule: `401` and `403` are still classified as broken, since an access-controlled link is worth flagging just as much as a missing one; `429` (rate limited) is also broken, so a server actively rate-limiting the crawl is surfaced rather than silently treated as healthy; `304 Not Modified` is a 3xx and is not broken. These decisions are locked in as regression tests in `internal/crawler`.

## Security considerations

link-auditor connects to arbitrary, user-supplied URLs and follows wherever a page's own links or HTTP redirects point, which makes it a plausible SSRF vector if left unguarded: a target could otherwise redirect the crawler at an internal service, a cloud metadata endpoint, or another address that was never meant to be reachable from outside. By default, link-auditor refuses to connect to loopback, RFC1918/ULA private, link-local (including the `169.254.169.254` cloud metadata address), unspecified, multicast, and carrier-grade-NAT addresses — for both the crawl itself and the SSL certificate check. This check runs after DNS resolution on every connection attempt (including ones made while following a redirect to a new host), so it is not defeated by a public hostname that resolves to, or redirects to, a private address.

Pass `--allow-private-addresses` when the scan target is intentionally internal, such as a local development server or a container in the same CI job. TLS certificate verification (chain of trust and hostname/SNI) is never weakened, with or without that flag.

## Limitations

- `--respect-robots` implements a small, well-defined subset of the Robots Exclusion Protocol (see above); it is not a full parser.
- The private-address guard (see Security considerations) covers the address ranges that matter for SSRF in practice; it is not an exhaustive reserved-IANA-block list.
- Only `<a href>` links are discovered; link-auditor does not execute JavaScript, so links injected client-side are not found.
- A redirect chain is followed only for internal targets within the `--depth` budget; an external redirect target is checked once and not itself crawled for further redirects.

## License

MIT

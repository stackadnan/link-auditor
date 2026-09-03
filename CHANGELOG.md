# Changelog

All notable changes to this project are documented in this file.

## [0.2.0]

Production-hardening release focused on making link-auditor dependable to run unattended, against untrusted targets, inside CI/CD.

### Added
- `--max-pages` bounds how many distinct URLs a crawl will admit; the report notes when a crawl was limited.
- `--max-body-size` bounds how many bytes of a response the crawler reads per request.
- `--user-agent` makes the `User-Agent` header configurable; it defaults to `link-auditor/<version>` instead of a hardcoded string.
- `--respect-robots` skips internal URLs disallowed by the target's `robots.txt` (`User-agent: *` group).
- `--ssl-warning-days` makes the "expiring soon" certificate warning window configurable (previously a fixed 30 days).
- `--fail-on` (`broken`, `redirect`, `ssl`, `any`) makes the CI failure policy configurable; the default (`broken`) matches pre-0.2.0 behavior.
- `--allow-private-addresses` opts out of the new default SSRF protection (see Security below).
- Exit code `2` for invalid CLI usage/configuration and `130` for an interrupted (`SIGINT`) scan, alongside the existing `0`/`1`.
- `internal/netguard`, a small package that refuses outbound connections to loopback/private/link-local/carrier-grade-NAT addresses by default, applied to both the crawler and the SSL certificate check, including on redirect hops.
- Report summaries now include the scan target, a pages-crawled count distinct from links-checked, and whether the crawl hit its page limit.
- Skipped results carry a `skip_reason` (`ignore-external` or `robots.txt`) instead of a bare boolean.

### Changed
- `checker.CheckCertificate` and `checker.EvaluateCertificate` take an `Options`/threshold parameter instead of a package-level constant.
- `report.BuildSummary` takes the scan target and page-limit flag in addition to the results and duration.
- The response body read for HTML link discovery is now bounded (see `--max-body-size`) rather than unbounded.

### Security
- Outbound connections to non-public address space (RFC1918/ULA private ranges, loopback, link-local including the `169.254.169.254` cloud metadata address, unspecified, multicast, and RFC6598 carrier-grade NAT) are refused by default, for both the crawler and the SSL check, and the check is re-applied after DNS resolution on every connection attempt so it cannot be bypassed by a redirect to a private address. See the README's "Security considerations" section.

No changes to certificate trust-chain or hostname (SNI) verification were made; both were already correct (no `InsecureSkipVerify`) and remain so.

package crawler

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// robotsMaxBodyBytes bounds how much of a robots.txt response is read,
// protecting against a pathological or malicious robots.txt inflating
// memory use. Real robots.txt files are tiny; this is generous headroom.
const robotsMaxBodyBytes = 512 << 10 // 512 KiB

// robotsRules holds the Disallow prefixes that apply to link-auditor's
// crawl, taken from the User-agent: * group of a robots.txt document.
//
// This implements a deliberately small subset of the Robots Exclusion
// Protocol: only "User-agent" and "Disallow" lines are recognized, a line
// applies to the most recently seen User-agent group, matching is a plain
// path-prefix test, and Allow directives and non-wildcard user-agent groups
// are not supported. See the README's Limitations section.
type robotsRules struct {
	disallow []string
}

// Disallowed reports whether path is blocked by any Disallow rule. A nil
// *robotsRules (the state when --respect-robots is off) never disallows
// anything.
func (r *robotsRules) Disallowed(path string) bool {
	if r == nil {
		return false
	}
	for _, prefix := range r.disallow {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// fetchRobots retrieves and parses robots.txt for rootURL's origin using
// client. Per the long-standing robots.txt convention, any failure to fetch
// it (network error, timeout, non-200 status, oversized or malformed body)
// is treated the same as "no rules": the crawl proceeds unrestricted rather
// than failing closed, since an unreachable robots.txt is far more often a
// misconfigured or absent file than a deliberate blanket block.
//
// robots.txt itself is never added to the crawl's Result set: it is a
// crawler-policy input, not a link being audited.
func fetchRobots(ctx context.Context, client *http.Client, rootURL *url.URL, userAgent string) *robotsRules {
	robotsURL := &url.URL{Scheme: rootURL.Scheme, Host: rootURL.Host, Path: "/robots.txt"}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL.String(), nil)
	if err != nil {
		return &robotsRules{}
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return &robotsRules{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &robotsRules{}
	}

	return parseRobots(io.LimitReader(resp.Body, robotsMaxBodyBytes))
}

// parseRobots extracts Disallow rules from the User-agent: * group(s) of a
// robots.txt document; see robotsRules for the supported subset.
func parseRobots(r io.Reader) *robotsRules {
	rules := &robotsRules{}
	inWildcardGroup := false

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := stripRobotsComment(scanner.Text())
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			inWildcardGroup = value == "*"
		case "disallow":
			if inWildcardGroup && value != "" {
				rules.disallow = append(rules.disallow, value)
			}
		}
	}
	return rules
}

func stripRobotsComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}
	return line
}

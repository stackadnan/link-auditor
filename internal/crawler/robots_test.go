package crawler

import (
	"strings"
	"testing"
)

func TestParseRobots(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		allowed []string
		blocked []string
	}{
		{
			name:    "simple disallow rule",
			body:    "User-agent: *\nDisallow: /private\n",
			allowed: []string{"/", "/public"},
			blocked: []string{"/private", "/private/page"},
		},
		{
			name:    "empty Disallow value means allow everything",
			body:    "User-agent: *\nDisallow:\n",
			allowed: []string{"/", "/anything"},
		},
		{
			name:    "comments and blank lines are ignored",
			body:    "# comment\nUser-agent: *\n\n# another comment\nDisallow: /admin # trailing comment\n",
			allowed: []string{"/"},
			blocked: []string{"/admin"},
		},
		{
			name:    "rules under a non-wildcard group do not apply",
			body:    "User-agent: Googlebot\nDisallow: /only-google\n",
			allowed: []string{"/only-google"},
		},
		{
			name:    "rules resume once a wildcard group starts again",
			body:    "User-agent: Googlebot\nDisallow: /only-google\nUser-agent: *\nDisallow: /everyone\n",
			allowed: []string{"/only-google"},
			blocked: []string{"/everyone"},
		},
		{
			name:    "multiple disallow lines accumulate",
			body:    "User-agent: *\nDisallow: /a\nDisallow: /b\n",
			blocked: []string{"/a", "/b/sub"},
			allowed: []string{"/c"},
		},
		{
			name:    "field names are case-insensitive",
			body:    "USER-AGENT: *\nDISALLOW: /admin\n",
			blocked: []string{"/admin"},
		},
		{
			name:    "malformed lines are ignored, not fatal",
			body:    "not a valid line\nUser-agent: *\nDisallow: /admin\n",
			blocked: []string{"/admin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := parseRobots(strings.NewReader(tt.body))
			for _, path := range tt.allowed {
				if rules.Disallowed(path) {
					t.Errorf("Disallowed(%q) = true, want false", path)
				}
			}
			for _, path := range tt.blocked {
				if !rules.Disallowed(path) {
					t.Errorf("Disallowed(%q) = false, want true", path)
				}
			}
		})
	}
}

// TestRobotsRules_NilIsAlwaysAllowed verifies that a nil *robotsRules (the
// state when --respect-robots is off) never disallows a path, so callers
// don't need a separate "is robots enabled" check at every call site.
func TestRobotsRules_NilIsAlwaysAllowed(t *testing.T) {
	var rules *robotsRules
	if rules.Disallowed("/anything") {
		t.Error("Disallowed() on a nil *robotsRules = true, want false")
	}
}

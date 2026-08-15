package checker

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"
)

// TestEvaluateCertificate exercises the pure expiration/warning-threshold
// logic against synthetic certificates, without any network I/O. This
// keeps the test fast and hermetic while still covering the calculation
// CheckCertificate depends on.
func TestEvaluateCertificate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		notAfter         time.Time
		wantDaysRemain   int
		wantExpired      bool
		wantExpiringSoon bool
	}{
		{
			name:             "healthy, far from expiring",
			notAfter:         now.Add(90 * 24 * time.Hour),
			wantDaysRemain:   90,
			wantExpired:      false,
			wantExpiringSoon: false,
		},
		{
			name:             "expiring soon, well within the 30 day window",
			notAfter:         now.Add(10 * 24 * time.Hour),
			wantDaysRemain:   10,
			wantExpired:      false,
			wantExpiringSoon: true,
		},
		{
			name:             "exactly at the 30 day warning threshold (inclusive)",
			notAfter:         now.Add(WarningThreshold),
			wantDaysRemain:   30,
			wantExpired:      false,
			wantExpiringSoon: true,
		},
		{
			name:             "just outside the warning threshold",
			notAfter:         now.Add(WarningThreshold + 24*time.Hour),
			wantDaysRemain:   31,
			wantExpired:      false,
			wantExpiringSoon: false,
		},
		{
			name:             "already expired",
			notAfter:         now.Add(-5 * 24 * time.Hour),
			wantDaysRemain:   -5,
			wantExpired:      true,
			wantExpiringSoon: false,
		},
		{
			name:             "expires at this exact instant is treated as not yet expired",
			notAfter:         now,
			wantDaysRemain:   0,
			wantExpired:      false,
			wantExpiringSoon: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cert := &x509.Certificate{
				Issuer:    pkix.Name{CommonName: "Test CA"},
				Subject:   pkix.Name{CommonName: "example.com"},
				NotBefore: now.Add(-365 * 24 * time.Hour),
				NotAfter:  tt.notAfter,
			}

			info := EvaluateCertificate("example.com", cert, now)

			if info.Host != "example.com" {
				t.Errorf("Host = %q, want %q", info.Host, "example.com")
			}
			if info.Issuer != "Test CA" {
				t.Errorf("Issuer = %q, want %q", info.Issuer, "Test CA")
			}
			if info.DaysRemaining != tt.wantDaysRemain {
				t.Errorf("DaysRemaining = %d, want %d", info.DaysRemaining, tt.wantDaysRemain)
			}
			if info.Expired != tt.wantExpired {
				t.Errorf("Expired = %v, want %v", info.Expired, tt.wantExpired)
			}
			if info.ExpiringSoon != tt.wantExpiringSoon {
				t.Errorf("ExpiringSoon = %v, want %v", info.ExpiringSoon, tt.wantExpiringSoon)
			}
			if info.Error != "" {
				t.Errorf("Error = %q, want empty", info.Error)
			}
		})
	}
}

func TestEvaluateCertificate_ExpiredAndExpiringSoonAreMutuallyExclusive(t *testing.T) {
	now := time.Now()
	cert := &x509.Certificate{
		NotBefore: now.Add(-365 * 24 * time.Hour),
		NotAfter:  now.Add(-1 * time.Hour), // expired an hour ago
	}

	info := EvaluateCertificate("example.com", cert, now)

	if !info.Expired {
		t.Error("Expired = false, want true")
	}
	if info.ExpiringSoon {
		t.Error("ExpiringSoon = true, want false: an already-expired certificate should not also be flagged as merely 'expiring soon'")
	}
}

func TestHostOnly(t *testing.T) {
	tests := []struct{ in, want string }{
		{"example.com:443", "example.com"},
		{"example.com", "example.com"},
		{"example.com:8443", "example.com"},
		{"192.0.2.1:443", "192.0.2.1"},
	}
	for _, tt := range tests {
		if got := hostOnly(tt.in); got != tt.want {
			t.Errorf("hostOnly(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCheckCertificate_ConnectionFailure verifies that a host which cannot
// be reached produces a populated *SSLInfo with a descriptive Error rather
// than a nil result, so callers (the report formatters in particular) can
// always safely dereference the returned SSLInfo. This exercises real
// network code but only needs a fast local failure (connection refused on
// an unused loopback port), so it stays hermetic and quick.
func TestCheckCertificate_ConnectionFailure(t *testing.T) {
	info, err := CheckCertificate("127.0.0.1:1", 2*time.Second)
	if err == nil {
		t.Fatal("CheckCertificate() error = nil, want a connection error")
	}
	if info == nil {
		t.Fatal("CheckCertificate() info = nil, want a non-nil SSLInfo describing the failure")
	}
	if info.Error == "" {
		t.Error("info.Error is empty, want a description of the connection failure")
	}
}

package checker

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"strings"
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

			info := EvaluateCertificate("example.com", cert, now, 0)

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

	info := EvaluateCertificate("example.com", cert, now, 0)

	if !info.Expired {
		t.Error("Expired = false, want true")
	}
	if info.ExpiringSoon {
		t.Error("ExpiringSoon = true, want false: an already-expired certificate should not also be flagged as merely 'expiring soon'")
	}
}

// TestEvaluateCertificate_CustomWarningThreshold verifies that a
// non-default warning threshold (as set via --ssl-warning-days) is honored
// instead of the package default.
func TestEvaluateCertificate_CustomWarningThreshold(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cert := &x509.Certificate{
		NotBefore: now.Add(-365 * 24 * time.Hour),
		NotAfter:  now.Add(10 * 24 * time.Hour), // 10 days remaining
	}

	// A wider 60-day warning window flags it as expiring soon...
	if info := EvaluateCertificate("example.com", cert, now, 60*24*time.Hour); !info.ExpiringSoon {
		t.Error("ExpiringSoon = false with a 60-day threshold and 10 days remaining, want true")
	}
	// ...while a narrower 5-day window does not.
	if info := EvaluateCertificate("example.com", cert, now, 5*24*time.Hour); info.ExpiringSoon {
		t.Error("ExpiringSoon = true with a 5-day threshold and 10 days remaining, want false")
	}
	// Zero falls back to the package default (30 days), which 10 days
	// remaining still falls within.
	if info := EvaluateCertificate("example.com", cert, now, 0); !info.ExpiringSoon {
		t.Error("ExpiringSoon = false with the default threshold and 10 days remaining, want true")
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
//
// AllowPrivateAddresses is set so this test exercises the dial-failure path
// itself rather than being short-circuited by netguard; that policy is
// covered separately by TestCheckCertificate_BlocksPrivateAddressByDefault.
func TestCheckCertificate_ConnectionFailure(t *testing.T) {
	info, err := CheckCertificate("127.0.0.1:1", 2*time.Second, Options{AllowPrivateAddresses: true})
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

// TestCheckCertificate_BlocksPrivateAddressByDefault verifies that probing
// the SSL certificate of a loopback/private address is refused unless the
// caller explicitly opts in via Options.AllowPrivateAddresses, keeping the
// certificate checker consistent with the crawler's own SSRF hardening
// (see internal/netguard) for the same target host.
func TestCheckCertificate_BlocksPrivateAddressByDefault(t *testing.T) {
	info, err := CheckCertificate("127.0.0.1:1", 2*time.Second, Options{})
	if err == nil {
		t.Fatal("CheckCertificate() error = nil, want the connection to be refused by netguard")
	}
	if info == nil || info.Error == "" {
		t.Fatal("CheckCertificate() should return a populated SSLInfo.Error describing why the connection was refused")
	}
	if !strings.Contains(info.Error, "private") && !strings.Contains(info.Error, "reserved") {
		t.Errorf("info.Error = %q, want it to explain the private-address policy", info.Error)
	}
}

// TestCheckCertificate_TLSHandshakeFailure verifies that a server which
// accepts the TCP connection but never completes a TLS handshake (i.e. not
// speaking TLS at all) is reported as a certificate-check failure distinct
// from an outright connection refusal, matching the "connection errors"
// case tracked separately from certificate validity by SSLInfo.
func TestCheckCertificate_TLSHandshakeFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close() // close immediately without ever speaking TLS
		}
	}()

	info, err := CheckCertificate(ln.Addr().String(), 2*time.Second, Options{AllowPrivateAddresses: true})
	if err == nil {
		t.Fatal("CheckCertificate() error = nil, want a TLS handshake error")
	}
	if info == nil || info.Error == "" {
		t.Fatal("CheckCertificate() should return a populated SSLInfo.Error describing the handshake failure")
	}
}

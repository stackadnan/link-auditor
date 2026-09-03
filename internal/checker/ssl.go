// Package checker inspects the SSL/TLS certificate presented by a remote
// host and reports how close it is to expiring.
package checker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"

	"github.com/stackadnan/link-auditor/internal/netguard"
)

// WarningThreshold is the default warning window used when Options.
// WarningThreshold is left zero: how far out from expiration a certificate
// is flagged as "expiring soon". It is configurable per call via
// Options.WarningThreshold (see the --ssl-warning-days flag).
const WarningThreshold = 30 * 24 * time.Hour

// Options controls CheckCertificate's behavior.
type Options struct {
	// WarningThreshold overrides the default 30-day "expiring soon" window.
	// Zero means "use WarningThreshold".
	WarningThreshold time.Duration
	// AllowPrivateAddresses permits connecting to loopback, private, and
	// other non-public address space. It defaults to false so that probing
	// a scan target's certificate is subject to the same SSRF-hardening
	// policy as the crawler itself (see internal/netguard); pass true only
	// when the caller has explicitly opted in via --allow-private-addresses.
	AllowPrivateAddresses bool
}

// SSLInfo describes the leaf TLS certificate presented by a host.
type SSLInfo struct {
	Host          string    `json:"host"`
	Issuer        string    `json:"issuer,omitempty"`
	Subject       string    `json:"subject,omitempty"`
	NotBefore     time.Time `json:"not_before,omitempty"`
	NotAfter      time.Time `json:"not_after,omitempty"`
	DaysRemaining int       `json:"days_remaining"`
	Expired       bool      `json:"expired"`
	ExpiringSoon  bool      `json:"expiring_soon"`
	// Error is set when the certificate could not be retrieved at all
	// (connection refused, DNS failure, handshake error, timeout). It is
	// distinct from Expired, which means a certificate was successfully
	// retrieved but has passed its validity window.
	Error string `json:"error,omitempty"`
}

// CheckCertificate connects to host over TLS and inspects the leaf
// certificate the server presents, reporting how many days remain before
// it expires. host may optionally include a port; ":443" is assumed when
// absent. The connection is closed before returning.
//
// On failure, both a non-nil *SSLInfo (with Error populated, suitable for
// direct inclusion in a report) and a non-nil error (for callers that want
// to log or wrap the underlying cause) are returned.
func CheckCertificate(host string, timeout time.Duration, opts Options) (*SSLInfo, error) {
	addr := host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(host, "443")
	}

	dialer := &net.Dialer{Timeout: timeout, Control: netguard.DialControl(opts.AllowPrivateAddresses)}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: hostOnly(host)})
	if err != nil {
		wrapped := fmt.Errorf("connecting to %s: %w", addr, err)
		return &SSLInfo{Host: host, Error: wrapped.Error()}, wrapped
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		err := fmt.Errorf("no certificates presented by %s", addr)
		return &SSLInfo{Host: host, Error: err.Error()}, err
	}

	return EvaluateCertificate(host, certs[0], time.Now(), opts.WarningThreshold), nil
}

// EvaluateCertificate derives an SSLInfo from a certificate's validity
// window relative to now, flagging it as "expiring soon" once fewer than
// warningThreshold remains (zero uses the default WarningThreshold). It
// contains no I/O and is kept separate from CheckCertificate specifically
// so the expiration and warning-threshold logic can be unit tested without
// a live network connection.
func EvaluateCertificate(host string, cert *x509.Certificate, now time.Time, warningThreshold time.Duration) *SSLInfo {
	if warningThreshold <= 0 {
		warningThreshold = WarningThreshold
	}

	remaining := cert.NotAfter.Sub(now)
	expired := now.After(cert.NotAfter)

	return &SSLInfo{
		Host:          host,
		Issuer:        cert.Issuer.CommonName,
		Subject:       cert.Subject.CommonName,
		NotBefore:     cert.NotBefore,
		NotAfter:      cert.NotAfter,
		DaysRemaining: int(remaining.Hours() / 24),
		Expired:       expired,
		ExpiringSoon:  !expired && remaining <= warningThreshold,
	}
}

// hostOnly strips any port suffix from host, for use as a TLS ServerName
// (SNI), which must not include a port.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

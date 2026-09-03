// Package netguard implements a conservative allow/deny policy for outbound
// network connections. link-auditor connects to arbitrary, user-supplied
// URLs (and whatever a page's own links or HTTP redirects point at), which
// makes it a classic SSRF vector if left unguarded: a target could redirect
// the crawler at an internal admin panel, a cloud metadata endpoint, or any
// other address that was never meant to be reachable from the outside.
// netguard denies connections to non-public address space by default.
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// extraBlocked holds address ranges that net/netip's IsPrivate/IsLoopback/
// IsLinkLocal* helpers do not already cover but that still identify
// non-public infrastructure worth denying by default.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // RFC 6598 shared address space (carrier-grade NAT)
}

// Blocked reports whether addr identifies a loopback, private, link-local,
// unspecified, multicast, or carrier-grade-NAT address rather than ordinary
// public internet space. IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1)
// are unmapped first so they cannot be used to smuggle a blocked IPv4
// address past the check.
func Blocked(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// DialControl returns a net.Dialer.Control function that rejects connection
// attempts to any address Blocked reports as non-public, or nil when allow
// is true, in which case the caller should leave Dialer.Control unset.
//
// Control is the right hook for this: the standard library calls it after
// DNS resolution has produced a concrete IP address but before the
// connection is actually made. Applying the check there, rather than to the
// URL's hostname, is what makes it effective against a redirect from a
// public hostname to a private address and against DNS-rebinding-style
// bypasses, not just a literal "http://127.0.0.1" target.
func DialControl(allow bool) func(network, address string, c syscall.RawConn) error {
	if allow {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("netguard: parsing resolved address %q: %w", address, err)
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("netguard: resolved address %q is not an IP: %w", host, err)
		}
		if Blocked(addr) {
			return fmt.Errorf("refusing to connect to private/reserved address %s (pass --allow-private-addresses to override)", addr)
		}
		return nil
	}
}

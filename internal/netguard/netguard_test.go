package netguard

import (
	"net/netip"
	"testing"
)

func TestBlocked(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv4 loopback range", "127.255.255.255", true},
		{"IPv6 loopback", "::1", true},
		{"RFC1918 10/8", "10.0.0.1", true},
		{"RFC1918 172.16/12", "172.16.0.1", true},
		{"RFC1918 192.168/16", "192.168.1.1", true},
		{"link-local (cloud metadata range)", "169.254.169.254", true},
		{"IPv6 link-local", "fe80::1", true},
		{"IPv6 unique local (ULA)", "fc00::1", true},
		{"unspecified IPv4", "0.0.0.0", true},
		{"unspecified IPv6", "::", true},
		{"multicast", "224.0.0.1", true},
		{"carrier-grade NAT", "100.64.0.1", true},
		{"IPv4-mapped IPv6 loopback", "::ffff:127.0.0.1", true},
		{"IPv4-mapped IPv6 private", "::ffff:10.0.0.1", true},
		{"public IPv4", "93.184.216.34", false},
		{"public IPv6", "2606:2800:220:1:248:1893:25c8:1946", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tt.addr, err)
			}
			if got := Blocked(addr); got != tt.want {
				t.Errorf("Blocked(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// TestDialControl_Allow verifies that DialControl(true) returns nil,
// meaning the caller leaves Dialer.Control unset and every address is
// reachable -- this is the --allow-private-addresses escape hatch.
func TestDialControl_Allow(t *testing.T) {
	if fn := DialControl(true); fn != nil {
		t.Error("DialControl(true) should return nil so Dialer.Control is left unset")
	}
}

// TestDialControl_Deny exercises the returned Control function directly
// against resolved addr:port strings, the same shape net.Dialer passes it
// after DNS resolution, without needing a real network connection.
func TestDialControl_Deny(t *testing.T) {
	fn := DialControl(false)
	if fn == nil {
		t.Fatal("DialControl(false) returned nil, want a non-nil Control function")
	}

	tests := []struct {
		name        string
		address     string
		wantBlocked bool
	}{
		{"loopback with port", "127.0.0.1:80", true},
		{"private RFC1918 with port", "10.0.0.1:443", true},
		{"link-local metadata address", "169.254.169.254:80", true},
		{"public address with port", "93.184.216.34:443", false},
		{"IPv6 public with port", "[2606:2800:220:1:248:1893:25c8:1946]:443", false},
		{"malformed address has no port", "127.0.0.1", true}, // SplitHostPort fails -> rejected
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fn("tcp", tt.address, nil)
			gotBlocked := err != nil
			if gotBlocked != tt.wantBlocked {
				t.Errorf("Control(%q) error = %v, wantBlocked %v", tt.address, err, tt.wantBlocked)
			}
		})
	}
}

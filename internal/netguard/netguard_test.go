package netguard

import (
	"net"
	"testing"
)

// TestBlockedRanges pins the exact set of ranges a daemon-side outbound request
// must refuse — the SSRF/rebinding guard shared by §15 egress and §13 callbacks.
func TestBlockedRanges(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// blocked: internal / non-public
		{"127.0.0.1", true},       // loopback
		{"::1", true},             // loopback v6
		{"169.254.169.254", true}, // link-local — cloud metadata
		{"169.254.0.1", true},     // link-local
		{"fe80::1", true},         // link-local v6
		{"10.1.2.3", true},        // RFC1918
		{"172.16.5.5", true},      // RFC1918
		{"192.168.0.1", true},     // RFC1918
		{"fc00::1", true},         // ULA
		{"100.64.0.1", true},      // CGNAT (RFC 6598) — IsPrivate omits this
		{"100.127.255.254", true}, // CGNAT upper edge
		{"224.0.0.1", true},       // multicast
		{"0.0.0.0", true},         // unspecified
		{"::", true},              // unspecified v6
		// allowed: genuinely public
		{"93.184.216.34", false},      // example.com
		{"8.8.8.8", false},            // public resolver
		{"1.1.1.1", false},            // public resolver
		{"100.63.255.255", false},     // just below CGNAT — public
		{"100.128.0.0", false},        // just above CGNAT — public
		{"2606:2800:220:1::1", false}, // public v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := Blocked(ip); got != c.want {
			t.Errorf("Blocked(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestBlockedNAT64 (#57 L1): an IPv6 address in the NAT64 well-known prefix
// (64:ff9b::/96) embeds an IPv4 target in its low 32 bits. The guard must unwrap
// it and apply the same block checks, so a metadata/private target can't be
// smuggled through in IPv6 clothing — while a NAT64 wrapping a genuinely public
// address is still allowed.
func TestBlockedNAT64(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"64:ff9b::169.254.169.254", true}, // cloud metadata via NAT64
		{"64:ff9b::10.0.0.1", true},        // RFC1918 via NAT64
		{"64:ff9b::127.0.0.1", true},       // loopback via NAT64
		{"64:ff9b::100.64.0.1", true},      // CGNAT via NAT64
		{"64:ff9b::8.8.8.8", false},        // public target — legitimately routable
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := Blocked(ip); got != c.want {
			t.Errorf("Blocked(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestBlockedNil — an address that can't be vetted is never safe to dial.
func TestBlockedNil(t *testing.T) {
	if !Blocked(nil) {
		t.Fatal("Blocked(nil) = false, want true (unvettable address must be blocked)")
	}
}

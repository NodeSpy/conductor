// Package netguard centralizes the SSRF / DNS-rebinding IP-range check shared
// by conductor's daemon-side outbound paths: the §15 enforced-egress proxy and
// the §13 callable callback poster. Both resolve a caller-influenced hostname
// daemon-side (outside any sandbox), so a name that resolves to an internal
// address — cloud metadata (169.254.169.254), loopback, RFC1918/ULA private
// space, CGNAT, link-local — is a rebinding gateway straight off the daemon's
// own network unless the operator opted that exact IP in literally.
package netguard

import "net"

// cgnat is RFC 6598 shared address space (100.64.0.0/10): carrier-grade NAT,
// routable inside an operator network but never on the public internet. Go's
// net.IP.IsPrivate covers RFC1918 + ULA but NOT this range, so we add it.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// Blocked reports whether ip sits in a range a daemon-side outbound request must
// not reach unless the operator opted that exact IP in. It is the union of:
//   - loopback (127.0.0.0/8, ::1)
//   - link-local unicast + multicast (169.254.0.0/16 incl. cloud metadata, fe80::/10)
//   - all multicast + interface-local multicast
//   - unspecified (0.0.0.0, ::)
//   - RFC1918 + ULA private (10/8, 172.16/12, 192.168/16, fc00::/7) via IsPrivate
//   - CGNAT shared space (100.64.0.0/10, RFC 6598), which IsPrivate omits
//
// A nil/unparseable address is treated as blocked — an address that can't be
// vetted is not safe to dial.
func Blocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && cgnat.Contains(v4) {
		return true
	}
	return false
}

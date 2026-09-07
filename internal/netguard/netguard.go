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

// nat64WellKnown is the NAT64 well-known prefix (RFC 6052, 64:ff9b::/96): the
// low 32 bits embed an IPv4 address, so 64:ff9b::169.254.169.254 is a route
// to the cloud metadata endpoint dressed up as a public-looking IPv6 address.
// A guard that inspected only the IPv6 form would wave it through, so we unwrap
// the embedded IPv4 and re-apply the block checks to it.
var nat64WellKnown = net.IPNet{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)}

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
	// NAT64: an IPv6 address in the well-known prefix carries an IPv4 target in
	// its low 32 bits — unwrap and vet that so it can't smuggle a blocked v4.
	if ip.To4() == nil {
		if ip16 := ip.To16(); ip16 != nil && nat64WellKnown.Contains(ip16) {
			if Blocked(net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])) {
				return true
			}
		}
	}
	return false
}

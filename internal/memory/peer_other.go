//go:build !linux

package memory

import "net"

// peerInfo has no peer-credential source on this platform: the skill broker
// claims sessions unbound (token-only) here.
func peerInfo(net.Conn) Peer { return Peer{} }

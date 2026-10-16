//go:build !linux && !darwin

package agentsandbox

import "net"

// PeerUID cannot read peer credentials on this platform.
func PeerUID(net.Conn) (uint32, bool) { return 0, false }

//go:build !linux && !darwin

package agentsandbox

import "net"

// PeerUID cannot read peer credentials on this platform.
func PeerUID(net.Conn) (uint32, bool) { return 0, false }

// PeerCred cannot read peer credentials on this platform.
func PeerCred(net.Conn) (uid uint32, pid int, ok bool) { return 0, 0, false }

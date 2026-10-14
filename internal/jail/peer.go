package jail

import "net"

// PeerUID is the uid of the process at the other end of a unix socket
// connection (false = unknown: not a unix socket, or the OS won't say).
func PeerUID(conn net.Conn) (uint32, bool) { return peerUID(conn) }

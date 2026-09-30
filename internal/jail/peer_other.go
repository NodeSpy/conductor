//go:build !linux && !darwin

package jail

import "net"

// peerUID has no source here; the broker then rests on the socket's
// placement and the per-dispatch token alone.
func peerUID(net.Conn) (uint32, bool) { return 0, false }

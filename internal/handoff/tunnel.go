package handoff

import "context"

// Tunnel exposes conductor's local hand-off listener at a public (or LAN) URL.
// Open is called fresh per hand-off draft (see WebChannel.Present); closeFn
// tears down whatever Open started and is always safe to call, including more
// than once.
//
// Conductor implements no tunnel itself: the web connector's `expose:` names
// an exposure connector (a builtin like lan or tunnel, or a plugin) and the
// connector package adapts its `exposes` verb to this interface
// (plugin-contract.md §2.3).
type Tunnel interface {
	Open(ctx context.Context, localAddr string) (publicURL string, closeFn func() error, err error)
}

func noopClose() error { return nil }

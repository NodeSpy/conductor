package member

import "github.com/NodeSpy/conductor/internal/grant"

// fileSHA is the same digest the jail's broker re-checks at use.
func fileSHA(path string) (string, error) { return grant.FileSHA256(path) }

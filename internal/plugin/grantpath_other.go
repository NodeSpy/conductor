//go:build !unix

package plugin

import "path/filepath"

// resolveOwned: there is no plugin jail here (buildJailed refuses), so a
// declared path is only cleaned.
func resolveOwned(p string) (string, error) { return filepath.Clean(p), nil }

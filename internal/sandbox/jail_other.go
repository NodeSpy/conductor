//go:build !linux

package sandbox

import "fmt"

// buildJail only ever runs inside a Linux mount namespace (RunEnter carries a
// Binds allow-list solely when launched there).
func buildJail([]BindMount) error {
	return fmt.Errorf("namespace filesystem jail runs inside Linux namespaces only")
}

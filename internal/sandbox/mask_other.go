//go:build !linux

package sandbox

import "fmt"

// maskPath only ever runs inside a Linux mount namespace.
func maskPath(string) error {
	return fmt.Errorf("filesystem masking runs inside Linux namespaces only")
}

//go:build !linux && !darwin

package keycache

import "errors"

// copyFromHome: a publish copies from a home only where it can walk one by
// file descriptor.
func copyFromHome(home, rel, dst string, budget *int64) error {
	return errors.New("publishing is not supported on this platform")
}

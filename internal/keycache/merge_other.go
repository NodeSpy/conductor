//go:build !linux

package keycache

import "errors"

// mergeLayers: only Linux restores by overlay, so no launch elsewhere has
// an upper layer to merge.
func mergeLayers(lower, upper, dst string, budget *int64) error {
	return errors.New("no overlay layers on this platform")
}

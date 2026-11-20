//go:build !linux && !darwin

package keycache

import "os"

func cloneFile(src, dst string, mode os.FileMode) (bool, error) { return false, nil }

func cloneDir(src, dst string) bool { return false }

// OverlayUsable: no overlay here.
func OverlayUsable(lowerRoot, upperRoot string) (bool, string) {
	return false, "no overlay mounts on this platform"
}

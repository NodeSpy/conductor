//go:build !linux

package sandbox

import "fmt"

// loopbackUp only ever runs inside a Linux namespace/container; anywhere
// else the helper has no business executing.
func loopbackUp() error {
	return fmt.Errorf("sandbox-net runs inside Linux namespaces/containers only")
}

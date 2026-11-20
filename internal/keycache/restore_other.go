//go:build !linux && !darwin

package keycache

import "errors"

// copyIn: a copy restore needs a walk by file descriptor, which this
// platform does not have here.
func (m *Manager) copyIn(home, lower, target string) error {
	return errors.New("copy restores are not supported on this platform")
}

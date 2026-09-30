//go:build !linux && !darwin

package jail

import (
	"context"
	"fmt"
	"io"
)

// runHost has no copy-on-write backend on this platform: host commands are
// refused rather than run with the operator's real, writable home.
func runHost(context.Context, *Manager, hostRun, io.Writer, io.Writer) (hostResult, error) {
	return hostResult{}, fmt.Errorf("host commands need Linux namespaces or macOS Seatbelt")
}

// RunHostExec is Linux-only.
func RunHostExec(string) int { return 213 }

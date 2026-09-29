//go:build !darwin

package jail

import "github.com/NodeSpy/conductor/internal/sandbox"

func (m *Manager) layoutDarwin(*Dispatch, string) ([]sandbox.BindMount, *sandbox.AgentProfile, string, error) {
	panic("layoutDarwin on a non-darwin build")
}

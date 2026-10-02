package flow

import (
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core/coretest"
)

// The fixture connectors stand in for the forge and chat plugins under
// `use: github` and `use: slack`.
func init() {
	connector.RegisterInProcessConnector(coretest.Forge)
	connector.RegisterInProcessConnector(coretest.Chat)
}

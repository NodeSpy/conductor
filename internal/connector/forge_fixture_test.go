package connector

import "github.com/NodeSpy/conductor/internal/core/coretest"

// The fixture connectors stand in for the forge and chat plugins under
// `use: github` and `use: slack`.
func init() { RegisterInProcessConnector(coretest.Forge); RegisterInProcessConnector(coretest.Chat) }

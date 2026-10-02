package flow

import (
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core/coretest"
)

// The fixture forge connector stands in for a forge plugin under `use: github`.
func init() { connector.RegisterInProcessConnector(coretest.Forge) }

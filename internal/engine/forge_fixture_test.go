package engine

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core/coretest"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The fixture forge connector stands in for the github plugin: the engine's
// test triggers come from its instance "i", whose declared credentials are
// minted through its read_token / write_token verbs (remediationVerbs).
func init() { connector.RegisterInProcessConnector(coretest.Forge) }

func forgeRegistry(t *testing.T) *connector.Registry {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("connectors:\n  i: { use: github }\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

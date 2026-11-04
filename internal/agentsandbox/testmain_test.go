package agentsandbox

import (
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain keeps every test here off the developer's real home (see testhome).
func TestMain(m *testing.M) {
	if spec := os.Getenv(usageHelperEnv); spec != "" {
		usageHelper(spec)
		return
	}
	testhome.Main(m)
}

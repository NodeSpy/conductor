package agentsandbox

import (
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain keeps every test here off the developer's real home (see
// testhome) — except the container probe, whose environment, inside an
// agent container, is what it checks.
func TestMain(m *testing.M) {
	if os.Getenv("AGENTSANDBOX_CPROBE") != "" {
		os.Exit(m.Run())
	}
	testhome.Main(m)
}

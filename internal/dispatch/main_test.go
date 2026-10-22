package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain points HOME at a throwaway directory for the whole package. A
// checkout:none dispatch makes its run directory under ~/.conductor/runs
// (makeRunDir), so any test that dispatches without its own t.Setenv("HOME")
// otherwise litters the developer's real home — and never cleans it up.
func TestMain(m *testing.M) { testhome.Main(m) }

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain keeps every test here off the developer's real home (see
// testhome), and removes the conductor binary the real-browser tests build.
func TestMain(m *testing.M) {
	root, err := testhome.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		os.Exit(1)
	}
	code := m.Run()
	if conductorBin != "" && conductorErr == nil {
		os.RemoveAll(filepath.Dir(conductorBin))
	}
	os.RemoveAll(root)
	os.Exit(code)
}

package dispatch

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at a throwaway directory for the whole package. A
// checkout:none dispatch makes its run directory under ~/.conductor/runs
// (makeRunDir), so any test that dispatches without its own t.Setenv("HOME")
// otherwise litters the developer's real home — and never cleans it up.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "conductor-dispatch-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch tests: make temp HOME:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

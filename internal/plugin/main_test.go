package plugin

import (
	"os"
	"testing"
)

// TestMain grants paths inside the test's temp dir like any other: the
// tests' fake homes, tools and state live there, and the system temp dir is
// one of the runtime dirs the jail refuses everything inside. A test of
// that refusal clears it (noGrantExempt).
func TestMain(m *testing.M) {
	GrantInsideTempForTests()
	os.Exit(m.Run())
}

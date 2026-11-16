package agentsandbox

import (
	"bytes"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain keeps every test here off the developer's real home (see testhome).
func TestMain(m *testing.M) {
	if spec := os.Getenv(usageHelperEnv); spec != "" {
		usageHelper(spec)
		return
	}
	if fakeBinary() {
		// A copy of this test binary standing in for a host tool: it
		// sleeps, whatever it is named or run as (see fakeTool).
		if fakeToolSetup != nil {
			fakeToolSetup(os.Args[1:])
		}
		secs := 3600.0
		if len(os.Args) > 1 {
			if s, err := strconv.ParseFloat(os.Args[1], 64); err == nil {
				secs = s
			}
		}
		time.Sleep(time.Duration(secs * float64(time.Second)))
		return
	}
	testhome.Main(m)
}

// fakeToolSetup prepares a fake tool from its arguments before it sleeps
// (Linux: "hide" makes it undumpable).
var fakeToolSetup func(args []string)

// The trailers a copy of the test binary carries: the stand-in for a tool
// and a stranger (the same length, so the two are the same size but never
// the same bytes).
const (
	fakeToolTrailer     = "\x00agentsandbox fake tool\x00"
	fakeStrangerTrailer = "\x00agentsandbox stranger!\x00"
)

// fakeBinary says whether this process runs a trailered copy of the test
// binary.
func fakeBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	f, err := os.Open(exe)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	n := int64(len(fakeToolTrailer))
	if err != nil || fi.Size() < n {
		return false
	}
	b := make([]byte, n)
	if _, err := f.ReadAt(b, fi.Size()-n); err != nil {
		return false
	}
	return bytes.Equal(b, []byte(fakeToolTrailer)) || bytes.Equal(b, []byte(fakeStrangerTrailer))
}

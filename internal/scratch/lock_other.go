//go:build !unix

package scratch

import (
	"errors"
	"os"
)

// Without flock nothing is known to be unheld: a sweep removes nothing.
func lockExclusive(*os.File, bool) error { return errors.New("scratch: no directory locks on this OS") }

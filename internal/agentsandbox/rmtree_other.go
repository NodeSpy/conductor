//go:build !linux && !darwin

package agentsandbox

import "os"

func removeTree(path string) (int64, error) { return 0, os.RemoveAll(path) }

// The home locks are no-ops where the agent sandbox does not run.
func lockHomeShared(string) func() { return func() {} }

func tryLockHomeExclusive(string) (func(), bool) { return func() {}, true }

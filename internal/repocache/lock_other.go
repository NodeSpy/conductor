//go:build !linux && !darwin

package repocache

func lockFile(string) func()            { return func() {} }
func LockShared(string) (func(), error) { return func() {}, nil }
func lockHeld(string) bool              { return false }
func tryLock(string) (func(), bool)     { return func() {}, true }

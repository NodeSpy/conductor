//go:build !linux

package keycache

// syncFS is syncDir (tests replace it).
var syncFS = syncDir

// syncDir: no volatile layers to flush here.
func syncDir(string) {}

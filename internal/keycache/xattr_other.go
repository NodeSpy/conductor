//go:build !linux && !darwin

package keycache

func stripXattrs(string) {}

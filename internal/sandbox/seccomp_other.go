//go:build !linux

package sandbox

// ExecFilterSupported reports whether this build installs the Linux exec
// filter; there is none here (macOS confines exec in its Seatbelt profile).
func ExecFilterSupported() bool { return false }

func restrictExec() error { return nil }

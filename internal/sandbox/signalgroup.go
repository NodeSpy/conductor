package sandbox

import "syscall"

// rawKill is syscall.Kill. Tests replace it, so no test ever sends a real
// signal: a mutated guard then fails a test instead of signalling the host.
var rawKill = syscall.Kill

// signalGroup sends sig to process group pgid. It refuses pgid <= 1:
// kill(-1, sig) signals every process the caller may signal (the operator's
// whole session) and kill(0, sig) the caller's own group. Every group signal
// in this package goes through here.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return syscall.EINVAL
	}
	return rawKill(-pgid, sig)
}

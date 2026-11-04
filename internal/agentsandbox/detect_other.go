//go:build !linux && !darwin

package agentsandbox

import "errors"

func listProcs(string, func(int) bool) func() ([]procInfo, error) {
	return func() ([]procInfo, error) { return nil, errors.New("no process watcher on this platform") }
}

func signalProc(int, uint64) error { return errors.New("unsupported") }

func safeToSignal(int) bool { return false }

func procStart(int) (uint64, bool) { return 0, false }

func ownPgid() int { return -1 }

//go:build !linux && !darwin

package skill

// procAncestor cannot read process ancestry on this platform. ok=false
// always — a process-tree-bound session (MintPendingSession/BindRoot,
// MintSession with a root) then never authorizes anyone here, same as a
// transport with no peer credentials: fail closed rather than fall back to
// uid-wide trust (rt2 #12).
func procAncestor(int) (ppid int, start uint64, ok bool) { return 0, 0, false }

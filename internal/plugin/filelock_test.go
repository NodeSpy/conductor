package plugin

import (
	"testing"
	"time"
)

// TestLockInstallStateSerializesTwoHandles is finding 6: installed.yaml had
// only a process-local mutex, so a concurrent `conductor plugin update` and
// the daemon's own auto-update cycle (two SEPARATE OS processes, each with
// its own unrelated in-process mutex) could race a load-mutate-save cycle
// and lose a write. LockInstallState's flock is tied to the OPEN FILE
// DESCRIPTION, not the process, so two independently-opened handles on the
// SAME directory (acquired here from two goroutines, simulating two
// processes exactly as the task describes) genuinely serialize against each
// other — this is not just re-testing an in-process mutex.
func TestLockInstallStateSerializesTwoHandles(t *testing.T) {
	dir := t.TempDir()

	first, err := LockInstallState(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	acquired := make(chan struct{})
	released := make(chan struct{})
	go func() {
		// Simulates a second, independent process: its own handle, from
		// LockInstallState called fresh (a new os.File / open file
		// description on the SAME lockfile), not a copy of `first`.
		second, err := LockInstallState(dir)
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		second.Unlock()
		close(released)
	}()

	select {
	case <-acquired:
		t.Fatal("a second handle acquired the lock while the first still held it")
	case <-time.After(200 * time.Millisecond):
		// expected: still blocked
	}

	if err := first.Unlock(); err != nil {
		t.Fatalf("unlock first: %v", err)
	}

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the second handle never acquired the lock after the first released it")
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the second handle's Unlock never completed")
	}
}

// TestLockInstallStateEmptyDirIsNoOp proves the documented no-op fallback:
// an empty dir (no state directory resolvable) never blocks anything.
func TestLockInstallStateEmptyDirIsNoOp(t *testing.T) {
	l1, err := LockInstallState("")
	if err != nil {
		t.Fatal(err)
	}
	l2, err := LockInstallState("")
	if err != nil {
		t.Fatal(err)
	}
	if err := l1.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := l2.Unlock(); err != nil {
		t.Fatal(err)
	}
}

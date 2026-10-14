package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestLockInstallStateCLIAcquiresImmediatelyWhenFree is finding 6: the
// overwhelmingly common uncontended case (nothing else is touching install
// state) must pay no delay and no notification at all.
func TestLockInstallStateCLIAcquiresImmediatelyWhenFree(t *testing.T) {
	dir := t.TempDir()
	var notified []string
	l, err := LockInstallStateCLI(dir, time.Second, func(s string) { notified = append(notified, s) })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if len(notified) != 0 {
		t.Fatalf("notify called on an uncontended lock: %v", notified)
	}
}

// TestLockInstallStateCLITimesOutWithClearError is finding 6 (MEDIUM): a CLI
// command used to block on plugin.LockInstallState forever and SILENTLY
// when another process (the daemon's own auto-update cycle, say) held the
// install-state lock. LockInstallStateCLI must notify exactly once that it
// is waiting, then give up after its timeout with a named, actionable error
// — never leave the operator staring at a hung terminal with no
// explanation at all.
func TestLockInstallStateCLITimesOutWithClearError(t *testing.T) {
	dir := t.TempDir()
	holder, err := LockInstallState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Unlock()

	var mu sync.Mutex
	var notified []string
	start := time.Now()
	_, err = LockInstallStateCLI(dir, 100*time.Millisecond, func(s string) {
		mu.Lock()
		notified = append(notified, s)
		mu.Unlock()
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error while the lock is held by another handle")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should name the lock contention clearly: %v", err)
	}
	mu.Lock()
	n := len(notified)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("expected exactly one wait notification, got %d: %v", n, notified)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("returned before the timeout elapsed (%s)", elapsed)
	}
}

// TestLockInstallStateCLIAcquiresAfterHolderReleases proves the
// non-timeout path: once the current holder releases within the bound, the
// CLI caller gets the lock normally (after exactly one wait notification),
// never an error — the bound exists to stop an indefinite hang, not to
// refuse a peer that is genuinely about to finish.
func TestLockInstallStateCLIAcquiresAfterHolderReleases(t *testing.T) {
	dir := t.TempDir()
	holder, err := LockInstallState(dir)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		holder.Unlock()
	}()

	notified := make(chan string, 4)
	l, err := LockInstallStateCLI(dir, 5*time.Second, func(s string) { notified <- s })
	if err != nil {
		t.Fatalf("expected to acquire once the holder released: %v", err)
	}
	defer l.Unlock()
	select {
	case <-notified:
	default:
		t.Fatal("expected a wait notification before acquiring a contended lock")
	}
}

// TestOpenLockFileRefusesSymlink is finding 6 (security hardening): a
// tampered install directory where the lockfile path has been replaced with
// a symlink must be refused, not followed — opening it with O_NOFOLLOW is
// what makes that refusal happen at the open() syscall itself rather than
// relying on anything checked after the fact.
func TestOpenLockFileRefusesSymlink(t *testing.T) {
	if noFollowFlag == 0 {
		t.Skip("O_NOFOLLOW is not wired up on this platform")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, installLockFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if _, err := openLockFile(dir); err == nil {
		t.Fatal("expected openLockFile to refuse a symlinked lockfile")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should name the symlink refusal: %v", err)
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

package skill

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// rt2 #12: a skill session token delivered to a sandboxed agent must be
// usable only by that sandbox's own process tree — not by any other process
// sharing the daemon's uid (another sandbox, or any host process). These
// tests exercise the fix at both levels: the raw ancestry walk
// (processInTree, over REAL processes) and the broker's Authorize gate
// (MintSession / MintPendingSession / BindRoot).

// spawnSleeper starts a real, short-lived child process the test can anchor
// to or test against, returning its pid. t.Cleanup kills it.
func spawnSleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleeper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// spawnGrandchild starts `sh -c 'sleep 30 & echo $!; wait'`: a direct child
// of the test process (the returned rootPID) that itself forks a SECOND
// process (the returned grandchildPID) — a two-level descendant, proving the
// ancestry walk does not stop at one hop.
func spawnGrandchild(t *testing.T) (rootPID, grandchildPID int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn root: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	buf := make([]byte, 64)
	n, err := out.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("read grandchild pid: n=%d err=%v", n, err)
	}
	gc, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("parse grandchild pid from %q: %v", buf[:n], err)
	}
	return cmd.Process.Pid, gc
}

func TestProcessInTreeAcceptsRootAndDescendants(t *testing.T) {
	root := AnchorForPID(os.Getpid())
	if root.Start == 0 {
		t.Skip("no /proc or sysctl start time available on this platform")
	}
	if !processInTree(os.Getpid(), root) {
		t.Fatal("root is its own descendant (depth 0)")
	}
	child := spawnSleeper(t)
	if !processInTree(child, root) {
		t.Fatal("a direct child of root must be in its tree")
	}
	rootPID, grandchild := spawnGrandchild(t)
	gcRoot := AnchorForPID(rootPID)
	if !processInTree(grandchild, gcRoot) {
		t.Fatal("a grandchild (two hops) must still be in root's tree")
	}
}

func TestProcessInTreeRefusesOutsiders(t *testing.T) {
	root := AnchorForPID(os.Getpid())
	if root.Start == 0 {
		t.Skip("no /proc or sysctl start time available on this platform")
	}
	// An independent process — no relation to this test's own tree — must
	// never match, no matter how long we'd walk.
	outsider := spawnSleeper(t)
	outsiderRoot := AnchorForPID(outsider)
	if processInTree(os.Getpid(), outsiderRoot) {
		t.Fatal("this test process is not a descendant of an unrelated sleeper")
	}
	// The inverse: root is not a descendant of its OWN child (ancestry is
	// directional).
	child := spawnSleeper(t)
	if processInTree(os.Getpid(), AnchorForPID(child)) {
		t.Fatal("a parent is not a descendant of its own child")
	}
}

func TestProcessInTreeRefusesOnPIDReuse(t *testing.T) {
	root := AnchorForPID(os.Getpid())
	if root.Start == 0 {
		t.Skip("no /proc or sysctl start time available on this platform")
	}
	// Simulate the kernel reusing root's pid for an unrelated process: same
	// PID, a start time that no longer matches what was recorded at bind
	// time. This must refuse even though the PID itself is an exact match —
	// the whole point of re-reading root's start time on every call.
	reused := ProcessAnchor{PID: root.PID, Start: root.Start + 1}
	if processInTree(os.Getpid(), reused) {
		t.Fatal("a pid whose start time moved on (reuse) must not match")
	}
}

func TestProcessInTreeEmptyAndInvalidInputsRefuse(t *testing.T) {
	if processInTree(os.Getpid(), ProcessAnchor{}) {
		t.Fatal("an empty (unset) root must never match")
	}
	if processInTree(0, AnchorForPID(os.Getpid())) {
		t.Fatal("pid 0 is never a valid peer")
	}
	if processInTree(-1, AnchorForPID(os.Getpid())) {
		t.Fatal("a negative pid is never a valid peer")
	}
}

// TestBrokerTreeBoundAuthorize drives the fix through the broker's public
// surface: MintSession binds a token to a known root immediately; a
// descendant of root authorizes, a process outside the tree (or with no
// peer credentials at all) is refused.
func TestBrokerTreeBoundAuthorize(t *testing.T) {
	b := NewBroker(func(string) (string, bool) { return "", false }, nil)
	// root is a SPAWNED process (not this test binary itself), so a sibling
	// process the test also spawns is a genuine outsider — not merely
	// another descendant of the test process by accident.
	rootPID, descendant := spawnGrandchild(t)
	root := AnchorForPID(rootPID)
	if root.Start == 0 {
		t.Skip("no /proc or sysctl start time available on this platform")
	}
	tok, err := b.MintSession(Identity{Agent: "fixer"}, root, SessionTTL)
	if err != nil {
		t.Fatalf("MintSession: %v", err)
	}
	// Root itself authorizes.
	if _, err := b.Authorize(tok, Peer{PID: rootPID, Valid: true}); err != nil {
		t.Fatalf("root must authorize: %v", err)
	}
	// A descendant of root (its own forked child) authorizes.
	if _, err := b.Authorize(tok, Peer{PID: descendant, Valid: true}); err != nil {
		t.Fatalf("a descendant of root must authorize: %v", err)
	}
	// A process outside root's tree — a SIBLING this test spawns independently,
	// not a descendant of root — is refused. This is the core of rt2 #12: it
	// used to succeed under same-uid binding (another sandbox, or any host
	// process at the daemon's uid, could present the token).
	outsider := spawnSleeper(t)
	if _, err := b.Authorize(tok, Peer{PID: outsider, Valid: true}); err == nil {
		t.Fatal("a process outside the bound tree must be refused")
	}
	// The inverse direction also refuses: this test process itself is
	// root's PARENT, not its descendant.
	if _, err := b.Authorize(tok, Peer{PID: os.Getpid(), Valid: true}); err == nil {
		t.Fatal("root's own parent is not in its tree")
	}
	// No peer credentials at all (a platform with no SO_PEERCRED, or a
	// transport that cannot supply them): refused, never a silent pass —
	// unlike the uid-bound/legacy path, a tree-bound token has nothing to
	// fall back to.
	if _, err := b.Authorize(tok, Peer{Valid: false}); err == nil {
		t.Fatal("a tree-bound token with no peer credentials must be refused")
	}
	// A PID present but unverified is refused on the Valid flag alone.
	if _, err := b.Authorize(tok, Peer{PID: rootPID, Valid: false}); err == nil {
		t.Fatal("a tree-bound token with an unverified peer PID must be refused")
	}
}

// TestBrokerTreeBoundPIDReuseRefused mints directly against a crafted
// ProcessAnchor (bypassing AnchorForPID) to deterministically simulate the
// kernel reusing root's pid after the real root process exited.
func TestBrokerTreeBoundPIDReuseRefused(t *testing.T) {
	b := NewBroker(func(string) (string, bool) { return "", false }, nil)
	real := AnchorForPID(os.Getpid())
	if real.Start == 0 {
		t.Skip("no /proc or sysctl start time available on this platform")
	}
	stale := ProcessAnchor{PID: real.PID, Start: real.Start + 999}
	tok, err := b.MintSession(Identity{Agent: "fixer"}, stale, SessionTTL)
	if err != nil {
		t.Fatalf("MintSession: %v", err)
	}
	if _, err := b.Authorize(tok, Peer{PID: os.Getpid(), Valid: true}); err == nil {
		t.Fatal("a reused pid (stale recorded start time) must be refused")
	}
}

// TestBrokerMintSessionRefusesEmptyRoot: a caller with no process to anchor
// to must use MintPendingSession or MintSessionUID instead — MintSession
// never silently falls back to an unbound or uid-wide token.
func TestBrokerMintSessionRefusesEmptyRoot(t *testing.T) {
	b := NewBroker(func(string) (string, bool) { return "", false }, nil)
	if _, err := b.MintSession(Identity{Agent: "fixer"}, ProcessAnchor{}, SessionTTL); err == nil {
		t.Fatal("MintSession with an empty root must be refused")
	}
}

// TestBrokerPendingSessionRefusesUntilBound: MintPendingSession's token
// authorizes NO ONE — not even the eventual root — until BindRoot names it.
// This is the fail-closed window the in-daemon launch path relies on: the
// token is written into the child's env before the child exists, so
// anything presenting it before BindRoot runs must be refused outright.
func TestBrokerPendingSessionRefusesUntilBound(t *testing.T) {
	b := NewBroker(func(string) (string, bool) { return "", false }, nil)
	// root is a SPAWNED process (not this test binary), so a sibling the
	// test also spawns is a genuine outsider, never a descendant by accident.
	rootPID := spawnSleeper(t)
	tok, err := b.MintPendingSession(Identity{Agent: "fixer"}, SessionTTL)
	if err != nil {
		t.Fatalf("MintPendingSession: %v", err)
	}
	if _, err := b.Authorize(tok, Peer{PID: rootPID, Valid: true}); err == nil {
		t.Fatal("a pending session must refuse even its eventual root before BindRoot")
	}
	if err := b.BindRoot(tok, rootPID); err != nil {
		t.Fatalf("BindRoot: %v", err)
	}
	if _, err := b.Authorize(tok, Peer{PID: rootPID, Valid: true}); err != nil {
		t.Fatalf("root must authorize once bound: %v", err)
	}
	// Binding twice is refused — one-shot.
	if err := b.BindRoot(tok, rootPID); err == nil {
		t.Fatal("BindRoot must be one-shot")
	}
	// An unrelated sibling process is still refused post-bind.
	outsider := spawnSleeper(t)
	if _, err := b.Authorize(tok, Peer{PID: outsider, Valid: true}); err == nil {
		t.Fatal("an outsider must be refused even after BindRoot")
	}
}

// TestBrokerBindRootRefusesUnknownOrNonPending covers BindRoot's own error
// paths: an unknown token, and a token that was never a pending tree binding
// in the first place (uid-bound, or already bound).
func TestBrokerBindRootRefusesUnknownOrNonPending(t *testing.T) {
	b := NewBroker(func(string) (string, bool) { return "", false }, nil)
	if err := b.BindRoot("never-minted", os.Getpid()); err == nil {
		t.Fatal("an unknown token must be refused")
	}
	uidTok, err := b.MintSessionUID(Identity{Agent: "x"}, 1000, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.BindRoot(uidTok, os.Getpid()); err == nil {
		t.Fatal("a uid-bound token is not a pending tree binding")
	}
}

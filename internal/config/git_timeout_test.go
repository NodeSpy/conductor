package config

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// blackHoleListener accepts TCP connections and never answers them — a
// black-holed git remote: the connection succeeds, but no bytes ever come
// back. Reproduces the condition a firewall that silently drops packets (or a
// credential helper stuck on a prompt despite GIT_TERMINAL_PROMPT=0) leaves a
// git subprocess in — hanging forever with no deadline.
func blackHoleListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and hold the connection open; never read or write. The
			// listener's own Close() (on test cleanup) is what ends this.
			t.Cleanup(func() { conn.Close() })
		}
	}()
	return ln.Addr().String()
}

// TestRunGitTimesOutOnBlackHole: a git operation against a connection that
// accepts but never answers must be bounded by gitCommandTimeout, not hang
// the calling goroutine (and whatever mutex/retry loop it's running under)
// forever.
func TestRunGitTimesOutOnBlackHole(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	old := gitCommandTimeout
	gitCommandTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitCommandTimeout = old })

	addr := blackHoleListener(t)
	url := fmt.Sprintf("git://%s/repo.git", addr)

	start := time.Now()
	_, err := RunGit("", "ls-remote", url)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want a timeout error against a black-holed connection, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timed-out error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunGit took %s — gitCommandTimeout did not bound it", elapsed)
	}
}

// TestRunGitStdoutTimesOutOnBlackHole is TestRunGitTimesOutOnBlackHole for the
// stdout-returning variant plugin fetches use to read object bytes.
func TestRunGitStdoutTimesOutOnBlackHole(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	old := gitCommandTimeout
	gitCommandTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitCommandTimeout = old })

	addr := blackHoleListener(t)
	url := fmt.Sprintf("git://%s/repo.git", addr)

	start := time.Now()
	_, err := RunGitStdout("", "ls-remote", url)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want a timeout error against a black-holed connection, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timed-out error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunGitStdout took %s — gitCommandTimeout did not bound it", elapsed)
	}
}

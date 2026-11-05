package gitwt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSSH routes the provisioner's ssh through a script that counts every
// connection and runs the remote's git-upload-pack locally — or, while
// <dir>/fail holds a count, refuses that many connections with the timeout
// an overloaded forge gives; "auth" refuses every one with a key rejection.
// It returns origin's ssh:// URL and the script's dir.
func fakeSSH(t *testing.T, origin string) (url, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := filepath.Join(dir, "fake-ssh")
	writeFile(t, script, `#!/bin/sh
dir=$(dirname "$0")
echo x >> "$dir/conns"
if [ -s "$dir/fail" ]; then
	case "$(cat "$dir/fail")" in
	auth) echo "git@github.com: Permission denied (publickey)." >&2; exit 255 ;;
	0) ;;
	*) echo $(($(cat "$dir/fail") - 1)) > "$dir/fail"
	   echo "ssh: connect to host github.com port 22: Connection timed out" >&2; exit 255 ;;
	esac
fi
for last; do :; done
exec sh -c "$last"
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	return "ssh://fakehost" + origin, dir
}

func sshConns(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "conns"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), "x")
}

func setFail(t *testing.T, dir, v string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "fail"), v+"\n")
}

// A fetch refused by a burst-overloaded forge (connect timeout) is retried
// with a backoff, so the checkout still lands.
func TestTransientFetchFailureIsRetried(t *testing.T) {
	origin := originRepo(t)
	url, sshDir := fakeSSH(t, origin)
	p := newProv(t, url)
	p.netBackoff = time.Millisecond
	ctx := context.Background()
	if _, err := p.baseClone(ctx, "acme/web"); err != nil {
		t.Fatalf("seed base clone: %v", err)
	}
	setFail(t, sshDir, "2")
	if _, _, err := p.ProvisionWorktree(ctx, prReq()); err != nil {
		t.Fatalf("ProvisionWorktree after 2 connect timeouts: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(sshDir, "fail")); strings.TrimSpace(string(b)) != "0" {
		t.Errorf("fake ssh still has %q refusals left; the timeouts were not retried", b)
	}
}

// An auth failure is not a network blip: it fails the checkout on the first
// try, with no retries.
func TestAuthFailureIsNotRetried(t *testing.T) {
	origin := originRepo(t)
	url, sshDir := fakeSSH(t, origin)
	p := newProv(t, url)
	p.netBackoff = time.Millisecond
	ctx := context.Background()
	if _, err := p.baseClone(ctx, "acme/web"); err != nil {
		t.Fatalf("seed base clone: %v", err)
	}
	setFail(t, sshDir, "auth")
	before := sshConns(t, sshDir)
	_, _, err := p.ProvisionWorktree(ctx, prReq())
	if err == nil {
		t.Fatal("ProvisionWorktree succeeded with every ssh connection refused")
	}
	if got := sshConns(t, sshDir) - before; got != 1 {
		t.Errorf("an auth failure made %d ssh connections, want 1 (no retry): %v", got, err)
	}
}

func TestTransientNetErr(t *testing.T) {
	for msg, want := range map[string]bool{
		"ssh: connect to host github.com port 22: Connection timed out\nfatal: Could not read from remote repository.": true,
		"kex_exchange_identification: read: Connection reset by peer\nfatal: Could not read from remote repository.":   true,
		"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.":                false,
		"ERROR: Repository not found.\nfatal: Could not read from remote repository.":                                  false,
		"fatal: couldn't find remote ref refs/pull/9/head":                                                             false,
	} {
		if got := transientNetErr(fmt.Errorf("git fetch: exit status 128: %s", msg)); got != want {
			t.Errorf("transientNetErr(%q) = %v, want %v", msg, got, want)
		}
	}
}

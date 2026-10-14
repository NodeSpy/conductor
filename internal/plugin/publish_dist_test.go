package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// scripts/publish-dist.sh and GitDist agree: what the release script
// publishes is exactly what conductor fetches.
func TestPublishDistScriptRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "publish-dist.sh")
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	run := func(dir string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	run("", "git", "init", "-q", "--bare", remote)
	work := t.TempDir()
	run(work, "git", "init", "-q", "-b", "main")
	run(work, "git", "remote", "add", "origin", remote)

	dist := t.TempDir()
	asset := "conductor-sentry_" + Platform()
	bin := []byte("sentry-binary")
	_ = os.WriteFile(filepath.Join(dist, asset), bin, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "conductor-sentry_plan9_386"), []byte("other"), 0o755)
	sum := sha256.Sum256(bin)
	other := sha256.Sum256([]byte("other"))
	_ = os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(
		hex.EncodeToString(sum[:])+"  "+asset+"\n"+hex.EncodeToString(other[:])+"  conductor-sentry_plan9_386\n"), 0o644)
	run(work, "bash", script, "connectors/sentry/v1.0.0", dist, "conductor-sentry")

	rs := RemoteSource{URL: "file://" + remote, Component: "connectors/sentry"}
	path, tag, _, verified, err := FetchRemoteVerified(rs, "", "", fixedCacheDir(t.TempDir()), GitDist{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if tag != "connectors/sentry/v1.0.0" || string(got) != "sentry-binary" || !verified {
		t.Fatalf("round trip: tag=%s bin=%q verified=%v", tag, got, verified)
	}
}

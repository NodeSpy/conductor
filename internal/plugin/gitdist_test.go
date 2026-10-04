package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// distRepo builds a git repository publishing tag's binary for each
// platform the way a release workflow does: one commit per platform on
// refs/dist/<tag>/<platform>, holding the binary and checksums.txt.
func distRepo(t *testing.T, component string, releases map[string]map[string][]byte) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "source")
	leaf := component[strings.LastIndex(component, "/")+1:]
	for tag, byPlat := range releases {
		for plat, bin := range byPlat {
			asset := "conductor-" + leaf + "_" + plat
			run("checkout", "-q", "--orphan", "dist-tmp")
			run("rm", "-rqf", "--ignore-unmatch", ".")
			sum := sha256.Sum256(bin)
			_ = os.WriteFile(filepath.Join(dir, asset), bin, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(hex.EncodeToString(sum[:])+"  "+asset+"\n"), 0o644)
			run("add", asset, "checksums.txt")
			run("commit", "-q", "-m", "dist "+tag+" "+plat)
			run("update-ref", DistRef(component+"/"+tag, plat), "HEAD")
			run("checkout", "-q", "main")
			run("branch", "-q", "-D", "dist-tmp")
		}
	}
	return "file://" + dir
}

// The whole fetch over plain git: only tags with THIS platform's binary are
// listed, the binary and checksums come from that platform's commit alone,
// and the result verifies.
func TestGitDistFetchesThisPlatformsBinary(t *testing.T) {
	here := Platform()
	url := distRepo(t, "connectors/sentry", map[string]map[string][]byte{
		"v1.0.0": {here: []byte("bin-1.0.0"), "plan9_386": []byte("other")},
		"v1.1.0": {here: []byte("bin-1.1.0")},
		"v2.0.0": {"plan9_386": []byte("not for this machine")},
	})
	rs := RemoteSource{URL: url, Component: "connectors/sentry"}
	g := GitDist{}
	tags, err := g.ListTags(rs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "connectors/sentry/v1.0.0,connectors/sentry/v1.1.0" {
		t.Fatalf("tags = %v: want only the releases published for %s", tags, here)
	}
	path, tag, sha, verified, err := FetchRemoteVerified(rs, "^1", "", fixedCacheDir(t.TempDir()), g)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	sum := sha256.Sum256([]byte("bin-1.1.0"))
	if tag != "connectors/sentry/v1.1.0" || string(b) != "bin-1.1.0" || sha != hex.EncodeToString(sum[:]) || !verified {
		t.Fatalf("fetched %q tag=%s sha=%s verified=%v", b, tag, sha, verified)
	}
}

// Transports that are not authenticated, or that run a local command as the
// "remote", are refused before git runs.
func TestGitDistRefusesUnsafeTransports(t *testing.T) {
	called := false
	g := GitDist{Git: func(string, ...string) (string, error) { called = true; return "", nil }}
	for _, u := range []string{"http://h/r", "git://h/r", "ext::sh -c id", "-upload-pack=x"} {
		if _, err := g.ListTags(RemoteSource{URL: u}); err == nil {
			t.Fatalf("%q was not refused", u)
		}
	}
	if called {
		t.Fatal("git ran for a refused transport")
	}
}

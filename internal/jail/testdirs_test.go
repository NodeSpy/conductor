//go:build unix

package jail

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// The jail tests' scratch dirs. They hold a fake operator home, a git remote,
// checkouts and confined runs' copy-on-write layers — hundreds of MB once a
// test has run terraform — so they must never be left behind, and never land
// in the real home.
//
// The base is neither /tmp (a host command's view replaces /tmp with the
// dispatch's own, so a workspace there would vanish from it) nor $HOME (the
// real home, or under the test-isolation harness a home inside /tmp):
// CONDUCTOR_JAILTEST_DIR, else /var/tmp.
//
// Each dir carries the test process's pid (owner.pid). t.Cleanup removes a
// fixture's dir on every path a test can end by — pass, fail, t.Fatal, a
// panic in the test — and TestMain removes the shared ones once the run ends;
// a run killed outright (a -timeout, SIGKILL) runs neither, so every new run
// first sweeps the dirs whose owner process is gone.
func jailTestBase() string {
	if d := os.Getenv("CONDUCTOR_JAILTEST_DIR"); d != "" {
		return d
	}
	return "/var/tmp"
}

var testDirs struct {
	mu    sync.Mutex
	dirs  []string
	swept bool
}

// newTestDir makes a fresh scratch dir under jailTestBase, removed by
// TestMain if nothing removes it sooner.
func newTestDir(prefix string) (string, error) {
	base := jailTestBase()
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	testDirs.mu.Lock()
	if !testDirs.swept {
		testDirs.swept = true
		sweepStaleTestDirs(base)
	}
	testDirs.mu.Unlock()
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	testDirs.mu.Lock()
	testDirs.dirs = append(testDirs.dirs, dir)
	testDirs.mu.Unlock()
	return dir, nil
}

// sweepStaleTestDirs removes the jail-test dirs whose owner process is gone
// (a killed run's), never one a live run still uses.
func sweepStaleTestDirs(base string) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "conductor-jailtest-") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "owner.pid"))
		if err != nil {
			continue // not ours to judge
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			removeScratch(dir)
		}
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	testDirs.mu.Lock()
	for _, d := range testDirs.dirs {
		removeScratch(d)
	}
	testDirs.mu.Unlock()
	os.Exit(code)
}

// A killed run's dirs (owner process gone) are swept; a live run's, and a
// dir that is not a jail test's, are left alone.
func TestSweepStaleTestDirs(t *testing.T) {
	base := t.TempDir()
	mk := func(name, pid string) string {
		d := filepath.Join(base, name)
		os.MkdirAll(filepath.Join(d, "home"), 0o700)
		if pid != "" {
			os.WriteFile(filepath.Join(d, "owner.pid"), []byte(pid), 0o600)
		}
		return d
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	dead := mk("conductor-jailtest-dead", strconv.Itoa(cmd.Process.Pid))
	live := mk("conductor-jailtest-live", strconv.Itoa(os.Getpid()))
	foreign := mk("conductor-jailtest-foreign", "")
	other := mk("something-else", strconv.Itoa(cmd.Process.Pid))
	sweepStaleTestDirs(base)
	for d, want := range map[string]bool{dead: false, live: true, foreign: true, other: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s: kept=%v, want %v", filepath.Base(d), err == nil, want)
		}
	}
}

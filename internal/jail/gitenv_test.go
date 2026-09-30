package jail

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// conductor's host git keeps where the operator's own config lives and drops
// what would inject config.
func TestScrubGitEnvKeepsTheOperatorsConfigLocation(t *testing.T) {
	got := strings.Join(scrubGitEnv([]string{
		"PATH=/usr/bin", "GIT_CONFIG_GLOBAL=/cfg/gitconfig", "GIT_CONFIG_SYSTEM=/etc/gc", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/x", "GIT_CONFIG_PARAMETERS='a.b=c'",
		"GIT_CONFIG=/x", "GIT_DIR=/x", "GIT_SSH_COMMAND=evil",
	}), "\n")
	for _, keep := range []string{"GIT_CONFIG_GLOBAL=/cfg/gitconfig", "GIT_CONFIG_SYSTEM=/etc/gc", "GIT_CONFIG_NOSYSTEM=1", "PATH="} {
		if !strings.Contains(got, keep) {
			t.Errorf("must keep %s:\n%s", keep, got)
		}
	}
	for _, gone := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG=", "GIT_DIR=", "GIT_SSH_COMMAND"} {
		if strings.Contains(got, gone) {
			t.Errorf("must drop %s:\n%s", gone, got)
		}
	}
}

// The signing configuration conductor reads (user.signingkey et al.) is the
// one the operator's GIT_CONFIG_GLOBAL names, not ~/.gitconfig.
func TestSigningConfigFollowsGitConfigGlobal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tsigningkey = /the/home/key.pub\n"), 0o600)
	cfg := filepath.Join(dir, "gitconfig")
	os.WriteFile(cfg, []byte("[user]\n\tsigningkey = /the/throwaway/key.pub\n"), 0o600)
	repo := filepath.Join(dir, "repo.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	if got := gitConfigGet(repo, "user.signingkey"); got != "/the/throwaway/key.pub" {
		t.Fatalf("signing key from %q, want GIT_CONFIG_GLOBAL's", got)
	}
}

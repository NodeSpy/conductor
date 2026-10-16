package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// The signer: sandbox.push's key, else git's own ssh signing key; with
// signing on and no key, pushes are refused; sign: false pushes unsigned.
func TestPushSigner(t *testing.T) {
	gc := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	os.WriteFile(gc, nil, 0o600)
	if _, err := pushSigner(&config.Config{}); err == nil || !strings.Contains(err.Error(), "no signing key") {
		t.Fatalf("signing on, no key: %v", err)
	}
	off := false
	if s, err := pushSigner(&config.Config{Sandbox: &config.SandboxConfig{Push: &config.SandboxPush{Sign: &off}}}); err != nil || s != nil {
		t.Fatalf("sign: false: %+v %v", s, err)
	}
	for _, kv := range [][2]string{{"user.name", "Op"}, {"user.email", "op@x"}, {"user.signingkey", "/k/id.pub"}} {
		exec.Command("git", "config", "--file", gc, kv[0], kv[1]).Run()
	}
	if _, err := pushSigner(&config.Config{}); err == nil {
		t.Fatal("a signing key is only git's when gpg.format is ssh")
	}
	exec.Command("git", "config", "--file", gc, "gpg.format", "ssh").Run()
	s, err := pushSigner(&config.Config{})
	if err != nil || s.Key != "/k/id.pub" || s.Name != "Op" || s.Email != "op@x" {
		t.Fatalf("git's own: %+v %v", s, err)
	}
	home, _ := os.UserHomeDir()
	s, err = pushSigner(&config.Config{Sandbox: &config.SandboxConfig{Push: &config.SandboxPush{SigningKey: "~/.ssh/c", Name: "N", Email: "e@x"}}})
	if err != nil || s.Key != filepath.Join(home, ".ssh/c") || s.Name != "N" || s.Email != "e@x" {
		t.Fatalf("sandbox.push wins: %+v %v", s, err)
	}
}

package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseRemoteSource(t *testing.T) {
	cases := []struct {
		src            string
		wantRepo, comp string
		ok             bool
	}{
		{"github.com/NodeSpy/conductor-plugins//sentry", "NodeSpy/conductor-plugins", "sentry", true},
		{"https://github.com/acme/conductor-jira", "acme/conductor-jira", "", true},
		{"./plugins/local", "", "", false},
		{"/abs/path", "", "", false},
		{"github.com/onlyowner", "", "", false},
	}
	for _, c := range cases {
		rs, ok := ParseRemoteSource(c.src)
		if ok != c.ok || rs.Repo != c.wantRepo || rs.Component != c.comp {
			t.Errorf("ParseRemoteSource(%q) = {%q %q} ok=%v, want {%q %q} ok=%v", c.src, rs.Repo, rs.Component, ok, c.wantRepo, c.comp, c.ok)
		}
	}
}

// stubAPI serves tags and a fixed binary; checksums.txt carries the real sha.
type stubAPI struct {
	tags      []string
	bin       []byte
	assetName string
	badSum    bool // publish a wrong checksum to force a mismatch
	tagsErr   bool // fail the tag listing, to exercise the degraded path
}

func (s stubAPI) ListTags(string) ([]string, error) {
	if s.tagsErr {
		return nil, errors.New("network unreachable")
	}
	return s.tags, nil
}
func (s stubAPI) Download(_, _, asset, destDir string) (string, error) {
	p := filepath.Join(destDir, asset)
	if asset == "checksums.txt" {
		sum := sha256.Sum256(s.bin)
		hexsum := hex.EncodeToString(sum[:])
		if s.badSum {
			hexsum = "deadbeef" + hexsum[8:]
		}
		return p, os.WriteFile(p, []byte(hexsum+"  "+s.assetName+"\n"), 0o644)
	}
	return p, os.WriteFile(p, s.bin, 0o755)
}

func TestFetchRemoteResolvesVerifiesCaches(t *testing.T) {
	rs := RemoteSource{Repo: "NodeSpy/conductor-plugins", Component: "sentry"}
	bin := []byte("#!/bin/sh\necho conductor-sentry\n")
	api := stubAPI{
		tags:      []string{"sentry/v1.0.0", "sentry/v1.1.0", "sentry/v2.0.0", "sentry/nightly"},
		bin:       bin,
		assetName: rs.AssetName(),
	}
	cache := t.TempDir()
	path, tag, sha, err := FetchRemote(rs, "~> 1.0", "", cache, api)
	if err != nil {
		t.Fatalf("FetchRemote: %v", err)
	}
	if tag != "sentry/v1.1.0" {
		t.Fatalf("resolved tag %q, want sentry/v1.1.0 (highest 1.x, not 2.0.0)", tag)
	}
	sum := sha256.Sum256(bin)
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("returned sha %q != binary sha", sha)
	}
	if got, _ := os.ReadFile(path); string(got) != string(bin) {
		t.Fatalf("cached binary content mismatch")
	}

	// A published checksum that doesn't match the binary is refused.
	if _, _, _, err := FetchRemote(rs, "~> 1.0", "", t.TempDir(), stubAPI{tags: api.tags, bin: bin, assetName: rs.AssetName(), badSum: true}); err == nil {
		t.Fatal("checksum mismatch must fail the fetch")
	}
	// A wrong config-pinned sha256 is refused even when the checksum is fine.
	if _, _, _, err := FetchRemote(rs, "~> 1.0", "0000", cache, api); err == nil {
		t.Fatal("sha256 pin mismatch must fail the fetch")
	}
}

// TestCopyExecutableReplacesRunningBinary reproduces the box condition that made
// every engine reconcile fail: the destination is a live, executing binary, so
// an in-place O_TRUNC write is refused with ETXTBSY. copyExecutable must still
// install the new build (via temp + rename), leaving no stray temp behind.
func TestCopyExecutableReplacesRunningBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ETXTBSY / rename-over-a-running-binary is unix-specific")
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary to stage as a running executable")
	}
	stage, err := os.ReadFile(sleepBin)
	if err != nil {
		t.Skipf("cannot read %s: %v", sleepBin, err)
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, "conductor-engine")
	v0 := filepath.Join(dir, "v0")
	if err := os.WriteFile(v0, stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(v0, dst); err != nil {
		t.Fatalf("initial install: %v", err)
	}

	// Start dst so the kernel holds its text segment busy, exactly like a loaded
	// engine plugin. sleep exits on its own; we kill it in cleanup regardless.
	cmd := exec.Command(dst, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start held binary: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	time.Sleep(100 * time.Millisecond) // let execve finish mapping the image

	// Prove the bug is real here: the old in-place write is refused.
	if err := os.WriteFile(dst, []byte("x"), 0o755); err == nil {
		t.Log("note: in-place overwrite unexpectedly succeeded (ETXTBSY not enforced on this platform)")
	} else if !errors.Is(err, syscall.ETXTBSY) {
		t.Logf("note: in-place overwrite failed with %v (not ETXTBSY)", err)
	}

	// The fix: atomic replace lands the new build even while dst is executing.
	want := append(append([]byte{}, stage...), "\n# v1\n"...)
	v1 := filepath.Join(dir, "v1")
	if err := os.WriteFile(v1, want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(v1, dst); err != nil {
		t.Fatalf("copyExecutable over a running binary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("dst not replaced: got %d bytes, want %d", len(got), len(want))
	}

	// No sibling temp file left behind (rename consumed it).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".conductor-engine") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

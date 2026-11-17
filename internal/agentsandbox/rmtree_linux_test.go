package agentsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A bind mount inside a home — the same filesystem, the same st_dev — is
// never entered: its source outside the home keeps its content.
func TestRemovalNeverEntersABindMountOfTheSameFilesystem(t *testing.T) {
	base := t.TempDir()
	src, home := filepath.Join(base, "src"), filepath.Join(base, "home")
	os.MkdirAll(src, 0o700)
	os.WriteFile(filepath.Join(src, "keep"), []byte("precious"), 0o600)
	mnt := filepath.Join(home, "sub", "mnt")
	os.MkdirAll(mnt, 0o700)
	if err := unix.Mount(src, mnt, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("no bind mount here (run under unshare --user --map-current-user --mount): %v", err)
	}
	t.Cleanup(func() { unix.Unmount(mnt, unix.MNT_DETACH) })
	if _, err := removeTree(home); !errors.Is(err, errMount) {
		t.Fatalf("removal: %v, want the mount refused", err)
	}
	if b, err := os.ReadFile(filepath.Join(src, "keep")); err != nil || string(b) != "precious" {
		t.Fatalf("the bind mount's source: %q %v", b, err)
	}
}

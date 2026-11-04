//go:build linux

package sandbox

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A file bind whose target an outer bind already shows — the gateway's
// socket under an fs: path, a FIFO — is mounted over as it is: never
// opened (a socket refuses with ENXIO, a FIFO would block the launch).
func TestTouchFileLeavesAnExistingInodeAlone(t *testing.T) {
	dir, err := os.MkdirTemp("", "touch") // short: a socket path is bounded
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "g.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sock, fifo} {
		if err := touchFile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	fresh := filepath.Join(dir, "new")
	if err := touchFile(fresh); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(fresh); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("a missing target is created as a file: %v %v", fi, err)
	}
}

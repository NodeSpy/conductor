//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A caller bind's read-only and noexec reach the mounts under it: a
// writable path with a mount inside it is noexec all the way down, so a
// plugin cannot run what it writes there. And a bind marked NoSymlink is
// opened and bound by fd, refusing a symlink anywhere in its path (not only
// the last component), so a directory swapped for a symlink after the grant
// was checked is never followed.
func TestBindAttributesAreRecursiveAndSymlinkSafe(t *testing.T) {
	if os.Getenv("BIND_ROLE") == "child" {
		os.Exit(bindAttrsChild())
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}
	cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "--pid", "--fork", "--mount-proc", "--kill-child", "--",
		os.Args[0], "-test.run=^TestBindAttributesAreRecursiveAndSymlinkSafe$")
	cmd.Env = append(os.Environ(), "BIND_ROLE=child", "BIND_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "BIND-OK") {
		t.Fatalf("%s", out)
	}
}

func bindAttrsChild() int {
	dir := os.Getenv("BIND_DIR")
	fail := func(f string, a ...any) int { fmt.Printf(f+"\n", a...); return 1 }
	r, w := filepath.Join(dir, "r"), filepath.Join(dir, "w")
	for _, d := range []string{r + "/sub", w + "/sub", dir + "/real/b", dir + "/via"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail("mkdir: %v", err)
		}
	}
	for _, d := range []string{r + "/sub", w + "/sub"} {
		if err := unix.Mount("tmpfs", d, "tmpfs", 0, "mode=0755"); err != nil {
			return fail("submount: %v", err)
		}
	}
	// An intermediate component that is a symlink now.
	if err := os.Symlink(dir+"/real", dir+"/via/a"); err != nil {
		return fail("symlink: %v", err)
	}
	_, err := buildJail([]BindMount{{Path: dir + "/via/a/b", NoSymlink: true}}, dir+"/root1", "", "")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		return fail("bind through a symlinked parent: %v", err)
	}
	if _, err := buildJail([]BindMount{
		{Path: r, RO: true, NoExec: true, NoSymlink: true},
		{Path: w, NoExec: true, NoSymlink: true},
	}, dir+"/root2", "", ""); err != nil {
		return fail("jail: %v", err)
	}
	want := map[string]int64{
		r + "/sub": unix.ST_RDONLY | unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV,
		w + "/sub": unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV,
		w:          unix.ST_NOEXEC | unix.ST_NOSUID | unix.ST_NODEV,
	}
	for p, flags := range want {
		var st unix.Statfs_t
		if err := unix.Statfs(p, &st); err != nil {
			return fail("statfs %s: %v", p, err)
		}
		if int64(st.Flags)&flags != flags {
			return fail("%s flags %#x, want %#x set", p, st.Flags, flags)
		}
	}
	fmt.Println("BIND-OK")
	return 0
}

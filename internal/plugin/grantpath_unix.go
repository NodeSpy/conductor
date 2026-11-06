//go:build unix

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// resolveOwned resolves p's symlinks, following only those root owns (a
// system layout: /lib -> usr/lib, /home -> /var/home, macOS /var ->
// private/var). A symlink anyone else owns is refused: conductor's own user
// owns it, so a plugin running as that user, or another plugin writing
// there, could have pointed it anywhere.
func resolveOwned(p string) (string, error) {
	cur := "/"
	rest := strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/")
	for hops := 0; len(rest) > 0; {
		name := rest[0]
		rest = rest[1:]
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, name)
		fi, err := os.Lstat(next)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || !systemOwned(st.Uid) {
			tgt, _ := os.Readlink(next)
			return "", &symlinkError{link: next, target: tgt}
		}
		if hops++; hops > 40 {
			return "", fmt.Errorf("%s: too many symlinks", p)
		}
		tgt, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(tgt) {
			tgt = filepath.Join(cur, tgt)
		}
		rest = append(strings.Split(strings.TrimPrefix(filepath.Clean(tgt), "/"), "/"), rest...)
		cur = "/"
	}
	return cur, nil
}

// overflowUID is the owner a user namespace shows for a file whose owner it
// does not map (Linux; root's files, when conductor runs in one).
var overflowUID = func() uint32 {
	b, err := os.ReadFile("/proc/sys/kernel/overflowuid")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}()

// systemOwned: uid is root, or an owner this user namespace does not map —
// neither is conductor's own user, which is the one a plugin runs as.
func systemOwned(uid uint32) bool {
	return uid == 0 || (overflowUID != 0 && uid == overflowUID && uid != uint32(os.Geteuid()))
}

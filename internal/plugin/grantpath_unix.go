//go:build unix

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
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
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
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

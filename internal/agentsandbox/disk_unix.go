//go:build linux || darwin

package agentsandbox

import "golang.org/x/sys/unix"

// diskSpace is the free (to an unprivileged user) and total bytes of p's
// filesystem.
func diskSpace(p string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}

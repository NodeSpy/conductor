//go:build unix

package keycache

import (
	"io/fs"
	"syscall"
)

// allocated is the disk a file takes: its allocated blocks.
func allocated(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return fi.Size()
}

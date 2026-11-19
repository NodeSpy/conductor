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

// fileID is a file's identity on disk (device, inode): two hard links to
// one file share it.
type fileID struct{ dev, ino uint64 }

// idOf is fi's identity and link count; ok false where the platform does
// not report them.
func idOf(fi fs.FileInfo) (id fileID, links uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, 1, false
	}
	return fileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, uint64(st.Nlink), true
}

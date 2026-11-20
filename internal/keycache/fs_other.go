//go:build !unix

package keycache

import "io/fs"

func allocated(fi fs.FileInfo) int64 { return fi.Size() }

type fileID struct{ dev, ino uint64 }

func idOf(fi fs.FileInfo) (id fileID, links uint64, ok bool) { return fileID{}, 1, false }

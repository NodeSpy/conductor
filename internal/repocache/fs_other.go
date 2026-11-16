//go:build !unix

package repocache

import "io/fs"

func allocated(fi fs.FileInfo) int64 { return fi.Size() }

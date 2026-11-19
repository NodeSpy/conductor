//go:build !unix

package keycache

import "io/fs"

func allocated(fi fs.FileInfo) int64 { return fi.Size() }

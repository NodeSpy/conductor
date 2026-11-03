package agentsandbox

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
)

const (
	lockfileMax      = 16 << 20
	lockfileTotalMax = 128 << 20
	lockfileDepth    = 3
	lockfileDirMax   = 2000
)

// lockfileSkip are directories never searched: dependency trees, build
// output and VCS state, which hold lockfiles of what is already installed.
var lockfileSkip = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "build": true, "dist": true,
	"_build": true, "deps": true, "venv": true, "__pycache__": true,
}

// SnapshotLockfiles reads ws's lockfiles — files whose base name matches
// one of patterns (path.Match), in ws and up to lockfileDepth directories
// below it — while ws is still conductor's checkout. Only regular files
// inside ws are read: symlinks (to files or directories) are skipped, and
// dot directories, dependency trees and build output are not searched.
// Keys are slash paths relative to ws.
func SnapshotLockfiles(ws string, patterns []string) (map[string][]byte, error) {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	out := map[string][]byte{}
	total, dirs := 0, 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if dirs++; dirs > lockfileDirMax {
			return nil
		}
		f, err := root.Open(filepath.FromSlash(dir))
		if err != nil {
			return nil
		}
		ents, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return nil
		}
		for _, e := range ents {
			name := e.Name()
			rel := path.Join(dir, name)
			switch {
			case e.Type()&os.ModeSymlink != 0:
			case e.IsDir():
				if depth < lockfileDepth && name[0] != '.' && !lockfileSkip[name] {
					if err := walk(rel, depth+1); err != nil {
						return err
					}
				}
			case e.Type().IsRegular() && matchAny(patterns, name):
				fi, err := root.Lstat(filepath.FromSlash(rel))
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				if fi.Size() > lockfileMax {
					return fmt.Errorf("agentsandbox: %s is %d bytes, over %d", rel, fi.Size(), lockfileMax)
				}
				b, err := root.ReadFile(filepath.FromSlash(rel))
				if err != nil {
					continue
				}
				if total += len(b); total > lockfileTotalMax {
					return fmt.Errorf("agentsandbox: %s's lockfiles are over %d bytes", ws, lockfileTotalMax)
				}
				out[rel] = b
			}
		}
		return nil
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	return out, nil
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

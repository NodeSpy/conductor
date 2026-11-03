package agentsandbox

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"
)

// PackageAccess is what a launch needs to reach conductor's package proxy.
type PackageAccess struct {
	// Addr is the proxy's host-loopback address, relayed into the sandbox
	// like a local model endpoint ("": no proxy).
	Addr string
	// Env points the agent's package managers at it, with a token naming
	// the identity and dispatch that ask. "${HOME}" in a value is the
	// identity home (a JVM takes its user.home from the password database,
	// not $HOME, so Java tools are told it).
	Env []string
	// HomeFiles are package-manager config files (slash paths relative to
	// the identity home) for the tools that read their registry only from
	// a file; their token names the identity alone, since the home outlives
	// one dispatch.
	HomeFiles map[string]string
}

// WriteHomeFiles writes files into home, every launch (an agent may have
// edited them; they are conductor's). Each is written inside an os.Root of
// home — a symlink an agent planted cannot redirect the write out of it —
// to a temp file renamed over the old, and left alone when unchanged.
func WriteHomeFiles(home string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	for rel, content := range files {
		p := filepath.FromSlash(rel)
		if !filepath.IsLocal(p) || path.Clean(rel) != rel {
			return fmt.Errorf("home file %q is not a plain path inside the home", rel)
		}
		if b, err := root.ReadFile(p); err == nil && bytes.Equal(b, []byte(content)) {
			if fi, err := root.Lstat(p); err == nil && fi.Mode().IsRegular() {
				continue
			}
		}
		if d := filepath.Dir(p); d != "." {
			if err := root.MkdirAll(d, 0o700); err != nil {
				return err
			}
		}
		tmp := p + ".conductor-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = root.Rename(tmp, p)
		}
		if werr != nil {
			_ = root.Remove(tmp)
			return werr
		}
	}
	return nil
}

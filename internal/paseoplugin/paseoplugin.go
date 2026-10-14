// Package paseoplugin is the paseo plugin conductor ships: it registers
// paseo's `conductor` provider, whose ACP stream is conductor's paseo
// provider socket (internal/controller/paseorelay.go). The plugin is
// embedded in the conductor binary, so the plugin paseo runs is always the
// one this conductor speaks to: Install writes it under conductor's state
// and has paseo load it from there, offline, idempotently.
package paseoplugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed plugin
var files embed.FS

// ID is the plugin's paseo id, and its provider's.
const ID = "conductor"

// Options configure Install.
type Options struct {
	// Dir is where the plugin is written (conductor's state:
	// <state>/paseo-plugin). paseo loads it from there in place.
	Dir string
	// Socket is the paseo provider socket the plugin dials.
	Socket string
	// PaseoBin is the paseo CLI ("" = "paseo" on PATH).
	PaseoBin string
	// PaseoHome selects a paseo daemon home (`--home`); "" = paseo's default.
	PaseoHome string
}

// Result reports what Install did.
type Result struct {
	Dir     string
	Version string // the plugin content's hash
	Action  string // installed | reloaded | unchanged
}

// Contents are the plugin's files for socket: the embedded source plus
// endpoint.json, keyed by relative path.
func Contents(socket string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(files, "plugin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, "plugin/")] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	ep, err := json.MarshalIndent(map[string]string{"socket": socket}, "", "  ")
	if err != nil {
		return nil, err
	}
	out["server/endpoint.json"] = append(ep, '\n')
	return out, nil
}

// Version is the content hash of the plugin as written for socket.
func Version(contents map[string][]byte) string {
	names := make([]string, 0, len(contents))
	for n := range contents {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(contents[n]))
		h.Write(contents[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// write puts contents in dir, replacing what is there, and reports whether
// anything changed.
func write(dir string, contents map[string][]byte) (bool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	changed := false
	for n, b := range contents {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return false, err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return false, err
		}
		if err := os.Rename(tmp, p); err != nil {
			return false, err
		}
		changed = true
	}
	// Files a previous version had and this one does not.
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if _, ok := contents[filepath.ToSlash(rel)]; !ok {
			changed = true
			return os.Remove(p)
		}
		return nil
	})
	return changed, err
}

// Install writes the plugin to opt.Dir and makes paseo load it:
//   - not configured in paseo yet: `paseo plugin install <dir>`;
//   - configured from opt.Dir: `paseo plugin reload conductor` when the
//     files changed (a new conductor), nothing otherwise;
//   - configured from somewhere else: an error — that is the operator's
//     plugin, not this one; conductor does not replace it.
//
// It needs the paseo daemon (of opt.PaseoHome) running; it never touches
// the network.
func Install(ctx context.Context, opt Options) (Result, error) {
	if opt.Dir == "" || opt.Socket == "" {
		return Result{}, errors.New("paseoplugin: Dir and Socket are required")
	}
	dir, err := filepath.Abs(opt.Dir)
	if err != nil {
		return Result{}, err
	}
	contents, err := Contents(opt.Socket)
	if err != nil {
		return Result{}, err
	}
	res := Result{Dir: dir, Version: Version(contents)}
	changed, err := write(dir, contents)
	if err != nil {
		return res, fmt.Errorf("paseoplugin: write %s: %w", dir, err)
	}
	configured, path, err := status(ctx, opt)
	if err != nil {
		return res, err
	}
	switch {
	case !configured:
		if _, err := paseo(ctx, opt, "plugin", "install", dir, "--json"); err != nil {
			return res, err
		}
		res.Action = "installed"
	case !samePath(path, dir):
		return res, fmt.Errorf("paseo already has a plugin %q, from %s — not conductor's (%s); remove it (`paseo plugin remove %s`) to install conductor's", ID, path, dir, ID)
	case changed:
		if _, err := paseo(ctx, opt, "plugin", "reload", ID, "--json"); err != nil {
			return res, err
		}
		res.Action = "reloaded"
	default:
		res.Action = "unchanged"
	}
	return res, nil
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// status reports whether paseo has a plugin with conductor's id configured,
// and the directory it loads it from.
func status(ctx context.Context, opt Options) (bool, string, error) {
	out, err := paseo(ctx, opt, "plugin", "ls", "--json")
	if err != nil {
		return false, "", err
	}
	var items []map[string]any
	if err := json.Unmarshal(out, &items); err != nil {
		var one map[string]any
		if json.Unmarshal(out, &one) != nil {
			return false, "", fmt.Errorf("paseo plugin ls: unexpected output: %.200s", out)
		}
		items = []map[string]any{one}
	}
	for _, it := range items {
		if id, _ := it["id"].(string); id != ID {
			continue
		}
		for _, k := range []string{"path", "directory", "source"} {
			if p, _ := it[k].(string); filepath.IsAbs(p) {
				return true, p, nil
			}
		}
		return true, "", nil
	}
	return false, "", nil
}

// paseo runs the paseo CLI and returns its stdout.
func paseo(ctx context.Context, opt Options, args ...string) ([]byte, error) {
	bin := opt.PaseoBin
	if bin == "" {
		bin = "paseo"
	}
	if opt.PaseoHome != "" {
		args = append(args, "--home", opt.PaseoHome)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("paseo %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

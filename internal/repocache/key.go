package repocache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"text/template"
)

// Key templates are text/template with three functions and nothing else:
//
//	{{hash "go.sum" "**/go.sum"}}  sha-256 over the files the globs match
//	{{os}} {{arch}}                the platform the run is on
//
// They render over a checkout: the run's (to pick the cache it restores)
// or the default branch's (to name the cache a warm publishes). A key
// selects among caches conductor itself published; it never writes one.

var keyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+=-]{0,199}$`)

// maxHashWalk bounds the entries one {{hash}} visits.
const maxHashWalk = 200000

// maxHashFile bounds one hashed file's size.
const maxHashFile = 256 << 20

func templateFuncs(hash func(...string) (string, error)) template.FuncMap {
	return template.FuncMap{
		"hash": hash,
		"os":   func() string { return runtime.GOOS },
		"arch": func() string { return runtime.GOARCH },
	}
}

// checkTemplate parses a key template without rendering it.
func checkTemplate(s string) error {
	_, err := template.New("key").Option("missingkey=error").Funcs(templateFuncs(nil)).Parse(s)
	return err
}

// RenderKey renders a key template over the checkout at root. The result
// must be a plain key ([A-Za-z0-9._+=-], up to 200 characters).
func RenderKey(tmpl, root string) (string, error) {
	return renderKey(tmpl, root, false)
}

// renderPrefix renders a restore-key template: a prefix, which may be
// empty only when the template is.
func renderPrefix(tmpl, root string) (string, error) {
	return renderKey(tmpl, root, true)
}

func renderKey(tmpl, root string, prefix bool) (string, error) {
	t, err := template.New("key").Option("missingkey=error").Funcs(templateFuncs(func(globs ...string) (string, error) {
		return HashFiles(root, globs...)
	})).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&limitWriter{w: &b, n: 512}, nil); err != nil {
		return "", err
	}
	k := strings.TrimSpace(b.String())
	if !keyRE.MatchString(k) {
		if prefix && k == "" {
			return "", errors.New("restore key renders empty")
		}
		return "", fmt.Errorf("key %q: must be [A-Za-z0-9._+=-], up to 200 characters, not starting with a symbol", k)
	}
	return k, nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errors.New("key too long")
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// HashFiles is the sha-256 (first 32 hex digits) over every regular file
// under root a glob matches — its slash path and its content, in path
// order. "**" matches any number of directories. Symlinks are never
// followed, .git and node_modules never walked. No match hashes to "".
func HashFiles(root string, globs ...string) (string, error) {
	for _, g := range globs {
		if g == "" || strings.HasPrefix(g, "/") || strings.Contains(g, "..") {
			return "", fmt.Errorf("hash %q: a pattern is relative and stays in the checkout", g)
		}
		if _, err := path.Match(strings.ReplaceAll(g, "**", "x"), ""); err != nil {
			return "", fmt.Errorf("hash %q: %w", g, err)
		}
	}
	files, err := matchFiles(root, globs)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	// Opened through an os.Root: a file replaced by a symlink out of the
	// checkout since it was listed is refused, never read.
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	for _, rel := range files {
		f, err := r.Open(filepath.FromSlash(rel))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", rel)
		_, err = io.Copy(h, io.LimitReader(f, maxHashFile))
		f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

// matchFiles lists root's regular files (slash paths, sorted) any glob
// matches.
func matchFiles(root string, globs []string) ([]string, error) {
	deep := false
	for _, g := range globs {
		if strings.Contains(g, "/") || strings.Contains(g, "**") {
			deep = true
		}
	}
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		for _, g := range globs {
			if globMatch(g, rel) && !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
				return
			}
		}
	}
	if !deep {
		ents, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if e.Type().IsRegular() {
				add(e.Name())
			}
		}
		sort.Strings(out)
		return out, nil
	}
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if n++; n > maxHashWalk {
			return fmt.Errorf("hash: more than %d entries under %s", maxHashWalk, root)
		}
		if d.IsDir() {
			if p != root && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // a symlink is never followed
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		add(filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// globMatch matches a slash path against a glob whose "**" segment matches
// any number of directories.
func globMatch(g, p string) bool {
	return matchSegs(strings.Split(g, "/"), strings.Split(p, "/"))
}

func matchSegs(g, p []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			for i := 0; i <= len(p); i++ {
				if matchSegs(g[1:], p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, _ := path.Match(g[0], p[0]); !ok {
			return false
		}
		g, p = g[1:], p[1:]
	}
	return len(p) == 0
}

// DetectIn reports whether any of the detect globs matches a file in the
// checkout at root.
func DetectIn(root string, detect []string) bool {
	if len(detect) == 0 {
		return false
	}
	files, err := matchFiles(root, detect)
	return err == nil && len(files) > 0
}

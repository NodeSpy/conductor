package browser

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// The workspace is the only place the browser's files touch the agent's
// world: screenshots and downloads are written there, uploads read from
// there — by conductor, through an os.Root, so no path (absolute, "..", or a
// symlink the agent planted) leads outside it. The browser itself never
// sees the workspace: uploads are copied into its private transfer
// directory, and downloads moved out of it.

// OutDir is where downloads and default screenshots land in the workspace.
const OutDir = ".browser"

var errOutside = errors.New("is outside the workspace")

// checkRel refuses a workspace path that is not plainly inside it, or that
// names a git directory or one of the workspace's carve-outs (deny: a
// read-only or hidden path inside it).
func checkRel(ws, rel string, deny []string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("an empty path")
	}
	if filepath.IsAbs(rel) {
		r, err := filepath.Rel(ws, rel)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s %w", rel, errOutside)
		}
		rel = r
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %w", rel, errOutside)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return "", fmt.Errorf("%s is inside a .git directory", rel)
		}
	}
	full := filepath.Join(ws, rel)
	for _, d := range deny {
		if full == d || strings.HasPrefix(full, d+string(filepath.Separator)) {
			return "", fmt.Errorf("%s is not open to the browser", rel)
		}
	}
	return rel, nil
}

// readWorkspace copies a workspace file (at most max bytes) to dst.
func readWorkspace(ws, rel string, deny []string, max int64, dst string) (int64, error) {
	rel, err := checkRel(ws, rel, deny)
	if err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", rel, unwrapRoot(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", rel)
	}
	if fi.Size() > max {
		return 0, fmt.Errorf("%s is %d bytes, over the upload limit (%d)", rel, fi.Size(), max)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(f, max+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("%s grew past the upload limit", rel)
	}
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return n, nil
}

// writeWorkspace writes r to a new file at rel (a free name next to it if
// rel is taken — nothing is ever overwritten), at most max bytes, and
// returns the workspace path it landed at.
func writeWorkspace(ws, rel string, deny []string, r io.Reader, max int64) (string, error) {
	rel, err := checkRel(ws, rel, deny)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("%s: %w", dir, unwrapRoot(err))
		}
	}
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	var f *os.File
	name := rel
	for i := 1; ; i++ {
		f, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i > 999 {
			return "", fmt.Errorf("%s: %w", name, unwrapRoot(err))
		}
		name = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("over the size limit (%d bytes)", max)
	}
	if err != nil {
		_ = root.Remove(name)
		return "", err
	}
	return filepath.ToSlash(name), nil
}

// unwrapRoot turns os.Root's escape error into a plain one.
func unwrapRoot(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return errOutside
	}
	return err
}

// safeName is a downloaded file's name as conductor will write it: its base
// name, printable, no leading dots, bounded.
func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == 0 || unicode.IsControl(r):
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-+=,() ", r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if s == "" {
		s = "download"
	}
	if len(s) > 120 {
		ext := filepath.Ext(s)
		if len(ext) > 16 {
			ext = ""
		}
		s = s[:120-len(ext)] + ext
	}
	return s
}

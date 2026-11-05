package blob

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// tarOf archives the tree under root the way a verb producing a workspace
// artifact would: directories, regular files and symlinks, in walk order.
func tarOf(t *testing.T, root string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(fi.Mode().Perm())}
		switch {
		case fi.IsDir():
			hdr.Typeflag, hdr.Name = tar.TypeDir, hdr.Name+"/"
		case fi.Mode()&fs.ModeSymlink != 0:
			hdr.Typeflag = tar.TypeSymlink
			if hdr.Linkname, err = os.Readlink(p); err != nil {
				return err
			}
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, fi.Size()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A directory's archive unpacks whole: files, nested dirs, the exec bit, an
// empty dir, and symlinks — kept as symlinks, not followed or copied.
func TestUntarUnpacksADirectory(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"README.md":          "top\n",
		"pkg/a.go":           "package pkg\n",
		".git/HEAD":          "ref: refs/heads/main\n",
		".git/objects/ab/cd": "obj",
	})
	if err := os.Chmod(filepath.Join(src, "pkg/a.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pkg/a.go", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../README.md", filepath.Join(src, "pkg", "up")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := Untar(bytes.NewReader(tarOf(t, src)), dst); err != nil {
		t.Fatalf("Untar: %v", err)
	}
	for name, want := range map[string]string{"README.md": "top\n", "pkg/a.go": "package pkg\n", ".git/HEAD": "ref: refs/heads/main\n", ".git/objects/ab/cd": "obj"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dst, "pkg/a.go")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("exec bit lost: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "empty")); err != nil || !fi.IsDir() {
		t.Errorf("empty dir lost: %v", err)
	}
	for link, target := range map[string]string{"link": "pkg/a.go", "pkg/up": "../README.md"} {
		got, err := os.Readlink(filepath.Join(dst, link))
		if err != nil || got != target {
			t.Errorf("%s: readlink = %q, %v; want the symlink kept as %q", link, got, err, target)
		}
	}
}

// An archive is untrusted whoever made it: an entry or link
// that escapes, a hard link or a device fails the unpack and leaves nothing.
func TestUntarRefusesHostileArchives(t *testing.T) {
	type ent struct {
		name, link string
		typ        byte
	}
	for name, ents := range map[string][]ent{
		"dotdot entry":   {{name: "../evil", typ: tar.TypeReg}},
		"absolute entry": {{name: "/tmp/evil", typ: tar.TypeReg}},
		"absolute link":  {{name: "l", link: "/etc", typ: tar.TypeSymlink}},
		"climbing link":  {{name: "l", link: "../..", typ: tar.TypeSymlink}},
		// each link stays inside on its own; together a -> b/.. -> ./.. escapes
		"link chain":     {{name: "a", link: "b/..", typ: tar.TypeSymlink}, {name: "b", link: ".", typ: tar.TypeSymlink}},
		"write via link": {{name: "l", link: ".", typ: tar.TypeSymlink}, {name: "l/../../evil", typ: tar.TypeReg}},
		"hard link":      {{name: "f", typ: tar.TypeReg}, {name: "h", link: "f", typ: tar.TypeLink}},
		"char device":    {{name: "null", typ: tar.TypeChar}},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, e := range ents {
				if err := tw.WriteHeader(&tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o644}); err != nil {
					t.Fatal(err)
				}
			}
			tw.Close()
			parent := t.TempDir()
			dst := filepath.Join(parent, "out")
			if err := Untar(&buf, dst); err == nil {
				t.Fatal("Untar accepted a hostile archive")
			}
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				t.Errorf("a failed unpack left %s behind", dst)
			}
			if ents, _ := os.ReadDir(parent); len(ents) != 0 {
				t.Errorf("a failed unpack wrote beside its destination: %v", ents)
			}
		})
	}
}

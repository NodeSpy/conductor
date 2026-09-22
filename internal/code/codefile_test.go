package code

import (
	"os"
	"strings"
	"testing"
)

func TestResolveCodeFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/script.py"
	if err := os.WriteFile(path, []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// file: prefix loads the file's contents
	got, err := resolveCodeFile("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "print('hi')\n" {
		t.Fatalf("got %q", got)
	}

	// leading/trailing whitespace around the reference is tolerated
	if got, err := resolveCodeFile("  file:" + path + "  "); err != nil || got != "print('hi')\n" {
		t.Fatalf("trimmed: got %q err %v", got, err)
	}
}

func TestResolveCodeFileInlineUnchanged(t *testing.T) {
	// no file: prefix → returned verbatim, even if it looks path-ish or is a
	// short command that could collide with a filename.
	for _, in := range []string{"echo hi", "keys", ".", "print('x')", "./not-a-file.py"} {
		if got, err := resolveCodeFile(in); err != nil || got != in {
			t.Fatalf("inline %q changed to %q (err %v)", in, got, err)
		}
	}
}

func TestResolveCodeFileMissing(t *testing.T) {
	if _, err := resolveCodeFile("file:/no/such/script.rb"); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("missing file should error, got %v", err)
	}
	if _, err := resolveCodeFile("file:"); err == nil {
		t.Fatal("empty file: path should error")
	}
}

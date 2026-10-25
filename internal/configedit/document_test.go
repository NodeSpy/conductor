package configedit

import (
	"strings"
	"testing"
)

const sample = `# conductor config
# a leading comment block

settings:
  concurrency: 4      # how many runs at once
  greeting: hello

dashboard:
  enabled: true
  port: 7788

# an unknown-to-this-binary section the editor must never touch
x-notes:
  author: platform-sre
  note: |
    multi-line
    scratch space

update:
  apply: true
  interval: 10m
`

func mustParse(t *testing.T, s string) *Document {
	t.Helper()
	d, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

// TestNoOpEmptyDiff is the first half of the hard test: staging a value back to
// its current value changes no bytes, so the diff is empty.
func TestNoOpEmptyDiff(t *testing.T) {
	d := mustParse(t, sample)
	if err := d.SetScalar([]string{"settings", "concurrency"}, 4); err != nil {
		t.Fatalf("set: %v", err)
	}
	if string(d.Bytes()) != sample {
		t.Fatalf("no-op edit changed the bytes:\n%s", string(d.Bytes()))
	}
	diff := ComputeDiff([]byte(sample), d.Bytes())
	if diff.Changed || len(diff.Lines) != 0 {
		t.Fatalf("no-op edit produced a diff: %+v", diff)
	}
}

// TestNoOpAcrossScalarSpelling treats true==true and 8==8.0 as no-ops (yaml.v3
// follows the YAML 1.2 core schema, so `yes` is the string "yes", not a bool —
// only genuinely equal values collapse).
func TestNoOpAcrossScalarSpelling(t *testing.T) {
	src := "a:\n  flag: true\n  n: 8\n"
	d := mustParse(t, src)
	if err := d.SetScalar([]string{"a", "flag"}, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetScalar([]string{"a", "n"}, 8.0); err != nil {
		t.Fatal(err)
	}
	if string(d.Bytes()) != src {
		t.Fatalf("spelling no-op changed bytes:\n%s", d.Bytes())
	}
}

// TestOneValueOneLineDiff is the second half: a single value change rewrites
// exactly one line, preserving its inline comment, and the diff is one line.
func TestOneValueOneLineDiff(t *testing.T) {
	d := mustParse(t, sample)
	if err := d.SetScalar([]string{"settings", "concurrency"}, 8); err != nil {
		t.Fatalf("set: %v", err)
	}
	diff := ComputeDiff([]byte(sample), d.Bytes())
	if diff.Added != 1 || diff.Removed != 1 {
		t.Fatalf("expected a one-line change, got +%d -%d\n%s", diff.Added, diff.Removed, diff.Unified)
	}
	// The inline comment must survive the splice.
	if !strings.Contains(string(d.Bytes()), "concurrency: 8      # how many runs at once") {
		t.Fatalf("inline comment not preserved:\n%s", d.Bytes())
	}
	// Exactly the one line differs.
	beforeLines := strings.Split(sample, "\n")
	afterLines := strings.Split(string(d.Bytes()), "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("line count changed: %d -> %d", len(beforeLines), len(afterLines))
	}
	changed := 0
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("expected exactly 1 line changed, got %d", changed)
	}
}

// TestCommentsAndUnknownSectionsPreserved is the third half: everything the
// edit did not touch — comments, blank lines, the x- section, the block scalar —
// stays byte-for-byte.
func TestCommentsAndUnknownSectionsPreserved(t *testing.T) {
	d := mustParse(t, sample)
	if err := d.SetScalar([]string{"dashboard", "port"}, 9000); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := string(d.Bytes())
	for _, must := range []string{
		"# conductor config",
		"# a leading comment block",
		"# an unknown-to-this-binary section the editor must never touch",
		"x-notes:",
		"  author: platform-sre",
		"    multi-line",
		"    scratch space",
		"  concurrency: 4      # how many runs at once",
		"  port: 9000",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("lost %q after edit:\n%s", must, got)
		}
	}
	// Only the port line changed.
	if strings.Contains(got, "port: 7788") {
		t.Fatalf("port not updated:\n%s", got)
	}
}

func TestSetQuotedString(t *testing.T) {
	src := "a:\n  name: \"acme\"\n"
	d := mustParse(t, src)
	if err := d.SetScalar([]string{"a", "name"}, "beta corp"); err != nil {
		t.Fatal(err)
	}
	v, _ := d.Get([]string{"a", "name"})
	if v != "beta corp" {
		t.Fatalf("got %q", v)
	}
}

func TestInsertMissingKey(t *testing.T) {
	src := "settings:\n  concurrency: 4\n"
	d := mustParse(t, src)
	if err := d.SetScalar([]string{"settings", "greeting"}, "hi"); err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get([]string{"settings", "greeting"}); !ok || v != "hi" {
		t.Fatalf("greeting not added: %v %v\n%s", v, ok, d.Bytes())
	}
	// The existing line is untouched.
	if !strings.Contains(string(d.Bytes()), "concurrency: 4") {
		t.Fatalf("existing key disturbed:\n%s", d.Bytes())
	}
}

func TestMissingSectionErrors(t *testing.T) {
	d := mustParse(t, "settings:\n  concurrency: 4\n")
	if err := d.SetScalar([]string{"nope", "deep", "key"}, 1); err == nil {
		t.Fatal("expected error for a missing parent section")
	}
}

func TestSequenceIndexPath(t *testing.T) {
	src := "hosts:\n  list:\n    - alpha\n    - beta\n"
	d := mustParse(t, src)
	if v, ok := d.Get([]string{"hosts", "list", "1"}); !ok || v != "beta" {
		t.Fatalf("sequence get: %v %v", v, ok)
	}
	if err := d.SetScalar([]string{"hosts", "list", "0"}, "gamma"); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]string{"hosts", "list", "0"}); v != "gamma" {
		t.Fatalf("sequence set: %v", v)
	}
}

func TestDeleteKey(t *testing.T) {
	src := "a:\n  keep: 1\n  drop: 2\n  also: 3\n"
	d := mustParse(t, src)
	if err := d.Delete([]string{"a", "drop"}); err != nil {
		t.Fatal(err)
	}
	got := string(d.Bytes())
	if strings.Contains(got, "drop") {
		t.Fatalf("drop not removed:\n%s", got)
	}
	if !strings.Contains(got, "keep: 1") || !strings.Contains(got, "also: 3") {
		t.Fatalf("siblings disturbed:\n%s", got)
	}
}

func TestParseEmpty(t *testing.T) {
	d := mustParse(t, "")
	if err := d.SetScalar([]string{"a"}, 1); err == nil {
		t.Fatal("expected error setting into an empty document root")
	}
}

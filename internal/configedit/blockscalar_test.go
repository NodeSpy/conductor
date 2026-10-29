package configedit

import (
	"strings"
	"testing"
)

// The end of a literal (|) or folded (>) block was computed from its decoded
// value, one line short. Deleting such a key left its last line behind, which
// YAML then read as a continuation of the previous key's value.
func TestDeleteBlockScalarLeavesNoResidue(t *testing.T) {
	d := mustParse(t, sample)
	if err := d.Delete([]string{"x-notes", "note"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]string{"x-notes", "author"}); v != "platform-sre" {
		t.Fatalf("previous key's value changed to %q:\n%s", v, d.Bytes())
	}
	if strings.Contains(string(d.Bytes()), "scratch space") {
		t.Fatalf("block content left behind:\n%s", d.Bytes())
	}
	if v, _ := d.Get([]string{"update", "interval"}); v != "10m" {
		t.Fatalf("following section disturbed:\n%s", d.Bytes())
	}
}

func TestInsertAfterBlockScalar(t *testing.T) {
	d := mustParse(t, sample)
	mustSet(t, d, []string{"x-notes", "added"}, "v")
	if v, _ := d.Get([]string{"x-notes", "note"}); v != "multi-line\nscratch space\n" {
		t.Fatalf("block scalar changed to %q:\n%s", v, d.Bytes())
	}
}

func TestReplaceBlockScalar(t *testing.T) {
	for name, value := range map[string]any{
		"with a short string": "short",
		"with a map":          map[string]any{"a": 1},
	} {
		t.Run(name, func(t *testing.T) {
			d := mustParse(t, sample)
			mustSet(t, d, []string{"x-notes", "note"}, value)
			if strings.Contains(string(d.Bytes()), "scratch space") {
				t.Fatalf("old block content left behind:\n%s", d.Bytes())
			}
			if v, _ := d.Get([]string{"x-notes", "author"}); v != "platform-sre" {
				t.Fatalf("sibling disturbed:\n%s", d.Bytes())
			}
		})
	}
}

// Folded and keep-chomped blocks, and a block scalar as the last thing in a
// list item and in the file.
func TestBlockScalarStyles(t *testing.T) {
	src := "a:\n  folded: >\n    one\n    two\n\n  kept: |+\n    body\n\n\nb:\n  - |-\n    item\n  - plain\nc:\n  tail: |\n    end\n"
	d := mustParse(t, src)
	mustSet(t, d, []string{"a", "new"}, 1)
	if v, _ := d.Get([]string{"a", "kept"}); v != "body\n\n\n" {
		t.Fatalf("keep-chomped value changed to %q:\n%s", v, d.Bytes())
	}
	if v, _ := d.Get([]string{"a", "folded"}); v != "one two\n" {
		t.Fatalf("folded value changed to %q:\n%s", v, d.Bytes())
	}
	if err := d.Delete([]string{"b", "0"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]string{"b", "0"}); v != "plain" {
		t.Fatalf("list item delete left residue:\n%s", d.Bytes())
	}
	mustSet(t, d, []string{"c", "after"}, "x")
	if v, _ := d.Get([]string{"c", "tail"}); v != "end\n" {
		t.Fatalf("last block in file changed to %q:\n%s", v, d.Bytes())
	}
}

// Editing through an alias would rewrite the anchored section and silently
// change every other place that uses it.
func TestRefuseEditThroughAlias(t *testing.T) {
	src := "a: &shared\n  x: 1\nb: *shared\n"
	d := mustParse(t, src)
	for _, c := range []struct {
		path  []string
		value any
	}{
		{[]string{"b", "y"}, 2},
		{[]string{"b", "y", "z"}, 2},
	} {
		err := d.SetScalar(c.path, c.value)
		if err == nil || !strings.Contains(err.Error(), "alias") {
			t.Errorf("%v: want an alias refusal, got %v", c.path, err)
		}
	}
	if string(d.Bytes()) != src {
		t.Fatalf("text changed:\n%s", d.Bytes())
	}
}

func TestRefuseFlowStyleSaysWhy(t *testing.T) {
	d := mustParse(t, "operators: {ops: {ssh_key: k}}\n")
	err := d.SetScalar([]string{"operators", "mallory"}, map[string]any{"ssh_key": "m"})
	if err == nil || !strings.Contains(err.Error(), "flow style") {
		t.Fatalf("want a flow-style refusal, got %v", err)
	}
}

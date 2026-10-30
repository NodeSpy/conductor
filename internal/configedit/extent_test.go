package configedit

import (
	"strings"
	"testing"
)

// A value can continue onto following lines without being a | or > block: a
// wrapped plain or quoted string, or a flow list. Its entry ends where the
// indentation returns to the entry's own column; deleting less leaves the tail
// behind for YAML to read as part of the previous value.
const wrapped = `a:
  first: one
  long: this is a long
    wrapped value
  quoted: "a quoted
    string"
  flow: [x,
    y]
b: 2
`

func TestDeleteWrappedValuesLeavesNoResidue(t *testing.T) {
	for _, key := range []string{"long", "quoted", "flow"} {
		t.Run(key, func(t *testing.T) {
			d := mustParse(t, wrapped)
			if err := d.Delete([]string{"a", key}); err != nil {
				t.Fatal(err)
			}
			if v, _ := d.Get([]string{"a", "first"}); v != "one" {
				t.Fatalf("previous key's value changed to %q:\n%s", v, d.Bytes())
			}
			if _, ok := d.Get([]string{"a", key}); ok {
				t.Fatalf("%s still present:\n%s", key, d.Bytes())
			}
			if v, _ := d.Get([]string{"b"}); v != 2 {
				t.Fatalf("following section disturbed:\n%s", d.Bytes())
			}
		})
	}
}

func TestInsertAfterWrappedValue(t *testing.T) {
	d := mustParse(t, "a:\n  first: one\n  long: this is a long\n    wrapped value\nb: 2\n")
	mustSet(t, d, []string{"a", "new"}, "v")
	if v, _ := d.Get([]string{"a", "long"}); v != "this is a long wrapped value" {
		t.Fatalf("wrapped value changed to %q:\n%s", v, d.Bytes())
	}
}

func TestSetWrappedValue(t *testing.T) {
	d := mustParse(t, wrapped)
	mustSet(t, d, []string{"a", "long"}, "short")
	mustSet(t, d, []string{"a", "quoted"}, "q")
	mustSet(t, d, []string{"a", "flow"}, []any{"z"})
	if strings.Contains(string(d.Bytes()), "wrapped value") || strings.Contains(string(d.Bytes()), `string"`) {
		t.Fatalf("old continuation lines left behind:\n%s", d.Bytes())
	}
	if v, _ := d.Get([]string{"a", "first"}); v != "one" {
		t.Fatalf("sibling disturbed:\n%s", d.Bytes())
	}
}

// The first key of a list item shares its line with the "- " marker.
const items = `bind:
  - address: x   # first
    hostnames: [a]
  - address: y
  - only: z
after: 1
`

func TestDeleteFirstKeyOfListItem(t *testing.T) {
	d := mustParse(t, items)
	if err := d.Delete([]string{"bind", "0", "address"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get([]string{"bind", "0", "hostnames"}); !ok || !strings.Contains(strings.TrimSpace(strings.ReplaceAll(string(d.Bytes()), "\n", " ")), "- hostnames: [a]") {
		t.Fatalf("item lost its other key or its marker (%v):\n%s", v, d.Bytes())
	}
	if _, ok := d.Get([]string{"bind", "0", "address"}); ok {
		t.Fatalf("address still present:\n%s", d.Bytes())
	}
	if v, _ := d.Get([]string{"bind", "1", "address"}); v != "y" {
		t.Fatalf("next item disturbed:\n%s", d.Bytes())
	}
	// The only key of an item: the item stays (as an empty map), the list
	// keeps its length.
	if err := d.Delete([]string{"bind", "2", "only"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := d.Get([]string{"bind", "2"}); !ok || len(v.(map[string]any)) != 0 {
		t.Fatalf("emptied item = %#v:\n%s", v, d.Bytes())
	}
	if v, _ := d.Get([]string{"after"}); v != 1 {
		t.Fatalf("following section disturbed:\n%s", d.Bytes())
	}
}

func TestEditInsideListItem(t *testing.T) {
	d := mustParse(t, items)
	mustSet(t, d, []string{"bind", "0", "allow_plain_http"}, true)
	mustSet(t, d, []string{"bind", "0", "address"}, map[string]any{"host": "x"})
	mustSet(t, d, []string{"bind", "1", "hostnames"}, []any{"b"})
	if v, _ := d.Get([]string{"bind", "0", "hostnames"}); v == nil {
		t.Fatalf("sibling in the item lost:\n%s", d.Bytes())
	}
}

// Replacing an anchored section that an alias elsewhere still uses would
// break the alias: refused, file untouched.
func TestRefuseReplacingAnAnchorInUse(t *testing.T) {
	src := "a: &shared\n  x: 1\nb: *shared\n"
	d := mustParse(t, src)
	if err := d.SetScalar([]string{"a"}, map[string]any{"y": 2}); err == nil {
		t.Fatalf("replacing an anchor in use succeeded:\n%s", d.Bytes())
	}
	if string(d.Bytes()) != src {
		t.Fatalf("text changed:\n%s", d.Bytes())
	}
}

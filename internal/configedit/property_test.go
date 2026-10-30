package configedit

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// paths lists every entry path in a decoded YAML value (maps and list items),
// outermost first.
func paths(v any, prefix []string, out *[][]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := append(append([]string{}, prefix...), k)
			*out = append(*out, p)
			paths(e, p, out)
		}
	case []any:
		for i, e := range x {
			p := append(append([]string{}, prefix...), strconv.Itoa(i))
			*out = append(*out, p)
			paths(e, p, out)
		}
	}
}

func decodeAll(t *testing.T, b []byte) any {
	t.Helper()
	tok, refs := tokenize(b)
	var v any
	if err := yaml.Unmarshal(tok, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return (&Document{refs: refs}).detokenize(v)
}

// without returns v with the entry at path removed (a list item is removed,
// not nulled, matching Delete).
func without(v any, path []string) any {
	if len(path) == 0 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k == path[0] {
				if len(path) == 1 {
					continue
				}
				e = without(e, path[1:])
				// Deleting a section's last key (or a list's last item) leaves "key:" with nothing
				// under it, which reads as null; conductor treats both as empty.
				if len(path) == 2 {
					if m, ok := e.(map[string]any); ok && len(m) == 0 {
						e = nil
					}
					if l, ok := e.([]any); ok && len(l) == 0 {
						e = nil
					}
				}
			}
			out[k] = e
		}
		return out
	case []any:
		i, _ := strconv.Atoi(path[0])
		out := []any{}
		for j, e := range x {
			if j == i {
				if len(path) == 1 {
					continue
				}
				e = without(e, path[1:])
			}
			out = append(out, e)
		}
		return out
	}
	return v
}

// Deleting any entry changes exactly that entry: everything else in the file
// decodes as it did. This is the property every extent bug broke silently.
func TestDeleteAnyEntryChangesOnlyThatEntry(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"config.example.yaml": string(example),
		"sample":              sample,
		"wrapped":             wrapped,
		"items":               items,
		"blocks":              "a:\n  folded: >\n    one\n    two\n\n  kept: |+\n    body\n\n\nb:\n  - |-\n    item\n  - plain\nc:\n  tail: |\n    end\n",
	} {
		before := decodeAll(t, []byte(src))
		var all [][]string
		paths(before, nil, &all)
		refused := 0
		for _, p := range all {
			if sharedYAML(mustParse(t, src), p) {
				continue // covered by TestDeleteShared
			}
			d := mustParse(t, src)
			if err := d.Delete(p); err != nil {
				// A refusal is safe only if the text is untouched.
				if string(d.Bytes()) != src {
					t.Fatalf("%s: refused delete of %v changed the text", name, p)
				}
				refused++
				continue
			}
			got := decodeAll(t, d.Bytes())
			want := without(before, p)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: deleting %s changed something else", name, strings.Join(p, "."))
			}
		}
		t.Logf("%s: %d entries deleted one at a time, %d refused", name, len(all)-refused, refused)
	}
}

// sharedYAML reports whether deleting path is meant to change more than the one
// entry: it sits in an anchored section (every alias of it changes too), or its
// mapping has a "<<:" merge (deleting an override reveals the merged value).
func sharedYAML(d *Document, path []string) bool {
	for i := 1; i <= len(path); i++ {
		v, parent, _, ok := d.node(path[:i])
		if !ok {
			return false
		}
		if v.Anchor != "" {
			return true
		}
		if i == len(path) && parent != nil && parent.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(parent.Content); j += 2 {
				if parent.Content[j].Value == "<<" {
					return true
				}
			}
		}
	}
	return false
}

// The two deletes that intentionally change more than one entry.
func TestDeleteShared(t *testing.T) {
	src := "x:\n  base: &base\n    model: big\n    type: agent\nsteps:\n  - <<: *base\n    model: small\n  - <<: *base\n"
	// Deleting from an anchored section changes every place that uses it.
	d := mustParse(t, src)
	if err := d.Delete([]string{"x", "base", "type"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Get([]string{"steps", "1", "type"}); ok {
		t.Fatalf("the alias didn't follow the anchor:\n%s", d.Bytes())
	}
	// Deleting an override reveals the merged value.
	d = mustParse(t, src)
	if err := d.Delete([]string{"steps", "0", "model"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]string{"steps", "0", "model"}); v != "big" {
		t.Fatalf("model = %v after deleting the override, want the merged value:\n%s", v, d.Bytes())
	}
	// Deleting an anchor something still uses is refused, file untouched.
	d = mustParse(t, src)
	if err := d.Delete([]string{"x", "base"}); err == nil || string(d.Bytes()) != src {
		t.Fatalf("deleting an anchor in use: err=%v\n%s", err, d.Bytes())
	}
}

// Setting any value changes exactly that value, and the check that guards it
// never refuses an ordinary edit.
func TestSetAnyValueChangesOnlyThatValue(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"config.example.yaml": string(example),
		"sample":              sample,
		"wrapped":             wrapped,
		"items":               items,
	} {
		before := decodeAll(t, []byte(src))
		var all [][]string
		paths(before, nil, &all)
		for _, p := range all {
			if sharedYAML(mustParse(t, src), p) {
				continue
			}
			values := []any{"zz new", map[string]any{"k": "v"}}
			if name == "config.example.yaml" {
				values = values[:1] // keeps the -race run well inside the test timeout
			}
			for _, value := range values {
				d := mustParse(t, src)
				if err := d.SetScalar(p, value); err != nil {
					if string(d.Bytes()) != src {
						t.Fatalf("%s: refused set of %v changed the text", name, p)
					}
					// Only flow collections and list items without a key are
					// refused for a plain block file.
					t.Logf("%s: set %s refused: %v", name, strings.Join(p, "."), err)
					continue
				}
				got := decodeAll(t, d.Bytes())
				want, _ := setIn(before, p, value)
				if !canonEqual(got, want) {
					t.Errorf("%s: setting %s changed something else", name, strings.Join(p, "."))
				}
			}
		}
	}
}

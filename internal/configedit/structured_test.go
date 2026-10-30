package configedit

import (
	"reflect"
	"strings"
	"testing"
)

// mustSet sets path to value and asserts the result re-parses and reads back
// as value (structurally, the way the daemon will decode it).
func mustSet(t *testing.T, d *Document, path []string, value any) {
	t.Helper()
	if err := d.SetScalar(path, value); err != nil {
		t.Fatalf("set %v: %v\n%s", path, err, d.Bytes())
	}
	got, ok := d.Get(path)
	if !ok {
		t.Fatalf("%v missing after set:\n%s", path, d.Bytes())
	}
	if !reflect.DeepEqual(normalize(got), normalize(value)) {
		t.Fatalf("%v = %#v, want %#v\n%s", path, got, value, d.Bytes())
	}
}

// normalize maps JSON-decoded and YAML-decoded values onto one shape.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case float64:
		if x == float64(int(x)) {
			return int(x)
		}
	}
	return v
}

const opsConfig = `# operators who may sign in
operators:
  ops:
    ssh_key: ssh-ed25519 AAAA ops   # the on-call key

dashboard:
  enabled: true
`

// A single-key map used to render as one line ("ssh_key: ...") and get spliced
// in as "mallory: ssh_key: ..." — invalid YAML.
func TestAddMapWithOneKey(t *testing.T) {
	d := mustParse(t, opsConfig)
	mustSet(t, d, []string{"operators", "mallory"}, map[string]any{"ssh_key": "ssh-ed25519 AAAA m"})
	if !strings.Contains(string(d.Bytes()), "ssh_key: ssh-ed25519 AAAA ops   # the on-call key") {
		t.Fatalf("existing operator line disturbed:\n%s", d.Bytes())
	}
}

func TestAddMapWithSeveralKeysAndNesting(t *testing.T) {
	d := mustParse(t, opsConfig)
	mustSet(t, d, []string{"dashboard", "auth"}, map[string]any{
		"password_requires_totp": false,
		"stepup":                 []any{"passkey", "ssh"},
		"methods":                []any{"ssh"},
	})
	mustSet(t, d, []string{"dashboard", "bind"}, []any{
		map[string]any{"address": "0.0.0.0:7791", "hostnames": []any{"box.lan"}},
	})
	if v, _ := d.Get([]string{"dashboard", "enabled"}); v != true {
		t.Fatalf("sibling disturbed:\n%s", d.Bytes())
	}
}

// Setting a leaf under a section that doesn't exist yet creates the section(s).
func TestSetLeafCreatesMissingSections(t *testing.T) {
	d := mustParse(t, opsConfig)
	mustSet(t, d, []string{"dashboard", "auth", "password_requires_totp"}, false)
	mustSet(t, d, []string{"dashboard", "auth", "stepup"}, []any{"ssh", "totp"})
	mustSet(t, d, []string{"brand", "new", "deep"}, 3)
	if v, _ := d.Get([]string{"dashboard", "auth", "password_requires_totp"}); v != false {
		t.Fatalf("first leaf lost after second edit:\n%s", d.Bytes())
	}
}

// A null section ("auth:" with nothing under it) is a place to put a map.
func TestSetUnderNullSection(t *testing.T) {
	d := mustParse(t, "dashboard:\n  auth:\n  enabled: true\n")
	mustSet(t, d, []string{"dashboard", "auth", "methods"}, []any{"ssh"})
	if v, _ := d.Get([]string{"dashboard", "enabled"}); v != true {
		t.Fatalf("sibling disturbed:\n%s", d.Bytes())
	}
}

// Replacing an existing map/list rewrites only that block.
func TestReplaceExistingStructure(t *testing.T) {
	d := mustParse(t, opsConfig+"  auth:\n    stepup: [passkey]\n    methods:\n      - passkey\n\nupdate:\n  apply: true  # keep\n")
	mustSet(t, d, []string{"dashboard", "auth"}, map[string]any{"methods": []any{"ssh", "password"}})
	if _, ok := d.Get([]string{"dashboard", "auth", "stepup"}); ok {
		t.Fatalf("old key survived a whole-map replace:\n%s", d.Bytes())
	}
	mustSet(t, d, []string{"operators", "ops"}, map[string]any{"ssh_key": "ssh-ed25519 AAAA new"})
	mustSet(t, d, []string{"dashboard", "auth", "methods"}, []any{"passkey"})
	mustSet(t, d, []string{"dashboard", "enabled"}, map[string]any{"x": 1}) // scalar -> map
	mustSet(t, d, []string{"dashboard", "enabled"}, true)                   // map -> scalar
	if !strings.Contains(string(d.Bytes()), "apply: true  # keep") || !strings.HasPrefix(string(d.Bytes()), "# operators who may sign in\n") {
		t.Fatalf("text outside the edited blocks disturbed:\n%s", d.Bytes())
	}
}

// Staging a structure equal to what's there is a no-op: the text is untouched.
func TestStructuredNoOpEmptyDiff(t *testing.T) {
	src := opsConfig + "  auth:\n    methods: [ssh, password]   # flow style\n"
	d := mustParse(t, src)
	if err := d.SetScalar([]string{"dashboard", "auth"}, map[string]any{"methods": []any{"ssh", "password"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetScalar([]string{"operators"}, map[string]any{"ops": map[string]any{"ssh_key": "ssh-ed25519 AAAA ops"}}); err != nil {
		t.Fatal(err)
	}
	if string(d.Bytes()) != src {
		t.Fatalf("no-op structured set changed the text:\n%s", d.Bytes())
	}
}

// Empty containers and the top level.
func TestEmptyContainersAndTopLevel(t *testing.T) {
	d := mustParse(t, opsConfig)
	mustSet(t, d, []string{"dashboard", "auth"}, map[string]any{})
	mustSet(t, d, []string{"dashboard", "bind"}, []any{})
	mustSet(t, d, []string{"imports"}, []any{"extra.yaml"})
	mustSet(t, d, []string{"x-new"}, map[string]any{"a": map[string]any{"b": "c"}})
}

// Keys and strings that need quoting survive the round trip.
func TestStructuredQuoting(t *testing.T) {
	d := mustParse(t, opsConfig)
	mustSet(t, d, []string{"x", "weird"}, map[string]any{"yes": "no", "a: b": "#not a comment", "n": "007", "empty": ""})
}

// Still refused, never a corrupt file: flow-style parents, multi-line strings,
// and missing sequence indexes.
func TestStructuredRefusals(t *testing.T) {
	d := mustParse(t, "operators: {ops: {ssh_key: k}}\nhosts:\n  - a\n")
	before := string(d.Bytes())
	for _, c := range []struct {
		path  []string
		value any
	}{
		{[]string{"operators", "mallory"}, map[string]any{"ssh_key": "m"}},
		{[]string{"hosts", "5", "name"}, "x"},
		{[]string{"note"}, "line one\nline two"},
	} {
		if err := d.SetScalar(c.path, c.value); err == nil {
			t.Errorf("%v: expected a refusal, got:\n%s", c.path, d.Bytes())
		}
		if string(d.Bytes()) != before {
			t.Fatalf("%v: a refused edit changed the text:\n%s", c.path, d.Bytes())
		}
	}
}

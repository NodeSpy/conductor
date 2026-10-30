package configedit

import (
	"strings"
	"testing"
)

// ${NAME} and ${settings.NAME} aren't valid YAML inside a flow collection
// until conductor substitutes them. The editor must still open such a file,
// read the references back literally, and leave them in place.
const withRefs = `hooks:
  sign: { header: X-Sig, secret: ${CW_SECRET}, scheme: sha256 }
  list: [${A}, b, "${settings.chan}"]
  plain: ${HOME_DIR}
`

func TestOpensConfigWithRefsInFlowCollections(t *testing.T) {
	d := mustParse(t, withRefs)
	for path, want := range map[string]string{
		"hooks.sign.secret": "${CW_SECRET}",
		"hooks.list.0":      "${A}",
		"hooks.list.2":      "${settings.chan}",
		"hooks.plain":       "${HOME_DIR}",
	} {
		if v, _ := d.Get(strings.Split(path, ".")); v != want {
			t.Errorf("%s = %#v, want %q", path, v, want)
		}
	}
	// Staging a reference back to itself is a no-op.
	if err := d.SetScalar([]string{"hooks", "sign", "secret"}, "${CW_SECRET}"); err != nil {
		t.Fatal(err)
	}
	if string(d.Bytes()) != withRefs {
		t.Fatalf("no-op changed the text:\n%s", d.Bytes())
	}
	mustSet(t, d, []string{"hooks", "sign", "scheme"}, "sha512")
	mustSet(t, d, []string{"hooks", "plain"}, "${OTHER}")
	mustSet(t, d, []string{"hooks", "added"}, map[string]any{"token": "${TOKEN}"})
	if !strings.Contains(string(d.Bytes()), "secret: ${CW_SECRET}, scheme: sha512 }") {
		t.Fatalf("reference next to the edit disturbed:\n%s", d.Bytes())
	}
}

// Deleting inside a flow collection would rewrite its whole line: refused.
func TestRefuseDeleteInsideFlowCollection(t *testing.T) {
	src := "a:\n  hosts: [x, y]\n  m: {k: 1, j: 2}\n"
	d := mustParse(t, src)
	for _, p := range [][]string{{"a", "hosts", "0"}, {"a", "m", "k"}} {
		if err := d.Delete(p); err == nil {
			t.Errorf("%v: delete inside a flow collection succeeded:\n%s", p, d.Bytes())
		}
	}
	if string(d.Bytes()) != src {
		t.Fatalf("text changed:\n%s", d.Bytes())
	}
}

// A key pulled in by a "<<:" merge isn't in the step's own text: deleting it
// must say so, not report a success that changes nothing. Setting it adds an
// explicit key, which overrides the merge.
func TestMergedKeys(t *testing.T) {
	src := "x-base:\n  shared: &base\n    model: big\n    type: agent\nsteps:\n  - id: a\n    <<: *base\n"
	d := mustParse(t, src)
	err := d.Delete([]string{"steps", "0", "model"})
	if err == nil || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("want a merge refusal, got %v", err)
	}
	if string(d.Bytes()) != src {
		t.Fatalf("text changed:\n%s", d.Bytes())
	}
	if v, _ := d.Get([]string{"steps", "0", "model"}); v != "big" {
		t.Fatalf("Get doesn't see the merged value: %v", v)
	}
	mustSet(t, d, []string{"steps", "0", "model"}, "small")
	if v, _ := d.Get([]string{"x-base", "shared", "model"}); v != "big" {
		t.Fatalf("the shared section changed:\n%s", d.Bytes())
	}
}

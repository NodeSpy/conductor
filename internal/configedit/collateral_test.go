package configedit

import (
	"strings"
	"testing"
)

// Edits through an alias rewrite the anchor, which changes every other user
// of it. Splices and deletes must refuse them like inserts already do.
const aliased = `x:
  shared: &a
    password_requires_totp: true
    methods: [passkey]
dashboard:
  auth: *a
other:
  auth: *a
`

func TestSpliceAndDeleteThroughAliasRefused(t *testing.T) {
	d := mustParse(t, aliased)
	if err := d.SetScalar([]string{"other", "auth", "password_requires_totp"}, false); err == nil {
		t.Errorf("splice through an alias succeeded:\n%s", d.Bytes())
	}
	if err := d.Delete([]string{"other", "auth", "methods"}); err == nil {
		t.Errorf("delete through an alias succeeded:\n%s", d.Bytes())
	}
	if string(d.Bytes()) != aliased {
		t.Fatalf("text changed:\n%s", d.Bytes())
	}
	// Editing the anchored definition itself is the intended way to change
	// every user at once.
	mustSet(t, d, []string{"x", "shared", "password_requires_totp"}, false)
	if v, _ := d.Get([]string{"dashboard", "auth", "password_requires_totp"}); v != false {
		t.Fatalf("alias didn't follow its anchor:\n%s", d.Bytes())
	}
}

// Setting a key inside a section that comes from a "<<:" merge must keep the
// merged section's other keys.
func TestSetUnderMergedParentKeepsSiblings(t *testing.T) {
	src := "x:\n  base: &base\n    opts:\n      a: 1\n      b: 2\n    model: big\nsteps:\n  - id: s\n    <<: *base\n"
	d := mustParse(t, src)
	mustSet(t, d, []string{"steps", "0", "opts", "a"}, 9)
	if v, _ := d.Get([]string{"steps", "0", "opts", "b"}); v != 2 {
		t.Fatalf("merged sibling dropped (opts.b = %v):\n%s", v, d.Bytes())
	}
	if v, _ := d.Get([]string{"steps", "0", "model"}); v != "big" {
		t.Fatalf("other merged key lost:\n%s", d.Bytes())
	}
	if v, _ := d.Get([]string{"x", "base", "opts", "a"}); v != 1 {
		t.Fatalf("the shared section changed:\n%s", d.Bytes())
	}
}

// classify must read a file whose ${...} references sit inside [...] or
// {...} (config.Load accepts it), not report it as unparseable and lose the
// widening list.
func TestClassifyWithFlowRefs(t *testing.T) {
	before := []byte("sandbox:\n  agents: on\nx-a:\n  z: [${HOME}]\n")
	after := []byte("sandbox:\n  agents: off\nx-a:\n  z: [${HOME}]\n")
	w, g := classify(before, after)
	for _, e := range g {
		if strings.Contains(e.Text, "parse") {
			t.Fatalf("classify says the config doesn't parse: %+v", g)
		}
	}
	if len(w) == 0 {
		t.Fatalf("widening lost: widening=%+v guarded=%+v", w, g)
	}
}

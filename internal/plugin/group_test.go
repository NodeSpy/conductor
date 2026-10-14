package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestDiscriminatorForLocalSplitsByContentNotPath is a TEST GAP at group.go
// ~191 (discriminatorFor): a local `use: ./path` build's process-group
// identity is documented as its CONTENT-addressed snapshot sha, never the
// declared path string — "two instances of a local plugin across a
// rebuild at the same path with different content" is the shape that would
// survive a mutation collapsing this back to a path-keyed (or otherwise
// content-blind) discriminator, since the path never changes across the
// rebuild in that scenario.
func TestDiscriminatorForLocalSplitsByContentNotPath(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "conductor-widget")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(binPath, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	u := config.Use{Origin: config.OriginLocal, Path: binPath}

	write("v1 build bytes")
	d1 := discriminatorFor(u, "widget", "", nil)
	if d1 == "" || d1 == "local-err:"+binPath {
		t.Fatalf("expected a successful content-addressed discriminator, got %q", d1)
	}

	// A rebuild AT THE SAME declared path, with different content — the
	// reviewer's exact reproduction shape.
	write("v2 build bytes, nothing like the first")
	d2 := discriminatorFor(u, "widget", "", nil)
	if d2 == "" || d2 == "local-err:"+binPath {
		t.Fatalf("expected a successful content-addressed discriminator after rebuild, got %q", d2)
	}
	if d1 == d2 {
		t.Fatalf("a rebuild at the SAME path with DIFFERENT content must produce a DIFFERENT discriminator — got the identical %q for both, which means grouping is keyed by path, not content", d1)
	}

	// Rebuilding back to the original bytes reproduces the ORIGINAL
	// discriminator exactly — content-addressed, not a monotonic counter or
	// a path+mtime hash.
	write("v1 build bytes")
	d3 := discriminatorFor(u, "widget", "", nil)
	if d3 != d1 {
		t.Fatalf("identical content must produce the identical discriminator regardless of the rebuild in between: original=%q after round-trip=%q", d1, d3)
	}
}

// TestGroupRefLocalInstancesSplitAcrossRebuild drives the same scenario
// through groupRef, the function Manager construction actually calls: two
// configured instances of ONE local plugin declared at the identical path
// share a group while their content agrees, and a rebuild between
// successive groupRef passes is reflected in a changed discriminator (the
// signal a hot-reload / re-snapshot path depends on to notice the binary
// moved), never silently reusing a path-keyed identity.
func TestGroupRefLocalInstancesSplitAcrossRebuild(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "conductor-widget")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(binPath, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("v1 build bytes")

	u := config.Use{Origin: config.OriginLocal, Path: binPath, Name: "widget"}
	ref := config.PluginRef{
		Name: "widget", Instance: "a", Use: u,
		Instances: map[string]config.ConnectorGrant{
			"a": {Use: u},
			"b": {Use: u},
		},
	}

	groups1, err := groupRef(ref, "", nil)
	if err != nil {
		t.Fatalf("groupRef: %v", err)
	}
	if len(groups1) != 1 {
		t.Fatalf("two instances of one local plugin with IDENTICAL content must share one group, got %d: %+v", len(groups1), groups1)
	}
	disc1 := groups1[0].discriminator

	write("v2 build bytes, nothing like the first")
	groups2, err := groupRef(ref, "", nil)
	if err != nil {
		t.Fatalf("groupRef after rebuild: %v", err)
	}
	if len(groups2) != 1 {
		t.Fatalf("both instances still point at the SAME (now rebuilt) path, so they must still share ONE group, got %d: %+v", len(groups2), groups2)
	}
	if groups2[0].discriminator == disc1 {
		t.Fatalf("a rebuild at the same path must change the group's discriminator (content changed), got the same %q before and after", disc1)
	}
}

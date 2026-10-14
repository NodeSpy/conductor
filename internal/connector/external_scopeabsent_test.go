package connector

import (
	"testing"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

// A plugin's scoped option crosses the wire whole: its dimension AND what an
// absent value names (github put_file without a branch writes the default
// branch), so the host checks an omitted option like a supplied one.
func TestMapSchemaCarriesScopeAbsent(t *testing.T) {
	got := mapSchema(plugin.Schema{"branch": {Type: "string", Scope: "branch", ScopeAbsent: "@default-branch"}})
	if f := got["branch"]; f.Scope != "branch" || f.ScopeAbsent != "@default-branch" {
		t.Fatalf("mapSchema dropped the scope: %+v", f)
	}
}

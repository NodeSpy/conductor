package plugin

import (
	"strings"
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A git_fetch declaration admits its host to agents' egress: a build that
// adds one, moves it to another host, or names another credential or
// setting widens the plugin and is held; dropping one does not.
func TestGitFetchWidens(t *testing.T) {
	decl := func(gs ...sdk.GitFetch) *Decl {
		return &Decl{Kind: sdk.KindConnector, Semantics: &sdk.ConnSemantics{GitFetch: gs}}
	}
	base := sdk.GitFetch{Credential: "read", Hosts: []string{"github.com"}, HostFrom: "api_base", Username: "x-access-token"}
	old := manifestFromDecl(decl(base))
	if w := old.Widens(old); len(w) != 0 {
		t.Fatalf("same: %v", w)
	}
	if w := manifestFromDecl(decl()).Widens(old); len(w) != 2 || !strings.Contains(strings.Join(w, ";"), "git fetch") {
		t.Fatalf("added: %v", w)
	}
	if w := old.Widens(manifestFromDecl(decl())); len(w) != 0 {
		t.Fatalf("dropped: %v", w)
	}
	for name, g := range map[string]sdk.GitFetch{
		"another host":       {Credential: "read", Hosts: []string{"evil.example"}, HostFrom: "api_base", Username: "x-access-token"},
		"another setting":    {Credential: "read", Hosts: []string{"github.com"}, HostFrom: "webhook_url", Username: "x-access-token"},
		"another credential": {Credential: "write", Hosts: []string{"github.com"}, HostFrom: "api_base", Username: "x-access-token"},
		"another username":   {Credential: "read", Hosts: []string{"github.com"}, HostFrom: "api_base", Username: "other"},
	} {
		if w := old.Widens(manifestFromDecl(decl(g))); len(w) == 0 {
			t.Errorf("%s: not held", name)
		}
	}
	// The hot-reload surface differs too: never swapped in place.
	if SameReloadSurface(decl(base), decl()) {
		t.Fatal("a git_fetch change is the same reload surface")
	}
}

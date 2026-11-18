package plugin

import (
	"strings"
	"testing"
)

func TestGitFetchDeclaration(t *testing.T) {
	decl := func(g GitFetch, role string) Decl {
		return Decl{Kind: KindConnector, Verbs: []Verb{{Name: "read_token", Options: map[string]Field{},
			Semantics: &VerbSemantics{HostOnly: true, MintsCredential: &MintsCredential{Credential: "read"}}}},
			Semantics: &ConnSemantics{Credentials: []Credential{{Name: "read", Role: role, Mint: CredentialMint{Verb: "read_token"}}},
				GitFetch: []GitFetch{g}}, Connection: Schema{"api_base": {Type: "string"}}}
	}
	good := GitFetch{Credential: "read", Hosts: []string{"forge.example", "forge.example:8443"}, Username: "x", HostFrom: "api_base"}
	if p := ValidateSemantics(decl(good, "read")); len(p) != 0 {
		t.Fatalf("good: %v", p)
	}
	for name, c := range map[string]struct {
		g    GitFetch
		role string
		want string
	}{
		"write credential": {good, "write", "role read"},
		"unknown":          {GitFetch{Credential: "nope", Hosts: good.Hosts, Username: "x"}, "read", "role read"},
		"no hosts":         {GitFetch{Credential: "read", Username: "x"}, "read", "hosts: required"},
		"wildcard host":    {GitFetch{Credential: "read", Hosts: []string{"*.forge.example"}, Username: "x"}, "read", "exact host"},
		"upper host":       {GitFetch{Credential: "read", Hosts: []string{"Forge.example"}, Username: "x"}, "read", "exact host"},
		"bad port":         {GitFetch{Credential: "read", Hosts: []string{"forge.example:0"}, Username: "x"}, "read", "exact host"},
		"url":              {GitFetch{Credential: "read", Hosts: []string{"https://forge.example"}, Username: "x"}, "read", "exact host"},
		"no user":          {GitFetch{Credential: "read", Hosts: good.Hosts}, "read", "username"},
		"colon user":       {GitFetch{Credential: "read", Hosts: good.Hosts, Username: "a:b"}, "read", "username"},
		"undeclared from":  {GitFetch{Credential: "read", Hosts: good.Hosts, Username: "x", HostFrom: "nope"}, "read", "host_from"},
	} {
		p := strings.Join(ValidateSemantics(decl(c.g, c.role)), "; ")
		if !strings.Contains(p, "git_fetch") || !strings.Contains(p, c.want) {
			t.Errorf("%s: problems %q, want %q", name, p, c.want)
		}
	}
	found := false
	for _, k := range KnownSemantics() {
		found = found || k == "connection.git_fetch"
	}
	if !found {
		t.Fatal("connection.git_fetch is not a known semantic")
	}
}

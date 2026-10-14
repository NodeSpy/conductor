package dispatch

import (
	"strings"
	"testing"
)

// declaredIdentity stands in for a connector's declared credential guidance
// (github's tells the agent its GH_TOKEN is the operator's, and that pushes
// go over SSH).
const declaredIdentity = "\n\n---\nIDENTITY: GH_TOKEN/GITHUB_TOKEN are MY token; `git push` goes over SSH as me. " +
	"Read with `GH_TOKEN=$PC_GH_APP_TOKEN gh ...`."

// ForJail swaps exactly the declared credential guidance; the jailed block
// names no token variable and no SSH, and a prompt without the declared text
// — or a request whose connector declared none — is untouched.
func TestForJailSwapsOnlyTheDeclaredGuidance(t *testing.T) {
	p := "task text" + declaredIdentity + "\n\nmore guidance"
	got := ForJail(p, declaredIdentity)
	if got != "task text"+JailedIdentityGuidance+"\n\nmore guidance" {
		t.Fatalf("ForJail:\n%s", got)
	}
	for _, bad := range []string{"TOKEN", "SSH", "ssh", "$"} {
		if strings.Contains(JailedIdentityGuidance, bad) {
			t.Errorf("jailed identity guidance mentions %q", bad)
		}
	}
	if ForJail("plain prompt", declaredIdentity) != "plain prompt" {
		t.Fatal("a prompt without the declared guidance is unchanged")
	}
	if ForJail(p, "") != p {
		t.Fatal("a request with no declared guidance is unchanged")
	}
	if !strings.Contains(JailedIdentityGuidance, "SCOPE: your writes are bound to THIS target") {
		t.Fatal("the jailed guidance carries the target scope")
	}
}

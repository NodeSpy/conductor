package connector

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
)

// The OAuth endpoints conductor posts the client secret and refresh token to
// come from the install record, not from what the running build declares: a
// build that answers describe with another token_url gets nothing.
func TestManagedAuthEndpointsComeFromTheInstallRecord(t *testing.T) {
	recorded := &plugin.AuthSpec{TokenURL: "https://oauth.vendor.example/token"}
	live := &plugin.Decl{Auth: &plugin.AuthSpec{TokenURL: "https://collector.attacker.example/token"}}
	spec := plugin.Spec{Name: "acme", Manifest: plugin.Manifest{Auth: recorded}}
	if got := managedAuthSpec(spec, live); got != recorded {
		t.Fatalf("managed auth uses %+v, want the recorded %+v", got, recorded)
	}
	// A local build has no record: it is what the operator built.
	spec.Local = true
	if got := managedAuthSpec(spec, live); got != live.Auth {
		t.Fatalf("local build: managed auth uses %+v, want its own declaration", got)
	}
}

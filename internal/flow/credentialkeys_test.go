package flow

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// TestCredentialKeysUnionsEveryResolvedVersion is finding 5: credentialKeys'
// type sweep (connector.Types() + connector.TypeDeclFor) used to see only
// the FIRST-registered group's declaration for a type — if two resolved
// versions of the same connector type are configured side by side and a
// later release declares a credential template the earlier one didn't, a
// workflow step referencing {{.<that template>}} would be wrongly rejected
// just because the representative group (which might be the OLDER version)
// doesn't declare it. credentialKeys must union every group's credentials.
func TestCredentialKeysUnionsEveryResolvedVersion(t *testing.T) {
	const typ = "zz-cred-multi-version"
	t.Cleanup(func() {
		connector.UnregisterExternalType(typ)
		connector.ResetInstanceGroups()
	})
	gk1 := "connectors/" + typ + "@v1.0.0"
	gk2 := "connectors/" + typ + "@v2.0.0"
	d1 := &connector.TypeDecl{Type: typ, Semantics: &sdk.ConnSemantics{
		Credentials: []sdk.Credential{{Name: "tok", Template: "zzToken"}},
	}}
	d2 := &connector.TypeDecl{Type: typ, Semantics: &sdk.ConnSemantics{
		Credentials: []sdk.Credential{{Name: "tok2", Template: "zzTokenV2"}},
	}}
	if err := connector.RegisterExternalTypeGroup(d1, nil, gk1, "connectors/"+typ); err != nil {
		t.Fatalf("register group 1: %v", err)
	}
	if err := connector.RegisterExternalTypeGroup(d2, nil, gk2, "connectors/"+typ); err != nil {
		t.Fatalf("register group 2: %v", err)
	}

	keys := credentialKeys()
	has := map[string]bool{}
	for _, k := range keys {
		has[k] = true
	}
	if !has["zzToken"] {
		t.Errorf("credentialKeys missing the FIRST (representative) group's template: %v", keys)
	}
	if !has["zzTokenV2"] {
		t.Errorf("credentialKeys missing the SECOND group's own template (the representative-only bug): %v", keys)
	}
}

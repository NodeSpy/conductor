package plugin

import (
	"github.com/NodeSpy/conductor/internal/keyringbroker"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// keyringSensitivity says what a declared item is when it is one of the
// items no plugin reads unless the user's config names it
// (keyringbroker.Sensitivity; "" when it is not). The broker checks the same
// list again against the attributes each served item really carries.
func keyringSensitivity(it sdk.KeyringItem) string { return keyringbroker.Sensitivity(it.Attributes) }

// allowedKeyring splits resolved items into those granted and the
// sensitive ones the config does not name in allow_keyring
// (keyringbroker.Allows).
func allowedKeyring(items []sdk.KeyringItem, allow []string) (granted, withheld []sdk.KeyringItem) {
	for _, it := range items {
		if keyringSensitivity(it) != "" && !keyringbroker.Allows(allow, it.Attributes) {
			withheld = append(withheld, it)
			continue
		}
		granted = append(granted, it)
	}
	return granted, withheld
}

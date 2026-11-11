package plugin

import (
	"slices"
	"strings"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// sensitiveKeyring are keyring items no plugin reads unless the user's
// config names it (allow_keyring): each one unlocks far more than a CLI's
// login — every password or cookie a browser keeps, the user's network
// secrets, their online accounts, another keyring. A declared item carrying
// one of these attribute values is one of them.
//
// It is a list of what is known to be held in the Secret Service on Linux
// (and named the same in the declarations that would read it); it is not a
// boundary by itself — the declared attributes are (sdk.IdentifyingKeys) —
// but a default a declaration cannot talk its way past.
var sensitiveKeyring = []struct {
	key    string
	values []string
	what   string
}{
	{"xdg:schema", []string{"chrome_libsecret_os_crypt_password_v1", "chrome_libsecret_os_crypt_password_v2"},
		"a browser's master key, which decrypts every password and cookie it keeps"},
	{"application", []string{"chrome", "chromium", "google-chrome", "brave", "brave-browser", "microsoft-edge", "msedge",
		"vivaldi", "opera", "yandex-browser", "firefox", "librewolf", "thunderbird", "electron"},
		"a browser's (or an app's) master key, which decrypts every password and cookie it keeps"},
	{"xdg:schema", []string{"org.gnome.keyring.ChainedKeyring", "org.gnome.keyring.Note"},
		"another keyring's password, or a note the user keeps"},
	{"xdg:schema", []string{"org.freedesktop.NetworkManager.Connection"},
		"a network's secret (Wi-Fi, VPN)"},
	{"xdg:schema", []string{"org.gnome.OnlineAccounts", "org.gnome.Evolution.Data.Source"},
		"the desktop's online accounts (mail, calendar, cloud)"},
	{"xdg:schema", []string{"org.keepassxc.KeePassXC.MainWindow"},
		"a password manager's database key"},
}

// keyringSensitivity says what a declared item is when it is one of
// sensitiveKeyring ("" when it is not).
func keyringSensitivity(it sdk.KeyringItem) string {
	for _, s := range sensitiveKeyring {
		v, ok := it.Attributes[s.key]
		if !ok {
			continue
		}
		if slices.ContainsFunc(s.values, func(w string) bool { return strings.EqualFold(strings.TrimSpace(v), w) }) {
			return s.what
		}
	}
	return ""
}

// normSelector is an allow_keyring entry as sdk.KeyringItem.Selector
// spells it: "k=v" pairs sorted, joined by ", ".
func normSelector(s string) string {
	var parts []string
	for _, p := range strings.Split(s, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		parts = append(parts, strings.TrimSpace(k)+"="+strings.TrimSpace(v))
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// allowedKeyring splits resolved items into those granted and the
// sensitive ones the config does not name in allow_keyring (by selector).
func allowedKeyring(items []sdk.KeyringItem, allow []string) (granted, withheld []sdk.KeyringItem) {
	for _, it := range items {
		if keyringSensitivity(it) != "" && !slices.ContainsFunc(allow, func(a string) bool { return normSelector(a) == it.Selector() }) {
			withheld = append(withheld, it)
			continue
		}
		granted = append(granted, it)
	}
	return granted, withheld
}

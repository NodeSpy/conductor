package keyringbroker

import (
	"slices"
	"strings"
)

// sensitive are keyring items no plugin reads unless the user's config
// names them (allow_keyring): each one unlocks far more than a CLI's login —
// every password or cookie a browser keeps, the user's network secrets,
// their online accounts, another keyring.
//
// The list is checked twice: against a declared item (consent, the grant),
// and, where it is served, against the attributes the item in the keyring
// really carries — a declaration naming only application=<app> must not reach
// the app's safe-storage key, whose schema it never named.
var sensitive = []struct {
	key    string
	values []string
	what   string
}{
	{"xdg:schema", []string{"chrome_libsecret_os_crypt_password_v1", "chrome_libsecret_os_crypt_password_v2"},
		"a browser's (or an app's) master key, which decrypts every password and cookie it keeps"},
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

// Sensitivity says what an item carrying attrs is when it is one of the
// sensitive items ("" when it is not). Values compare without case or
// surrounding space.
func Sensitivity(attrs map[string]string) string {
	for _, s := range sensitive {
		v, ok := attrs[s.key]
		if !ok {
			continue
		}
		if slices.ContainsFunc(s.values, func(w string) bool { return strings.EqualFold(strings.TrimSpace(v), w) }) {
			return s.what
		}
	}
	return ""
}

// Allows reports whether an allow_keyring entry names an item carrying
// attrs: every "k=v" pair of some entry is one of attrs, exactly. An entry
// with no pair, or a pair without a key, names nothing.
func Allows(allow []string, attrs map[string]string) bool {
	return slices.ContainsFunc(allow, func(e string) bool {
		pairs := strings.Split(e, ",")
		for _, p := range pairs {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			k = strings.TrimSpace(k)
			if !ok || k == "" {
				return false
			}
			if have, set := attrs[k]; !set || have != strings.TrimSpace(v) {
				return false
			}
		}
		return len(pairs) > 0
	})
}

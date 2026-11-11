package keyringbroker

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// An app's safe-storage key (an Electron app keeps it as Chromium does)
// carries the app's name and the browser's schema. A declaration naming the
// app alone matches it; it is withheld by the schema it really carries — in
// a search, the collection's items, GetSecret and GetSecrets — unless the
// user's allow_keyring names it.
func TestBrokerWithholdsSensitiveServedItems(t *testing.T) {
	const masterPath, tokenPath = dbus.ObjectPath("/org/freedesktop/secrets/collection/login/7"), dbus.ObjectPath("/org/freedesktop/secrets/collection/login/8")
	fake := func() *fakeKeyring {
		return &fakeKeyring{items: map[dbus.ObjectPath]fakeItem{
			masterPath: {attrs: map[string]string{"xdg:schema": "chrome_libsecret_os_crypt_password_v2", "application": "Slack"}, value: "MASTER-KEY"},
			tokenPath:  {attrs: map[string]string{"application": "Slack", "service": "slack-cli"}, value: "cli-token"},
		}}
	}
	items := []sdk.KeyringItem{{Label: "Slack CLI login", Attributes: map[string]string{"application": "Slack"}}}
	served := func(t *testing.T, allow []string) (paths []dbus.ObjectPath, values []string, up *fakeKeyring, l *logs) {
		t.Helper()
		up = fake()
		c, l := serveFakeAllowing(t, up, items, allow)
		var out dbus.Variant
		var sess dbus.ObjectPath
		if err := svc(c).Call(ifService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &sess); err != nil {
			t.Fatal(err)
		}
		var u, lk []dbus.ObjectPath
		if err := svc(c).Call(ifService+".SearchItems", 0, map[string]string{"xdg:schema": "chrome_libsecret_os_crypt_password_v2"}).Store(&u, &lk); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, u...)
		if err := svc(c).Call(ifService+".SearchItems", 0, map[string]string{}).Store(&u, &lk); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, u...)
		var coll []dbus.ObjectPath
		if err := c.Object(secretsName, collection).Call(ifCollection+".SearchItems", 0, map[string]string{"application": "Slack"}).Store(&coll); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, coll...)
		var iv dbus.Variant
		if err := c.Object(secretsName, aliasDefault).Call(ifProps+".Get", 0, ifCollection, "Items").Store(&iv); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, iv.Value().([]dbus.ObjectPath)...)
		var s secret
		err := c.Object(secretsName, masterPath).Call(ifItem+".GetSecret", 0, sess).Store(&s)
		var de dbus.Error
		switch {
		case err == nil:
			values = append(values, string(s.Value))
		case !errors.As(err, &de) || de.Name != errNoObject:
			t.Errorf("GetSecret of the master key: %v", err)
		}
		var many map[dbus.ObjectPath]secret
		if err := svc(c).Call(ifService+".GetSecrets", 0, []dbus.ObjectPath{masterPath, tokenPath}, sess).Store(&many); err != nil {
			t.Fatal(err)
		}
		for _, v := range many {
			values = append(values, string(v.Value))
		}
		var av dbus.Variant
		if err := c.Object(secretsName, masterPath).Call(ifProps+".Get", 0, ifItem, "Attributes").Store(&av); err == nil {
			values = append(values, "attributes")
		}
		return paths, values, up, l
	}

	paths, values, up, l := served(t, nil)
	if slices.Contains(paths, masterPath) || slices.Contains(values, "MASTER-KEY") || slices.Contains(values, "attributes") {
		t.Fatalf("the master key was served: paths %v, values %q", paths, values)
	}
	if !slices.Contains(paths, tokenPath) || !slices.Contains(values, "cli-token") {
		t.Fatalf("the app's own login was not served: paths %v, values %q", paths, values)
	}
	if _, read := up.seen(); slices.Contains(read, masterPath) {
		t.Fatal("the keyring was read for the master key")
	}
	var withheld []string
	for _, m := range l.get() {
		if strings.Contains(m, "withheld") {
			withheld = append(withheld, m)
		}
	}
	if len(withheld) != 1 || !strings.Contains(withheld[0], `"application=Slack"`) || !strings.Contains(withheld[0], "master key") || !strings.Contains(withheld[0], "allow_keyring") {
		t.Fatalf("logged: %q", l.get())
	}

	// Named in allow_keyring (by the declared selector, or by the item's own
	// attributes), it is served.
	for _, allow := range []string{"application=Slack", "xdg:schema=chrome_libsecret_os_crypt_password_v2, application=Slack"} {
		paths, values, _, _ = served(t, []string{allow})
		if !slices.Contains(paths, masterPath) || !slices.Contains(values, "MASTER-KEY") {
			t.Fatalf("allowed by %q, not served: paths %v, values %q", allow, paths, values)
		}
	}
	// Named for another item, or spelled with another value, it is not.
	for _, allow := range []string{"application=slack", "application=Slack, service=slack-cli", "=", "", "application"} {
		paths, values, _, _ = served(t, []string{allow})
		if slices.Contains(paths, masterPath) || slices.Contains(values, "MASTER-KEY") {
			t.Fatalf("allowed by %q: paths %v, values %q", allow, paths, values)
		}
	}
}

func TestSensitivityAndAllows(t *testing.T) {
	for _, a := range []map[string]string{
		{"xdg:schema": "chrome_libsecret_os_crypt_password_v1"},
		{"xdg:schema": "chrome_libsecret_os_crypt_password_v2", "application": "Slack"},
		{"application": " Chrome "},
		{"xdg:schema": "org.freedesktop.NetworkManager.Connection", "connection-uuid": "x"},
	} {
		if Sensitivity(a) == "" {
			t.Errorf("%v is not sensitive", a)
		}
	}
	for _, a := range []map[string]string{{"service": "gh:github.com"}, {"application": "Slack"}, {}} {
		if s := Sensitivity(a); s != "" {
			t.Errorf("%v is %s", a, s)
		}
	}
	attrs := map[string]string{"application": "chrome", "service": "x"}
	for allow, want := range map[string]bool{
		"application=chrome":                     true,
		" application = chrome , service = x ":   true,
		"service=x,application=chrome":           true,
		"application=chrome, service=x, extra=y": false,
		"application=Chrome":                     false,
		"application":                            false,
		"=chrome":                                false,
		"":                                       false,
	} {
		if got := Allows([]string{allow}, attrs); got != want {
			t.Errorf("Allows(%q) = %v", allow, got)
		}
	}
}

package plugin

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

var toolItem = KeyringItem{Label: "tool login for example.com", Attributes: map[string]string{"service": "tool:example.com"}}

func TestKeyringConsentLine(t *testing.T) {
	m := Manifest{Keyring: []KeyringItem{toolItem}}
	want := "reads every item in your keyring with service=\"tool:example.com\" (\"tool login for example.com\", in its words)"
	if got := strings.Join(m.Lines(), "\n"); !strings.Contains(got, want) {
		t.Fatalf("consent lines:\n%s\nwant a line %q", got, want)
	}
	if m.IsZero() {
		t.Fatal("a manifest declaring a keyring item is not zero")
	}
	if c := m.Caps(); len(c.Keyring) != 1 || c.Keyring[0].Selector() != "service=tool:example.com" {
		t.Fatalf("Caps: %+v", c.Keyring)
	}
}

func TestKeyringWidens(t *testing.T) {
	prevGOOS := spawnGOOS
	t.Cleanup(func() { spawnGOOS = prevGOOS })
	spawnGOOS = "linux"
	other := KeyringItem{Label: "tool login for other.example", Attributes: map[string]string{"service": "tool:other.example"}}
	narrower := KeyringItem{Label: "tool login for me", Attributes: map[string]string{"service": "tool:example.com", "username": "me"}}
	relabelled := KeyringItem{Label: "renamed", Attributes: map[string]string{"service": "tool:example.com"}}
	for _, tc := range []struct {
		name       string
		have, next Manifest
		widens     bool
	}{
		{"a new item", Manifest{}, Manifest{Keyring: []KeyringItem{toolItem}}, true},
		{"the macOS Keychain does not cover it off macOS", Manifest{Keychain: true}, Manifest{Keychain: true, Keyring: []KeyringItem{toolItem}}, true},
		{"another item", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{toolItem, other}}, true},
		{"the same item", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{toolItem}}, false},
		// Narrower, but under a new label: asked for again (the label is consent).
		{"a narrower item", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{narrower}}, true},
		{"a narrower item, same label", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{{Label: toolItem.Label, Attributes: narrower.Attributes}}}, false},
		{"a wider item", Manifest{Keyring: []KeyringItem{narrower}}, Manifest{Keyring: []KeyringItem{toolItem}}, true},
		// The label is consent text: a new one is asked for again.
		{"a new label alone", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{relabelled}}, true},
	} {
		if w := tc.have.Widens(tc.next); (len(w) > 0) != tc.widens {
			t.Errorf("%s: widens %v, want widening=%v", tc.name, w, tc.widens)
		}
	}
	if r := (Manifest{Keyring: []KeyringItem{toolItem}}).Widens(Manifest{}); len(r) != 0 {
		t.Errorf("dropping an item widens: %v", r)
	}
	if r := (Manifest{}).Widens(Manifest{Keyring: []KeyringItem{toolItem}}); len(r) != 1 || !strings.Contains(r[0], "in your keyring with service=\"tool:example.com\"") {
		t.Errorf("the widening names the item: %v", r)
	}
	// On macOS a keyring item is granted as the Keychain: a build that has
	// the Keychain already has it.
	spawnGOOS = "darwin"
	if w := (Manifest{Keychain: true}).Widens(Manifest{Keychain: true, Keyring: []KeyringItem{toolItem}}); len(w) != 0 {
		t.Errorf("macOS: an item the Keychain grant covers widens: %v", w)
	}
	if w := (Manifest{}).Widens(Manifest{Keyring: []KeyringItem{toolItem}}); len(w) != 1 {
		t.Errorf("macOS: a new item without the Keychain: %v", w)
	}
	if w := (Manifest{Keychain: true, Keyring: []KeyringItem{toolItem}}).Widens(Manifest{Keychain: true, Keyring: []KeyringItem{relabelled}}); len(w) != 1 {
		t.Errorf("macOS: a new label under a Keychain grant: %v", w)
	}
}

// An update that adds a keyring item asks for a new permission: it is held
// for approval and the installed build keeps running, as for any other.
func TestReconcileUpdateAddingKeyringIsHeld(t *testing.T) {
	prevGOOS := spawnGOOS
	t.Cleanup(func() { spawnGOOS = prevGOOS })
	spawnGOOS = "linux"
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	decl := func(c Capabilities) DescribeFunc {
		return func(context.Context, Spec) (*Decl, error) {
			return &Decl{Kind: KindConnector, Type: "jira", Capabilities: c}, nil
		}
	}
	v1 := Capabilities{Commands: []string{"tool"}, Keychain: true}
	if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: decl(v1)}); err != nil {
		t.Fatal(err)
	}
	v2 := v1
	v2.Keyring = []KeyringItem{toolItem}
	res, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0", "jira/v2.0.0"), Options{Describe: decl(v2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionHeld {
		t.Fatalf("resolution = %+v, want held", res)
	}
	if got := strings.Join(res[0].Widens, "; "); got != "reads every item in your keyring with service=\"tool:example.com\" (\"tool login for example.com\", in its words)" {
		t.Fatalf("held for: %q", got)
	}
	if rec, _ := st.Get("connectors/jira"); rec.Resolved != "jira/v1.0.0" || len(rec.Manifest.Keyring) != 0 {
		t.Fatalf("the installed record changed: %+v", rec)
	}
}

// A record a conductor the SDK withheld keyring from (v0.88 to v0.90:
// jailSchema; after that, until keyring: refsSchema) is described again on
// the first pass, once. A keyring item the build declares is not given on
// adoption: unlike a withheld config: reference it is a new permission, so
// it is held for approval. A build that declares none is recorded quietly.
func TestOlderRecordHoldsKeyring(t *testing.T) {
	prevGOOS := spawnGOOS
	t.Cleanup(func() { spawnGOOS = prevGOOS })
	spawnGOOS = "linux"
	withheld := Capabilities{Commands: []string{"tool"}, Keychain: true}
	full := withheld
	full.Keyring = []KeyringItem{toolItem}
	for _, tc := range []struct {
		name   string
		schema int
		now    Capabilities
		held   bool
	}{
		{"jailSchema, an item", jailSchema, full, true},
		{"refsSchema, an item", refsSchema, full, true},
		{"refsSchema, no item", refsSchema, withheld, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := stateAt(t)
			ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
			refs := map[string]config.PluginRef{ref.Key(): ref}
			describes := 0
			decl := func(c Capabilities) DescribeFunc {
				return func(context.Context, Spec) (*Decl, error) {
					describes++
					return &Decl{Kind: KindConnector, Type: "jira", Capabilities: c}, nil
				}
			}
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: decl(withheld)}); err != nil {
				t.Fatal(err)
			}
			rec, _ := st.Get("connectors/jira")
			rec.ManifestSchema = tc.schema
			st.Put(rec)
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
			st = LoadInstallState(st.Dir())
			describes = 0
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: decl(tc.now)}); err != nil {
				t.Fatal(err)
			}
			if describes != 1 {
				t.Fatalf("described %d times, want once", describes)
			}
			cur, _ := st.Get("connectors/jira")
			if len(cur.Manifest.Keyring) != 0 {
				t.Fatalf("adoption granted the keyring item without approval: %+v", cur.Manifest)
			}
			p := st.PendingUpdates()
			if tc.held != (len(p) == 1) {
				t.Fatalf("pending %+v, want held=%v", p, tc.held)
			}
			if tc.held && !strings.Contains(strings.Join(p[0].Widens, ";"), "in your keyring with") {
				t.Fatalf("held for: %v", p[0].Widens)
			}
			if !tc.held && cur.ManifestSchema != manifestSchema {
				t.Fatalf("schema %d, want %d", cur.ManifestSchema, manifestSchema)
			}
			// Once recorded, the next pass does not describe it again.
			describes = 0
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: decl(tc.now)}); err != nil {
				t.Fatal(err)
			}
			if !tc.held && describes != 0 {
				t.Fatalf("a recorded build was described again (%d)", describes)
			}
		})
	}
}

// On macOS a declared keyring item is read from the Keychain: the jail's
// profile grants it read-only (keychain: true, read-write).
func TestSeatbeltGrantsKeychainForKeyringItems(t *testing.T) {
	oldG, oldL, oldC, oldH := spawnGOOS, spawnLookPath, sandbox.CheckGOOS, jailHome
	spawnGOOS = "darwin"
	spawnLookPath = func(n string) (string, error) { return "/usr/bin/" + n, nil }
	sandbox.CheckGOOS = "darwin"
	home := t.TempDir()
	jailHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { spawnGOOS, spawnLookPath, sandbox.CheckGOOS, jailHome = oldG, oldL, oldC, oldH })
	dir := t.TempDir()
	bin := writeBin(t, dir, "plugin", []byte("x"), 0o755)
	sd := SandboxDeps{EgressAddr: func([]string) (string, string, func(), error) { return "127.0.0.1:9", "tok", func() {}, nil }}
	for _, tc := range []struct {
		m    Manifest
		want bool
		rw   bool
	}{{Manifest{}, false, false}, {Manifest{Keyring: []KeyringItem{toolItem}}, true, false}, {Manifest{Keychain: true}, true, true},
		// keychain: true beside items (a record from a describe that sent
		// both): read-only all the same.
		{Manifest{Keychain: true, Keyring: []KeyringItem{toolItem}}, true, false}} {
		cmd, cleanup, _, err := buildCommand(Spec{Name: "p", Kind: KindConnector, BinPath: bin, Local: true, Manifest: tc.m}, sd)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if got := strings.Contains(cmd.Args[2], filepath.Join(home, "Library/Keychains")); got != tc.want {
			t.Errorf("%+v: Keychain granted=%v, want %v", tc.m, got, tc.want)
		}
		// A keyring item reads the Keychain; only keychain: true writes it.
		if got := strings.Contains(cmd.Args[2], "file-write* (subpath \""+filepath.Join(home, "Library/Keychains")); got != tc.rw {
			t.Errorf("%+v: Keychain writable=%v, want %v", tc.m, got, tc.rw)
		}
	}
}

// A referencing item is shown as its reference, as a config: path is.
func TestKeyringConsentLineReferences(t *testing.T) {
	// A value is the plugin's text, quoted as the label is: it cannot
	// close the line and write consent of its own; nor can a name.
	v := strings.Join(Manifest{Keyring: []KeyringItem{{Label: "x", Attributes: map[string]string{
		"service": `tool ("x", in its words); reads nothing else`, `a key, with=words`: "v"}}}}.Lines(), "\n")
	if want := `with "a key, with=words"="v", service="tool (\"x\", in its words); reads nothing else" ("x", in its words)`; !strings.Contains(v, want) {
		t.Errorf("consent %q: want %q", v, want)
	}
	m := Manifest{Keyring: []KeyringItem{
		{Label: "tool login for your server", Attributes: map[string]string{"service": "tool:${host:config:api_base}"}},
		{Label: "registry login", Attributes: map[string]string{"server": "${config:registries}", "kind": "1"}},
		{Label: "tool login for $TOOL_HOST", Attributes: map[string]string{"service": "tool:${env:TOOL_HOST}"}},
	}}
	got := strings.Join(m.Lines(), "\n")
	for _, want := range []string{
		"reads every item in your keyring with service=\"tool:<host of api_base from its config>\" (\"tool login for your server\", in its words)",
		"reads every item in your keyring with kind=\"1\", server=\"<registries from its config>\" (\"registry login\", in its words)",
		"reads every item in your keyring with service=\"tool:$TOOL_HOST\" (\"tool login for $TOOL_HOST\", in its words)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("consent lines:\n%s\nwant a line %q", got, want)
		}
	}
}

// On macOS the consent says what a keyring item really grants: the
// Keychain, read-only, which macOS cannot narrow to the item.
func TestKeyringConsentLineDarwin(t *testing.T) {
	prev := spawnGOOS
	t.Cleanup(func() { spawnGOOS = prev })
	spawnGOOS = "darwin"
	got := strings.Join(Manifest{Keyring: []KeyringItem{toolItem}}.Lines(), "\n")
	for _, want := range []string{"macOS Keychain, read-only", `service="tool:example.com"`, "cannot confine it", "per-item access controls"} {
		if !strings.Contains(got, want) {
			t.Errorf("darwin consent %q: no %q", got, want)
		}
	}
}

// An env: reference resolves from the env allow_env leaves the plugin, as
// its env does: a variable the config withholds names no item.
func TestKeyringEnvRefNarrowedByAllowEnv(t *testing.T) {
	t.Setenv("TOOL_HOST", "env.example.test")
	m := Manifest{Env: []string{"TOOL_HOST"}, Keyring: []KeyringItem{{Label: "tool login for $TOOL_HOST", Attributes: map[string]string{"service": "tool:${host:env:TOOL_HOST}"}}}}
	g, err := pluginGrant(Spec{Name: "p", Manifest: m})
	if err != nil || len(g.Keyring) != 1 || g.Keyring[0].Attributes["service"] != "tool:env.example.test" {
		t.Fatalf("allowed: %+v %v", g.Keyring, err)
	}
	g, err = pluginGrant(Spec{Name: "p", Manifest: m, AllowEnv: []string{}})
	if err != nil || len(g.Keyring) != 0 {
		t.Fatalf("allow_env: [] still resolves the item: %+v %v", g.Keyring, err)
	}
}

// plugin show says how many keyring items each declared one matches now.
func TestHereLinesCountsKeyringMatches(t *testing.T) {
	prevG, prevM := spawnGOOS, KeyringMatches
	t.Cleanup(func() { spawnGOOS, KeyringMatches = prevG, prevM })
	spawnGOOS = "linux"
	KeyringMatches = func(attrs map[string]string) (int, error) {
		if attrs["service"] == "tool:example.com" {
			return 3, nil
		}
		return 0, nil
	}
	here := strings.Join(HereLines(Spec{Name: "p", Manifest: Manifest{Keyring: []KeyringItem{toolItem}}}), "\n")
	if !strings.Contains(here, "service=tool:example.com (3 items match now)") {
		t.Fatalf("here lines: %s", here)
	}
}

// Red-team regressions: a declaration that would read a whole class of the
// user's keyring (a schema alone, a protocol alone) is refused at install,
// whatever its label says; so is a label a bidi override disguises.
func TestKeyringBroadSelectorRefused(t *testing.T) {
	for _, it := range []KeyringItem{
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "org.gnome.keyring.NetworkPassword"}},
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "org.freedesktop.Secret.Generic"}},
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "chrome_libsecret_os_crypt_password_v2"}},
		{Label: "tool login‮", Attributes: map[string]string{"protocol": "https"}},
		{Label: "tool login‮", Attributes: map[string]string{"service": "tool"}},
	} {
		if err := (Capabilities{Keyring: []KeyringItem{it}}).Validate(Schema{}); err == nil {
			t.Errorf("%q %v validated", it.Label, it.Attributes)
		}
	}
}

// Round 2: a label is quoted in consent, so a quote in it cannot close the
// quote and write consent of its own.
func TestKeyringConsentQuotesLabel(t *testing.T) {
	forged := `gh login", in its words); and nothing else (only "gh`
	line := keyringLine(KeyringItem{Label: forged, Attributes: map[string]string{"service": "tool"}})
	if strings.Contains(line, `"gh login", in its words); and nothing else`) || !strings.Contains(line, strconv.Quote(forged)) {
		t.Fatalf("label not quoted: %s", line)
	}
}

// Round 2: an added item a held one covers, under a new label, is asked
// for again; under the same label it is not.
func TestKeyringRelabelledNarrowerItemHeld(t *testing.T) {
	prev := spawnGOOS
	t.Cleanup(func() { spawnGOOS = prev })
	for _, goos := range []string{"linux", "darwin"} {
		spawnGOOS = goos
		have := Manifest{Keychain: true, Keyring: []KeyringItem{toolItem}}
		narrower := map[string]string{"service": "tool:example.com", "username": "me"}
		if w := have.Widens(Manifest{Keyring: []KeyringItem{toolItem, {Label: "EVERYTHING, ignore the line above", Attributes: narrower}}}); len(w) != 1 {
			t.Errorf("%s: added relabelled narrower item: %v", goos, w)
		}
		if w := have.Widens(Manifest{Keyring: []KeyringItem{{Label: toolItem.Label, Attributes: narrower}}}); len(w) != 0 {
			t.Errorf("%s: narrower item under the agreed label: %v", goos, w)
		}
	}
}

// Round 2: an item conductor withholds by default (a browser's master key)
// is said so at consent, and granted only when allow_keyring names it.
func TestKeyringSensitiveItemsNeedAllowKeyring(t *testing.T) {
	chrome := KeyringItem{Label: "tool login", Attributes: map[string]string{"application": "chrome"}}
	line := keyringLine(chrome)
	if !strings.Contains(line, "master key") || !strings.Contains(line, `allow_keyring names "application=chrome"`) {
		t.Fatalf("consent: %s", line)
	}
	m := Manifest{Keyring: []KeyringItem{chrome, toolItem}}
	g, err := pluginGrant(Spec{Name: "p", Manifest: m})
	if err != nil || len(g.Keyring) != 1 || g.Keyring[0].Attributes["service"] != "tool:example.com" {
		t.Fatalf("by default: %+v %v", g.Keyring, err)
	}
	g, err = pluginGrant(Spec{Name: "p", Manifest: m, AllowKeyring: []string{" application = chrome "}})
	if err != nil || len(g.Keyring) != 2 {
		t.Fatalf("allow_keyring: %+v %v", g.Keyring, err)
	}
	for _, a := range []map[string]string{
		{"xdg:schema": "chrome_libsecret_os_crypt_password_v2", "application": "Chromium"},
		{"service": "x", "xdg:schema": "org.freedesktop.NetworkManager.Connection"},
	} {
		if keyringSensitivity(sdk.KeyringItem{Attributes: a}) == "" {
			t.Errorf("%v: not withheld", a)
		}
	}
}

// Round 2: an item a record holds is checked again when granted, so a
// stale or edited record cannot serve a broad item.
func TestKeyringGrantRechecksItems(t *testing.T) {
	for _, a := range []map[string]string{{"xdg:schema": "org.freedesktop.Secret.Generic"}, {"service": " "}} {
		m := Manifest{Keyring: []KeyringItem{{Label: "tool login", Attributes: a}}}
		if _, err := pluginGrant(Spec{Name: "p", Manifest: m}); err == nil {
			t.Errorf("%v: granted", a)
		}
	}
}

// Round 2: a config: destination's port is a default: consent says so.
func TestConsentConfigDestinationPort(t *testing.T) {
	got := strings.Join(Manifest{Egress: []string{"config:credential_hosts:443"}}.Lines(), "\n")
	if !strings.Contains(got, "<credential_hosts from its config> (on its own port, else 443)") {
		t.Fatalf("consent: %s", got)
	}
}

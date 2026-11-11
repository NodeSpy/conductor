package plugin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

var toolItem = KeyringItem{Label: "tool login for example.com", Attributes: map[string]string{"service": "tool:example.com"}}

func TestKeyringConsentLine(t *testing.T) {
	m := Manifest{Keyring: []KeyringItem{toolItem}}
	want := "reads its tool login for example.com from your keyring (the item service=tool:example.com)"
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
		{"a narrower item", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{narrower}}, false},
		{"a wider item", Manifest{Keyring: []KeyringItem{narrower}}, Manifest{Keyring: []KeyringItem{toolItem}}, true},
		{"a new label alone", Manifest{Keyring: []KeyringItem{toolItem}}, Manifest{Keyring: []KeyringItem{relabelled}}, false},
	} {
		if w := tc.have.Widens(tc.next); (len(w) > 0) != tc.widens {
			t.Errorf("%s: widens %v, want widening=%v", tc.name, w, tc.widens)
		}
	}
	if r := (Manifest{Keyring: []KeyringItem{toolItem}}).Widens(Manifest{}); len(r) != 0 {
		t.Errorf("dropping an item widens: %v", r)
	}
	if r := (Manifest{}).Widens(Manifest{Keyring: []KeyringItem{toolItem}}); len(r) != 1 || !strings.Contains(r[0], "from your keyring") {
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
	if got := strings.Join(res[0].Widens, "; "); got != "reads its tool login for example.com from your keyring (the item service=tool:example.com)" {
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
			if tc.held && !strings.Contains(strings.Join(p[0].Widens, ";"), "from your keyring") {
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
// profile grants it, as keychain: true does.
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
	}{{Manifest{}, false}, {Manifest{Keyring: []KeyringItem{toolItem}}, true}, {Manifest{Keychain: true}, true}} {
		cmd, cleanup, _, err := buildCommand(Spec{Name: "p", Kind: KindConnector, BinPath: bin, Local: true, Manifest: tc.m}, sd)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if got := strings.Contains(cmd.Args[2], filepath.Join(home, "Library/Keychains")); got != tc.want {
			t.Errorf("%+v: Keychain granted=%v, want %v", tc.m, got, tc.want)
		}
	}
}

// A referencing item is shown as its reference, as a config: path is.
func TestKeyringConsentLineReferences(t *testing.T) {
	m := Manifest{Keyring: []KeyringItem{
		{Label: "tool login for your server", Attributes: map[string]string{"service": "tool:${host:config:api_base}"}},
		{Label: "registry login", Attributes: map[string]string{"server": "${config:registries}", "kind": "1"}},
		{Label: "tool login for $TOOL_HOST", Attributes: map[string]string{"service": "tool:${env:TOOL_HOST}"}},
	}}
	got := strings.Join(m.Lines(), "\n")
	for _, want := range []string{
		"reads its tool login for your server from your keyring (the item service=tool:<host of api_base from its config>)",
		"reads its registry login from your keyring (the item kind=1, server=<registries from its config>)",
		"reads its tool login for $TOOL_HOST from your keyring (the item service=tool:$TOOL_HOST)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("consent lines:\n%s\nwant a line %q", got, want)
		}
	}
}

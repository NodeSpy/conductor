package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Regression tests for the consent half of the plugin jail's adversarial
// review: what is recorded, compared and held.

var acmeTrust = &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

// jiraDecl describes a jira plugin declaring caps.
func jiraDecl(caps Capabilities) DescribeFunc {
	return func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: caps}, nil
	}
}

// withInstance makes ref a connector referenced by one connectors: entry,
// the shape config.PluginRefs gives (reconcileInstances).
func withInstance(ref config.PluginRef) config.PluginRef {
	ref.Instances = map[string]config.ConnectorGrant{ref.Name: {Use: ref.Use}}
	return ref
}

var jiraWithLogin = Capabilities{Egress: []string{"acme.atlassian.net:443"}, Read: []string{"~/.config/jira"}}

// saveAsOlderConductor rewrites st's state file the way a conductor without
// the jail saves it: version 1, no schema marker, no held updates, and only
// the fields it records (egress, commands, fs, env, auth, single_process).
// It leaves the consent copy alone, as such a conductor does.
func saveAsOlderConductor(t *testing.T, st *InstallState) {
	t.Helper()
	old := struct {
		Version int         `yaml:"version"`
		Plugins []Installed `yaml:"plugins"`
	}{Version: 1}
	for _, p := range st.Plugins {
		p.ManifestSchema, p.Legacy = 0, false
		p.Manifest.Listen, p.Manifest.Read, p.Manifest.Write, p.Manifest.Exec, p.Manifest.Keychain = nil, nil, nil, nil, false
		old.Plugins = append(old.Plugins, p)
	}
	b, err := yaml.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), installStateFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// neverJailed is saveAsOlderConductor on a box that never ran the jail:
// there is no consent copy.
func neverJailed(t *testing.T, st *InstallState) {
	t.Helper()
	saveAsOlderConductor(t, st)
	if err := os.Remove(filepath.Join(st.Dir(), consentFile)); err != nil {
		t.Fatal(err)
	}
}

// shapes are the two reconcile paths: a single reference (runtimes,
// engines) and a connector's per-instance explode.
var shapes = []struct {
	name string
	ref  func(config.PluginRef) config.PluginRef
}{{"runtime-style ref", func(r config.PluginRef) config.PluginRef { return r }}, {"connector instances", withInstance}}

// A record written by a conductor without the jail has no read/listen/
// keychain fields and no schema marker. The next pass — even boot's
// gaps-only one — describes the same build once and records it in full,
// without a hold: it ran unconfined until now, so this narrows it.
func TestLegacyRecordIsRecordedInFullWithoutHold(t *testing.T) {
	for _, shape := range []struct {
		name string
		ref  func(config.PluginRef) config.PluginRef
	}{{"runtime-style ref", func(r config.PluginRef) config.PluginRef { return r }}, {"connector instances", withInstance}} {
		t.Run(shape.name, func(t *testing.T) {
			st := stateAt(t)
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			desc := jiraDecl(jiraWithLogin)
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: desc}); err != nil {
				t.Fatal(err)
			}
			rec, _ := st.Get("connectors/jira")
			if rec.ManifestSchema != manifestSchema {
				t.Fatalf("a fresh record must carry the schema marker: %+v", rec)
			}
			// What the conductor before the jail recorded.
			neverJailed(t, st)
			st = LoadInstallState(st.Dir())

			var logged []string
			res, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: desc,
				Log: func(f string, a ...any) { logged = append(logged, f) }})
			if err != nil {
				t.Fatal(err)
			}
			now, _ := LoadInstallState(st.Dir()).Get("connectors/jira")
			if strings.Join(now.Manifest.Read, ",") != "~/.config/jira" || now.ManifestSchema != manifestSchema {
				t.Fatalf("boot did not record the full declaration: %+v", now)
			}
			if now.Sha256 != rec.Sha256 || now.Resolved != rec.Resolved {
				t.Fatalf("re-recording must keep the same build: %+v", now)
			}
			if len(st.PendingUpdates()) != 0 || res[0].Action == ActionHeld {
				t.Fatalf("a legacy record must be re-recorded without a hold: %+v / %+v", res, st.PendingUpdates())
			}
			// Once: the next pass does not describe it again.
			calls := 0
			counting := func(ctx context.Context, s Spec) (*Decl, error) { calls++; return desc(ctx, s) }
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: counting}); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("a recorded build was described again on a gaps-only pass (%d)", calls)
			}
		})
	}
}

// `plugin update <name>` on a build that is already current re-describes it:
// one that now declares more than its record is held for approval (so the
// "`conductor plugin update` records it for approval" hint is true), and
// approving it records the wider declaration for the same build.
func TestNamedUpdateHoldsACurrentBuildThatDeclaresMore(t *testing.T) {
	for _, shape := range []struct {
		name string
		ref  func(config.PluginRef) config.PluginRef
	}{{"runtime-style ref", func(r config.PluginRef) config.PluginRef { return r }}, {"connector instances", withInstance}} {
		t.Run(shape.name, func(t *testing.T) {
			st := stateAt(t)
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			narrow := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}})
			api := stubFor("jira", "jira/v1.0.0")
			if _, err := Reconcile(refs, st, acmeTrust, api, Options{Describe: narrow}); err != nil {
				t.Fatal(err)
			}
			before, _ := st.Get("connectors/jira")

			wide := jiraDecl(jiraWithLogin)
			// A plain update pass (the auto-update) does not re-describe.
			if res, _ := Reconcile(refs, st, acmeTrust, api, Options{Describe: wide}); res[0].Action != ActionCurrent {
				t.Fatalf("plain pass: %+v", res)
			}
			res, err := Reconcile(refs, st, acmeTrust, api, Options{Describe: wide, Only: "jira"})
			if err != nil {
				t.Fatal(err)
			}
			if res[0].Action != ActionHeld || !strings.Contains(strings.Join(res[0].Widens, ";"), "~/.config/jira") {
				t.Fatalf("named update of a current build that declares more: %+v, want held naming the read", res[0])
			}
			st = LoadInstallState(st.Dir())
			if now, _ := st.Get("connectors/jira"); len(now.Manifest.Read) != 0 {
				t.Fatalf("a held declaration must not be granted: %+v", now.Manifest)
			}
			p := st.PendingUpdates()
			if len(p) != 1 || p[0].Resolved != before.Resolved || p[0].Replaces != before.Resolved || p[0].Sha256 != before.Sha256 {
				t.Fatalf("pending must be the same build with the wider declaration: %+v", p)
			}
			if _, err := st.Approve(ApproveArg("jira", st.PendingUpdates()[0].Installed)); err != nil {
				t.Fatal(err)
			}
			if now, _ := st.Get("connectors/jira"); strings.Join(now.Manifest.Read, ",") != "~/.config/jira" || now.Sha256 != before.Sha256 {
				t.Fatalf("approval must record the declaration for the same build: %+v", now)
			}
		})
	}
}

// A held update survives a reload of install state: `conductor plugin
// approve` runs in its own process and must find it.
func TestPendingUpdateSurvivesReload(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}})}); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0", "jira/v2.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
		t.Fatal(err)
	}
	reloaded := LoadInstallState(st.Dir())
	if p := reloaded.PendingUpdates(); len(p) != 1 || p[0].Resolved != "jira/v2.0.0" {
		t.Fatalf("pending after reload: %+v", p)
	}
	if _, err := reloaded.Approve("jira@v2.0.0"); err != nil {
		t.Fatalf("approve from a fresh load: %v", err)
	}
}

// A bare declared host is host:443, on both sides of the comparison: v2
// asking for other ports on it is a widening.
func TestBareHostDoesNotCoverOtherPortsWhenComparing(t *testing.T) {
	m := Manifest{Egress: []string{"api.example.com"}}
	next := Manifest{Egress: []string{"api.example.com:22", "api.example.com:6379"}}
	if w := m.Widens(next); len(w) != 2 {
		t.Fatalf("widens = %v, want both new ports", w)
	}
	if w := m.Widens(Manifest{Egress: []string{"api.example.com:443"}}); len(w) != 0 {
		t.Fatalf("api.example.com:443 is what api.example.com grants: %v", w)
	}
	if w := (Manifest{Egress: []string{"api.example.com:443"}}).Widens(m); len(w) != 0 {
		t.Fatalf("api.example.com is api.example.com:443: %v", w)
	}
}

// The OAuth endpoints conductor posts credentials to are part of what is
// compared: moving one to another host is a widening.
func TestAuthEndpointChangeWidens(t *testing.T) {
	a := Manifest{Auth: &AuthSpec{TokenURL: "https://oauth.vendor.example/token"}}
	b := Manifest{Auth: &AuthSpec{TokenURL: "https://collector.attacker.example/token"}}
	w := a.Widens(b)
	if len(w) != 1 || !strings.Contains(w[0], "collector.attacker.example") {
		t.Fatalf("widens = %v, want the new token endpoint", w)
	}
	if w := a.Widens(Manifest{Auth: &AuthSpec{TokenURL: "https://OAUTH.vendor.example/v2/token"}}); len(w) != 0 {
		t.Fatalf("same host, another path: %v", w)
	}
	if w := (Manifest{}).Widens(a); len(w) != 1 {
		t.Fatalf("declaring managed auth for the first time: %v", w)
	}
	if !strings.Contains(strings.Join(a.Lines(), "\n"), "oauth.vendor.example") {
		t.Fatalf("the consent lines must name where credentials go: %v", a.Lines())
	}
}

// A changed constraint that lands on a version not installed yet is compared
// with the build already installed: a wider one is held, not installed.
func TestConstraintChangeToAWiderBuildIsHeld(t *testing.T) {
	st := stateAt(t)
	api := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	r1 := refFor(t, config.UseKindConnector, "acme/plugins/jira@^1")
	if _, err := Reconcile(map[string]config.PluginRef{r1.Key(): r1}, st, acmeTrust, api, Options{Describe: jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}})}); err != nil {
		t.Fatal(err)
	}
	r2 := refFor(t, config.UseKindConnector, "acme/plugins/jira@^2")
	wide := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}, Read: []string{"~/.ssh"}, Commands: []string{"sh"}})
	res, err := Reconcile(map[string]config.PluginRef{r2.Key(): r2}, st, acmeTrust, api, Options{Describe: wide})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Action != ActionHeld || len(res[0].Widens) != 2 {
		t.Fatalf("constraint ^1 -> ^2 with a wider build: %+v, want held", res[0])
	}
	if _, ok := st.GetForConstraint("connectors/jira", r2.Use); ok {
		t.Fatal("the held build must not be installed for the new constraint")
	}
	if p := st.PendingUpdates(); len(p) != 1 || p[0].Resolved != "jira/v2.0.0" || p[0].Replaces != "jira/v1.0.0" {
		t.Fatalf("pending: %+v", p)
	}
}

// Consent names the variables that look like credentials.
func TestConsentNamesSecretLookingEnv(t *testing.T) {
	lines := strings.Join(Manifest{Env: []string{"LOG_LEVEL", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY"}}.Lines(), "\n")
	if !strings.Contains(lines, "GH_TOKEN, AWS_SECRET_ACCESS_KEY look like credentials") || strings.Contains(lines, "LOG_LEVEL look") {
		t.Fatalf("lines:\n%s", lines)
	}
}

// An update whose describe fails leaves nothing behind that a later pass
// could take for a record an older conductor wrote: the installed build
// keeps running, and the update is compared (and held) once it describes.
func TestUndescribedUpdateIsNeverAdopted(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			st := stateAt(t)
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}})}); err != nil {
				t.Fatal(err)
			}
			api2 := stubFor("jira", "jira/v1.0.0", "jira/v1.1.0")
			api2.bin = []byte("#!/bin/sh\necho plugin v2\n")
			failing := func(context.Context, Spec) (*Decl, error) { return nil, errors.New("describe timed out") }
			res, err := Reconcile(refs, st, acmeTrust, api2, Options{Describe: failing, Prune: true})
			if err != nil {
				t.Fatal(err)
			}
			if res[0].Action != ActionFailed || res[0].Tag != "jira/v1.0.0" {
				t.Fatalf("failed describe: %+v, want failed, reporting the installed build", res[0])
			}
			st = LoadInstallState(st.Dir())
			if all := st.AllVersions("connectors/jira"); len(all) != 1 || all[0].Resolved != "jira/v1.0.0" {
				t.Fatalf("after a failed describe the state must hold only the installed build: %+v", all)
			}
			wide := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443", "evil.example.com:443"}, Read: []string{"~/.ssh"}})
			var logs []string
			logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
			res, _ = Reconcile(refs, st, acmeTrust, api2, Options{GapsOnly: true, Describe: wide, Log: logf})
			if res[0].Tag != "jira/v1.0.0" || strings.Contains(strings.Join(logs, "\n"), "predates") {
				t.Fatalf("boot after a failed describe: %+v, logs %v", res[0], logs)
			}
			res, _ = Reconcile(refs, st, acmeTrust, api2, Options{Describe: wide})
			if res[0].Action != ActionHeld || len(res[0].Widens) != 2 {
				t.Fatalf("the update once it describes: %+v, want held", res[0])
			}
			if cur, _ := st.GetForConstraint("connectors/jira", ref.Use); cur.Resolved != "jira/v1.0.0" || len(cur.Manifest.Read) != 0 {
				t.Fatalf("runs %+v", cur)
			}
		})
	}
}

// A release whose asset is replaced under a tag that is installed (a new
// sha) is refused: the installed build, and its bytes on disk, are kept.
// --force takes it, compared against the build it replaces: held when it
// declares more.
func TestRepublishedReleaseIsRefused(t *testing.T) {
	for _, shape := range shapes {
		for _, second := range []string{"acme/plugins/jira", "acme/plugins/jira@^2"} {
			t.Run(shape.name+" then "+second, func(t *testing.T) {
				st := stateAt(t)
				narrow := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}})
				wide := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}, Read: []string{"~/.ssh"}, Commands: []string{"sh"}})
				r1 := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
				if _, err := Reconcile(map[string]config.PluginRef{r1.Key(): r1}, st, acmeTrust, stubFor("jira", "jira/v2.0.0"), Options{Describe: narrow}); err != nil {
					t.Fatal(err)
				}
				before, _ := st.Get("connectors/jira")
				r2 := shape.ref(refFor(t, config.UseKindConnector, second))
				refs := map[string]config.PluginRef{r2.Key(): r2}
				a2 := stubFor("jira", "jira/v2.0.0")
				a2.bin = []byte("#!/bin/sh\necho republished\n")
				res, err := Reconcile(refs, st, acmeTrust, a2, Options{Describe: wide})
				if err != nil {
					t.Fatal(err)
				}
				var rp *republishedError
				if res[0].Action != ActionFailed || !errors.As(res[0].Err, &rp) {
					t.Fatalf("re-published asset: %+v, want refused", res[0])
				}
				cur, _ := LoadInstallState(st.Dir()).GetForConstraint("connectors/jira", r2.Use)
				if cur.Sha256 != before.Sha256 || len(cur.Manifest.Read) != 0 {
					t.Fatalf("runs %+v, want the installed build", cur)
				}
				if got, _ := fileSha256(cur.Path); got != before.Sha256 {
					t.Fatal("the installed build's bytes were replaced")
				}
				res, err = Reconcile(refs, st, acmeTrust, a2, Options{Describe: wide, Force: true})
				if err != nil {
					t.Fatal(err)
				}
				if res[0].Action != ActionHeld || len(res[0].Widens) != 2 {
					t.Fatalf("--force on a wider re-published asset: %+v, want held", res[0])
				}
				cur, _ = st.GetForConstraint("connectors/jira", r2.Use)
				if cur.Sha256 != before.Sha256 || len(cur.Manifest.Read) != 0 {
					t.Fatalf("held, the record must stay what was agreed to: %+v", cur)
				}
				if p := st.PendingUpdates(); len(p) != 1 || p[0].Sha256 == before.Sha256 {
					t.Fatalf("pending: %+v", p)
				}
			})
		}
	}
}

// Only a record loaded from an older conductor's (version 1) state file is
// adopted. A record this conductor wrote with nothing recorded is compared
// like any other.
func TestOnlyAnOlderConductorsRecordIsAdopted(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			st := stateAt(t)
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(Capabilities{})}); err != nil {
				t.Fatal(err)
			}
			rec, _ := st.Get("connectors/jira")
			rec.ManifestSchema = 0
			st.Put(rec)
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
			st = LoadInstallState(st.Dir())
			res, _ := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: jiraDecl(jiraWithLogin)})
			if cur, _ := st.Get("connectors/jira"); len(cur.Manifest.Read) != 0 || res[0].Manifest.Read != nil {
				t.Fatalf("a record in a version 2 file was adopted: %+v", cur)
			}
			res, _ = Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin), Only: "jira"})
			if res[0].Action != ActionHeld {
				t.Fatalf("named update: %+v, want held", res[0])
			}
		})
	}
}

// Adopting an older conductor's record takes only what it could not record
// (listen, read, write, exec, keychain) from the build without a hold. What
// it did record is compared: a build that now declares more egress, env,
// commands or OAuth endpoints is held, and runs jailed to the record.
func TestAdoptionComparesWhatTheOlderConductorRecorded(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			st := stateAt(t)
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443"}, Env: []string{"JIRA_HOST"}})}); err != nil {
				t.Fatal(err)
			}
			neverJailed(t, st)
			st = LoadInstallState(st.Dir())
			now := jiraDecl(Capabilities{Egress: []string{"acme.atlassian.net:443", "collector.example:443"}, Env: []string{"JIRA_HOST"}, Read: []string{"~/.config/jira"}, Listen: []string{":9000"}})
			res, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: now})
			if err != nil {
				t.Fatal(err)
			}
			cur, _ := st.Get("connectors/jira")
			if strings.Join(cur.Manifest.Read, ",") != "~/.config/jira" || strings.Join(cur.Manifest.Listen, ",") != ":9000" || strings.Join(cur.Manifest.Egress, ",") != "acme.atlassian.net:443" {
				t.Fatalf("adopted record: %+v", cur.Manifest)
			}
			if cur.Legacy || cur.ManifestSchema != manifestSchema {
				t.Fatalf("adopted record must be marked: %+v", cur)
			}
			p := st.PendingUpdates()
			if len(p) != 1 || strings.Join(p[0].Widens, ";") != "connects to collector.example:443" {
				t.Fatalf("pending %+v, want the new egress held", p)
			}
			_ = res
		})
	}
}

// The plugin SDK withholds config: and env: entries from a conductor
// without the jail (it cannot resolve them), so its record lacks them.
// Adopting it takes them from the build without a hold, as it takes what
// that conductor could not record; anything else the build declares beyond
// the record is still held.
func TestAdoptionTakesWithheldReferencesWithoutHold(t *testing.T) {
	full := Capabilities{
		Egress: []string{"acme.atlassian.net:443", "config:base_url"},
		Read:   []string{"?env:JIRA_HOME/conf"},
		Env:    []string{"JIRA_HOME"},
	}
	describe := func(caps Capabilities) DescribeFunc {
		return func(context.Context, Spec) (*Decl, error) {
			return &Decl{Kind: KindConnector, Type: "jira", Connection: sdk.Schema{"base_url": {Type: "string"}}, Capabilities: caps}, nil
		}
	}
	withheld := Capabilities{Egress: []string{"acme.atlassian.net:443"}, Env: full.Env}
	for _, tc := range []struct {
		name     string
		recorded Capabilities // what the SDK gave the older conductor
		now      Capabilities
		held     string
	}{
		{"static entries left", withheld, full, ""},
		{"config: entries only, so unspecified", Capabilities{}, Capabilities{Egress: []string{"config:base_url"}}, ""},
		{"recorded in full by an older SDK", full, full, ""},
		{"a new static destination is still held", withheld,
			Capabilities{Egress: append([]string{"collector.example:443"}, full.Egress...), Read: full.Read, Env: full.Env}, "connects to collector.example:443"},
	} {
		for _, shape := range shapes {
			t.Run(tc.name+"/"+shape.name, func(t *testing.T) {
				st := stateAt(t)
				ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
				refs := map[string]config.PluginRef{ref.Key(): ref}
				if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: describe(tc.recorded)}); err != nil {
					t.Fatal(err)
				}
				neverJailed(t, st)
				st = LoadInstallState(st.Dir())
				res, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: describe(tc.now)})
				if err != nil {
					t.Fatal(err)
				}
				cur, _ := st.Get("connectors/jira")
				p := st.PendingUpdates()
				if tc.held == "" {
					if len(p) != 0 || res[0].Action == ActionHeld {
						t.Fatalf("held %+v; withheld references must be adopted without a hold", p)
					}
					if strings.Join(cur.Manifest.Egress, ",") != strings.Join(tc.now.Egress, ",") || strings.Join(cur.Manifest.Read, ",") != strings.Join(tc.now.Read, ",") {
						t.Fatalf("adopted %+v, want the full declaration %+v", cur.Manifest, tc.now)
					}
					return
				}
				if len(p) != 1 || strings.Join(p[0].Widens, ";") != tc.held {
					t.Fatalf("pending %+v, want only %q held", p, tc.held)
				}
				if strings.Join(cur.Manifest.Egress, ",") != strings.Join(full.Egress, ",") {
					t.Fatalf("while held it runs jailed to the record and the withheld references: %+v", cur.Manifest.Egress)
				}
			})
		}
	}
}

// A jailing conductor the plugin SDK withheld config: and env: entries and
// expose_local from (v0.88 to v0.90) recorded the build without them. The
// first pass of a conductor that grants them takes them from the build
// without a hold, once; anything else the build declares beyond the record
// is still held. A record this conductor wrote is never re-described.
func TestJailSchemaRecordTakesWithheldWithoutHold(t *testing.T) {
	full := Capabilities{
		Egress:      []string{"acme.atlassian.net:443", "config:base_url"},
		Listen:      []string{"config:listen"},
		Env:         []string{"JIRA_HOME"},
		ExposeLocal: true,
	}
	withheld := Capabilities{Egress: []string{"acme.atlassian.net:443"}, Env: full.Env}
	for _, tc := range []struct {
		name   string
		schema int
		now    Capabilities
		held   string
		same   bool
	}{
		{"withheld entries taken", jailSchema, full, "", false},
		{"a new static destination is still held", jailSchema,
			Capabilities{Egress: append([]string{"collector.example:443"}, full.Egress...), Listen: full.Listen, Env: full.Env, ExposeLocal: true}, "connects to collector.example:443", false},
		{"this conductor's record is not re-described", manifestSchema, full, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			describes := 0
			describe := func(caps Capabilities) DescribeFunc {
				return func(context.Context, Spec) (*Decl, error) {
					describes++
					return &Decl{Kind: KindConnector, Type: "jira", Connection: sdk.Schema{"base_url": {Type: "string"}, "listen": {Type: "string"}}, Capabilities: caps}, nil
				}
			}
			st := stateAt(t)
			ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
			refs := map[string]config.PluginRef{ref.Key(): ref}
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: describe(withheld)}); err != nil {
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
			var logs []string
			res, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: describe(tc.now), Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
			if err != nil {
				t.Fatal(err)
			}
			cur, _ := st.Get("connectors/jira")
			if tc.same {
				if describes != 0 || !reflect.DeepEqual(cur.Manifest, rec.Manifest) {
					t.Fatalf("re-described (%d) or changed: %+v", describes, cur.Manifest)
				}
				return
			}
			if cur.ManifestSchema != manifestSchema {
				t.Fatalf("not marked: %+v", cur)
			}
			p := st.PendingUpdates()
			if tc.held == "" {
				if len(p) != 0 || res[0].Action == ActionHeld {
					t.Fatalf("held %+v", p)
				}
				if !reflect.DeepEqual(cur.Manifest, manifestFromDecl(&Decl{Capabilities: full})) {
					t.Fatalf("recorded %+v, want the full declaration", cur.Manifest)
				}
				if !slices.ContainsFunc(logs, func(l string) bool {
					return strings.Contains(l, "could not record") && strings.Contains(l, "listens on")
				}) {
					t.Errorf("what was taken is not logged: %q", logs)
				}
			} else {
				if len(p) != 1 || strings.Join(p[0].Widens, ";") != tc.held {
					t.Fatalf("pending %+v, want only %q held", p, tc.held)
				}
				if !slices.Equal(cur.Manifest.Egress, full.Egress) || !slices.Equal(cur.Manifest.Listen, full.Listen) || !cur.Manifest.ExposeLocal {
					t.Fatalf("while held it runs jailed to the record and the withheld entries: %+v", cur.Manifest)
				}
			}
			describes = 0
			if _, err := Reconcile(refs, LoadInstallState(st.Dir()), acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: describe(tc.now)}); err != nil {
				t.Fatal(err)
			}
			if describes != 0 {
				t.Errorf("adopted twice")
			}
		})
	}
}

// After a rollback to a conductor without the jail and an upgrade back, the
// adoption compares against what the user agreed to (the consent copy the
// older conductor does not touch), the held update it dropped is restored,
// and a build it installed meanwhile that declares more than the user last
// agreed to is reported loudly.
func TestRollbackAndUpgradeKeepsConsent(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	api2 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	api2.bin = []byte("#!/bin/sh\necho v2\n")
	wider := Capabilities{Egress: jiraWithLogin.Egress, Read: []string{"~/.config/jira", "~/.ssh"}}
	logged := func(logs *[]string) func(string, ...any) {
		return func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) }
	}

	t.Run("held update and agreed record restored", func(t *testing.T) {
		st := stateAt(t)
		if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
			t.Fatal(err)
		}
		if res, _ := Reconcile(refs, st, acmeTrust, api2, Options{Describe: jiraDecl(wider)}); res[0].Action != ActionHeld {
			t.Fatalf("v2: %+v, want held", res[0])
		}
		saveAsOlderConductor(t, st)
		st = LoadInstallState(st.Dir())
		if p := st.PendingUpdates(); len(p) != 1 || p[0].Resolved != "jira/v2.0.0" {
			t.Fatalf("the held update must be restored from the consent copy: %+v", p)
		}
		Reconcile(refs, st, acmeTrust, api2, Options{GapsOnly: true, Describe: jiraDecl(jiraWithLogin)})
		cur, _ := st.Get("connectors/jira")
		if cur.Resolved != "jira/v1.0.0" || strings.Join(cur.Manifest.Read, ",") != "~/.config/jira" || cur.Legacy {
			t.Fatalf("adopted %+v", cur)
		}
		if p := st.PendingUpdates(); len(p) != 1 || p[0].Resolved != "jira/v2.0.0" {
			t.Fatalf("pending after adoption: %+v", p)
		}
	})

	t.Run("same build declaring more than agreed is held", func(t *testing.T) {
		st := stateAt(t)
		if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
			t.Fatal(err)
		}
		saveAsOlderConductor(t, st)
		st = LoadInstallState(st.Dir())
		res, _ := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{GapsOnly: true, Describe: jiraDecl(wider)})
		cur, _ := st.Get("connectors/jira")
		if strings.Join(cur.Manifest.Read, ",") != "~/.config/jira" || strings.Join(res[0].Manifest.Read, ",") != "~/.config/jira" {
			t.Fatalf("adopted %+v, want what the user agreed to for this build", cur.Manifest)
		}
		if p := st.PendingUpdates(); len(p) != 1 || !strings.Contains(strings.Join(p[0].Widens, ";"), "~/.ssh") {
			t.Fatalf("pending %+v", p)
		}
	})

	t.Run("build installed during the rollback is reported", func(t *testing.T) {
		st := stateAt(t)
		if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
			t.Fatal(err)
		}
		agreed, err := os.ReadFile(filepath.Join(st.Dir(), consentFile))
		if err != nil {
			t.Fatal(err)
		}
		// The older conductor installs v2 (here: fetched, then the
		// records rewritten as it would leave them).
		if res, _ := Reconcile(refs, st, acmeTrust, api2, Options{Describe: jiraDecl(jiraWithLogin), Prune: true}); res[0].Tag != "jira/v2.0.0" {
			t.Fatalf("v2: %+v", res[0])
		}
		if err := os.WriteFile(filepath.Join(st.Dir(), consentFile), agreed, 0o600); err != nil {
			t.Fatal(err)
		}
		saveAsOlderConductor(t, st)
		st = LoadInstallState(st.Dir())
		var logs []string
		Reconcile(refs, st, acmeTrust, api2, Options{GapsOnly: true, Describe: jiraDecl(wider), Log: logged(&logs)})
		all := strings.Join(logs, "\n")
		if !strings.Contains(all, "WARNING: jira/v2.0.0 was installed by a conductor without the plugin jail") || !strings.Contains(all, "~/.ssh") {
			t.Fatalf("logs %v, want the widening over jira/v1.0.0 reported", logs)
		}
	})
}

// A real rollback: the older conductor does not read a version 2 state file,
// sees no installs, and installs the newest release — the update this
// conductor held. Forward again, that build is compared against what the
// user last agreed to (the consent copy), not against the record the older
// conductor wrote for it: it is held again, runs jailed to the agreement,
// and the log says so.
func TestRollbackReinstalledHeldBuildIsHeldAgain(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			ref := shape.ref(refFor(t, config.UseKindConnector, "acme/plugins/jira"))
			refs := map[string]config.PluginRef{ref.Key(): ref}
			api2 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
			api2.bin = []byte("#!/bin/sh\necho v2\n")
			wider := Capabilities{Egress: append([]string{"evil.example.com:443"}, jiraWithLogin.Egress...), Read: []string{"~/.config/jira", "~/.ssh"}}
			st := stateAt(t)
			if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
				t.Fatal(err)
			}
			if res, _ := Reconcile(refs, st, acmeTrust, api2, Options{Describe: jiraDecl(wider)}); res[0].Action != ActionHeld {
				t.Fatalf("v2: %+v, want held", res[0])
			}
			agreed, err := os.ReadFile(filepath.Join(st.Dir(), consentFile))
			if err != nil {
				t.Fatal(err)
			}
			// The older conductor: no state it can read; it installs the
			// newest release and records what it recorded (its egress).
			old := &InstallState{dir: st.Dir()}
			if res, _ := Reconcile(refs, old, acmeTrust, api2, Options{Describe: jiraDecl(wider)}); res[0].Tag != "jira/v2.0.0" {
				t.Fatalf("rollback: %+v, want jira/v2.0.0 installed", res[0])
			}
			if err := os.WriteFile(filepath.Join(st.Dir(), consentFile), agreed, 0o600); err != nil {
				t.Fatal(err)
			}
			saveAsOlderConductor(t, old)

			st = LoadInstallState(st.Dir())
			var logs []string
			Reconcile(refs, st, acmeTrust, api2, Options{GapsOnly: true, Describe: jiraDecl(wider), Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
			cur, _ := st.Get("connectors/jira")
			if s := cur.Manifest.Summary(); strings.Contains(s, "~/.ssh") || strings.Contains(s, "evil.example.com") {
				t.Fatalf("runs %s with %s, want what the user agreed to for jira/v1.0.0", cur.Resolved, s)
			}
			p := st.PendingUpdates()
			if len(p) != 1 || p[0].Resolved != "jira/v2.0.0" || p[0].Replaces != "jira/v1.0.0" {
				t.Fatalf("pending %+v, want jira/v2.0.0 held against jira/v1.0.0", p)
			}
			for _, want := range []string{"~/.ssh", "evil.example.com:443"} {
				if !strings.Contains(strings.Join(p[0].Widens, ";"), want) {
					t.Errorf("widens %v lacks %s", p[0].Widens, want)
				}
			}
			all := strings.Join(logs, "\n")
			for _, want := range []string{"WARNING: jira/v2.0.0 was installed by a conductor without the plugin jail", "HELD: it runs jailed to what you agreed to for jira/v1.0.0", "plugin approve " + ApproveArg("jira", p[0].Installed)} {
				if !strings.Contains(all, want) {
					t.Errorf("logs lack %q:\n%s", want, all)
				}
			}
		})
	}
}

// `plugin approve` accepts the build the user was shown, named by its sha
// or tag: an update held after the listing (the periodic pass replaced the
// held one with a newer, wider build) is not approved by an approval of
// the one listed, and a bare name approves nothing.
func TestApproveAcceptsOnlyTheListedBuild(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	st := stateAt(t)
	if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(Capabilities{Egress: jiraWithLogin.Egress})}); err != nil {
		t.Fatal(err)
	}
	api2 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	api2.bin = []byte("#!/bin/sh\necho v2\n")
	Reconcile(refs, st, acmeTrust, api2, Options{Describe: jiraDecl(jiraWithLogin)})
	listed := st.PendingUpdates()
	if len(listed) != 1 || listed[0].Resolved != "jira/v2.0.0" {
		t.Fatalf("pending %+v", listed)
	}
	if _, err := st.Approve("jira"); err == nil || !strings.Contains(err.Error(), ApproveArg("jira", listed[0].Installed)) {
		t.Fatalf("bare name: %v, want refused naming the build to approve", err)
	}

	// The periodic pass holds a newer build that asks for even more.
	api3 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0", "jira/v3.0.0")
	api3.bin = []byte("#!/bin/sh\necho v3\n")
	Reconcile(refs, st, acmeTrust, api3, Options{Describe: jiraDecl(Capabilities{Egress: jiraWithLogin.Egress, Read: []string{"~/.config/jira", "~/.ssh"}})})
	if p := st.PendingUpdates(); len(p) != 1 || p[0].Resolved != "jira/v3.0.0" {
		t.Fatalf("pending %+v, want jira/v3.0.0 held in its place", p)
	}
	for _, arg := range []string{ApproveArg("jira", listed[0].Installed), "jira@jira/v2.0.0", "jira@v2.0.0"} {
		if _, err := st.Approve(arg); err == nil || !strings.Contains(err.Error(), "is not the update waiting") {
			t.Errorf("approve %s: %v, want refused", arg, err)
		}
	}
	if cur, _ := st.Get("connectors/jira"); cur.Resolved != "jira/v1.0.0" {
		t.Fatalf("installed %s, want jira/v1.0.0 kept", cur.Resolved)
	}
	shown := st.PendingUpdates()[0]
	done, err := st.Approve(ApproveArg("jira", shown.Installed))
	if err != nil || len(done) != 1 || done[0].Sha256 != shown.Sha256 {
		t.Fatalf("approve the shown build: %+v %v", done, err)
	}
}

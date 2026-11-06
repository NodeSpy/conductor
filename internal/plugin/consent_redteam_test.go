package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
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
			rec.Manifest.Read, rec.ManifestSchema = nil, 0
			st.Put(rec)
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
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
			if _, err := st.Approve("jira"); err != nil {
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
	if _, err := reloaded.Approve("jira"); err != nil {
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

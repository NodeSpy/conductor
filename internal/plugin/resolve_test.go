package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// stateAt builds an InstallState rooted at a temp dir.
func stateAt(t *testing.T) *InstallState {
	t.Helper()
	return LoadInstallState(t.TempDir())
}

// refFor builds the derived PluginRef a `use:` reference produces.
func refFor(t *testing.T, kind config.UseKind, ref string) config.PluginRef {
	t.Helper()
	u, err := config.ParseUse(kind, ref)
	if err != nil {
		t.Fatalf("ParseUse(%q): %v", ref, err)
	}
	return config.PluginRef{Name: u.Name, Instance: u.Name, Use: u}
}

func stubFor(component string, tags ...string) stubAPI {
	return stubAPI{
		tags:      tags,
		bin:       []byte("#!/bin/sh\necho plugin\n"),
		assetName: RemoteSource{Component: component}.AssetName(),
	}
}

// The whole app-extension install path: a `use:` reference nobody has installed
// is fetched, checksum-verified, vendored under the STATE DIR (not the config
// dir, and not a committed lockfile), and recorded locally.
func TestReconcileInstallsIntoLocalState(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/conductor-plugins/jira@^1.0")
	api := stubFor("jira", "jira/v1.0.0", "jira/v1.2.0", "jira/v2.0.0")

	res, err := Reconcile(
		map[string]config.PluginRef{ref.Key(): ref}, st,
		&config.PackTrustConfig{Allow: []string{"github.com/acme/*"}},
		api, Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionInstalled {
		t.Fatalf("resolution = %+v, want one installed", res)
	}
	if res[0].Tag != "jira/v1.2.0" {
		t.Fatalf("tag = %q, want the highest 1.x", res[0].Tag)
	}

	inst, ok := st.Get("connectors/jira")
	if !ok {
		t.Fatal("install state has no record")
	}
	if inst.Sha256 != res[0].Sha || inst.Resolved != res[0].Tag {
		t.Fatalf("record %+v does not match resolution %+v", inst, res[0])
	}
	if !strings.HasPrefix(inst.Path, st.Dir()) {
		t.Fatalf("binary %q is not under the install dir %q", inst.Path, st.Dir())
	}
	if _, err := os.Stat(inst.Path); err != nil {
		t.Fatalf("installed binary missing: %v", err)
	}

	// It survives a reload: boot reads this offline, with no network.
	reloaded := LoadInstallState(st.Dir())
	if got, ok := reloaded.Get("connectors/jira"); !ok || got.Sha256 != inst.Sha256 {
		t.Fatalf("state did not round-trip: %+v", got)
	}
}

// A second pass over an unchanged release is up-to-date, not a re-install.
func TestReconcileIsIdempotent(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	api := stubFor("jira", "jira/v1.0.0")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	if _, err := Reconcile(refs, st, trust, api, Options{}); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(refs, st, trust, api, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionCurrent {
		t.Fatalf("second pass = %+v, want up-to-date", res)
	}
}

// STAY-CURRENT is the default: an unpinned reference follows the newest
// matching release, and the move is reported with the sha it came from so a
// surprise change is visible.
func TestReconcileStaysCurrent(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{}); err != nil {
		t.Fatal(err)
	}
	first, _ := st.Get("connectors/jira")

	newer := stubFor("jira", "jira/v1.0.0", "jira/v1.5.0")
	newer.bin = []byte("#!/bin/sh\necho newer\n")
	res, err := Reconcile(refs, st, trust, newer, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionUpdated {
		t.Fatalf("resolution = %+v, want updated", res)
	}
	if res[0].Tag != "jira/v1.5.0" {
		t.Fatalf("tag = %q, want jira/v1.5.0", res[0].Tag)
	}
	if res[0].PrevSha != first.Sha256 {
		t.Fatalf("PrevSha %q does not name the sha it replaced (%q)", res[0].PrevSha, first.Sha256)
	}
}

// An EXACT @version is the opt-out from stay-current.
func TestReconcileExactPinDoesNotMove(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira@v1.0.0")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{}); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0", "jira/v9.9.9"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionPinned {
		t.Fatalf("resolution = %+v, want pinned", res)
	}
	if res[0].Tag != "jira/v1.0.0" {
		t.Fatalf("a pinned plugin moved to %q", res[0].Tag)
	}
}

// BOOT posture: gaps only. What is already installed is never re-resolved on
// the hot path, and what is missing is fetched.
func TestReconcileGapsOnly(t *testing.T) {
	st := stateAt(t)
	jira := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	if _, err := Reconcile(map[string]config.PluginRef{jira.Key(): jira}, st, trust, stubFor("jira", "jira/v1.0.0"), Options{}); err != nil {
		t.Fatal(err)
	}
	res, err := Reconcile(
		map[string]config.PluginRef{jira.Key(): jira}, st, trust,
		stubFor("jira", "jira/v1.0.0", "jira/v2.0.0"), Options{GapsOnly: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionSkipped {
		t.Fatalf("resolution = %+v, want skipped on a gaps-only pass", res)
	}
	if got, _ := st.Get("connectors/jira"); got.Resolved != "jira/v1.0.0" {
		t.Fatalf("gaps-only pass moved an installed plugin to %q", got.Resolved)
	}
}

// DEGRADE-SAFE: a fetch failure keeps the installed build running and reports
// the failure instead of taking anything down.
func TestReconcileDegradesOnFetchFailure(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Get("connectors/jira")

	res, err := Reconcile(refs, st, trust, stubAPI{tagsErr: true}, Options{})
	if err != nil {
		t.Fatalf("a fetch failure must not be a hard error: %v", err)
	}
	if len(res) != 1 || res[0].Action != ActionFailed || res[0].Err == nil {
		t.Fatalf("resolution = %+v, want a recorded failure", res)
	}
	if res[0].Sha != before.Sha256 {
		t.Fatalf("degraded resolution lost the installed build: %+v", res[0])
	}
	if after, _ := st.Get("connectors/jira"); after.Sha256 != before.Sha256 {
		t.Fatal("a failed fetch overwrote install state")
	}
}

// One unreachable plugin never blocks the others.
func TestReconcileContinuesPastOneFailure(t *testing.T) {
	st := stateAt(t)
	good := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	bad := refFor(t, config.UseKindConnector, "acme/plugins/linear")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	// The stub only publishes jira tags, so linear finds no matching release.
	api := stubFor("jira", "jira/v1.0.0")
	res, err := Reconcile(
		map[string]config.PluginRef{good.Key(): good, bad.Key(): bad},
		st, trust, api, Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want a result per reference, got %+v", res)
	}
	var installed, failed int
	for _, r := range res {
		switch r.Action {
		case ActionInstalled:
			installed++
		case ActionFailed:
			failed++
		}
	}
	if installed != 1 || failed != 1 {
		t.Fatalf("want 1 installed + 1 failed, got %+v", res)
	}
}

// TRUST: the official repo needs no allowlist entry; a third-party repo does.
func TestReconcileTrustGate(t *testing.T) {
	official := refFor(t, config.UseKindConnector, "sentry") // bare name -> official repo
	third := refFor(t, config.UseKindConnector, "acme/plugins/jira")

	t.Run("official is trusted by default", func(t *testing.T) {
		st := stateAt(t)
		api := stubFor("connectors/sentry", "connectors/sentry/v1.0.0")
		res, err := Reconcile(map[string]config.PluginRef{official.Key(): official}, st, nil, api, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionInstalled {
			t.Fatalf("official repo refused with no plugin_trust block: %+v", res[0])
		}
	})

	t.Run("third-party needs an explicit entry", func(t *testing.T) {
		st := stateAt(t)
		api := stubFor("jira", "jira/v1.0.0")
		res, err := Reconcile(map[string]config.PluginRef{third.Key(): third}, st, nil, api, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionFailed {
			t.Fatalf("an untrusted third-party source was installed: %+v", res[0])
		}
		res, err = Reconcile(map[string]config.PluginRef{third.Key(): third}, st, nil, api, Options{AllowUnlisted: true})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionInstalled {
			t.Fatalf("--allow-unlisted did not override trust: %+v", res[0])
		}
	})
}

// A local development binary is never fetched, pinned, or trust-gated.
func TestReconcileLocalIsNotFetched(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "./bin/conductor-jira")
	res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, nil, stubAPI{tagsErr: true}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionLocal {
		t.Fatalf("resolution = %+v, want local", res)
	}
}

// The install-time describe records the PERMISSION MANIFEST and enforces the
// declared kind, so both are known before the plugin ever does work.
func TestReconcileRecordsManifestAndEnforcesKind(t *testing.T) {
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	t.Run("manifest is recorded at install", func(t *testing.T) {
		st := stateAt(t)
		ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
		describe := func(context.Context, Spec) (*Decl, error) {
			return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
				Egress: []string{"acme.atlassian.net:443"}, Commands: []string{"git"},
			}}, nil
		}
		res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, trust,
			stubFor("jira", "jira/v1.0.0"), Options{Describe: describe})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionInstalled {
			t.Fatalf("%+v", res[0])
		}
		got, _ := st.Get("connectors/jira")
		if len(got.Manifest.Egress) != 1 || got.Manifest.Egress[0] != "acme.atlassian.net:443" {
			t.Fatalf("manifest egress not recorded: %+v", got.Manifest)
		}
		if len(got.Manifest.Commands) != 1 || got.Manifest.Commands[0] != "git" {
			t.Fatalf("manifest commands not recorded: %+v", got.Manifest)
		}
		if !strings.Contains(got.Manifest.Summary(), "acme.atlassian.net:443") {
			t.Fatalf("manifest summary does not surface egress: %q", got.Manifest.Summary())
		}
	})

	t.Run("a runtime declared under connectors: is refused", func(t *testing.T) {
		st := stateAt(t)
		ref := refFor(t, config.UseKindConnector, "acme/plugins/sneaky")
		describe := func(context.Context, Spec) (*Decl, error) {
			return &Decl{Kind: KindRuntime, Type: "sneaky"}, nil
		}
		res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, trust,
			stubFor("sneaky", "sneaky/v1.0.0"), Options{Describe: describe})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionFailed {
			t.Fatalf("a runtime was accepted under connectors: %+v", res[0])
		}
		if !strings.Contains(res[0].Err.Error(), "cannot be wired as a connector") {
			t.Fatalf("unhelpful kind error: %v", res[0].Err)
		}
		if _, ok := st.Get("connectors/sneaky"); ok {
			t.Fatal("a kind-mismatched plugin was recorded as installed")
		}
	})

	t.Run("a plugin that reports no kind is trusted to its block", func(t *testing.T) {
		st := stateAt(t)
		ref := refFor(t, config.UseKindConnector, "acme/plugins/old")
		describe := func(context.Context, Spec) (*Decl, error) {
			return &Decl{Type: "old"}, nil // pre-Kind SDK
		}
		res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, trust,
			stubFor("old", "old/v1.0.0"), Options{Describe: describe})
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Action != ActionInstalled {
			t.Fatalf("an older plugin with no declared kind was refused: %+v", res[0])
		}
	})
}

// A reference dropped from the config drops out of install state, so the record
// does not grow forever.
func TestReconcileDropsUnreferenced(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	if _, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, trust, stubFor("jira", "jira/v1.0.0"), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(map[string]config.PluginRef{}, st, trust, stubFor("jira"), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("connectors/jira"); ok {
		t.Fatal("an unreferenced plugin stayed in install state")
	}
}

// SpecFromRef joins a derived reference with install state: a remote plugin runs
// from its installed binary, and an uninstalled one says so instead of trying
// to exec a URL.
func TestSpecFromRefUsesInstallState(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")

	missing := SpecFromRef(ref, "/cfg", Installed{}, false)
	if missing.Installed() {
		t.Fatal("an uninstalled plugin reported installed")
	}
	if !strings.Contains(missing.NotInstalledError().Error(), "conductor init") {
		t.Fatalf("unhelpful not-installed error: %v", missing.NotInstalledError())
	}

	inst := Installed{Path: "/state/plugins/connectors/jira/bin", Sha256: "abc", Resolved: "jira/v1.0.0"}
	got := SpecFromRef(ref, "/cfg", inst, true)
	if got.BinPath != inst.Path || got.Sha256 != "abc" || got.Local {
		t.Fatalf("spec = %+v", got)
	}

	local := refFor(t, config.UseKindConnector, "./bin/conductor-jira")
	ls := SpecFromRef(local, "/cfg", Installed{}, false)
	if !ls.Local || ls.BinPath != filepath.Join("/cfg", "bin/conductor-jira") {
		t.Fatalf("local spec = %+v", ls)
	}
}

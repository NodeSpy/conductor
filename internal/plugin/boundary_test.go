package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// An update is compared with the build it would replace before it is
// installed: the same boundary is installed; a changed one (anything added
// or removed) is held while the installed build keeps running exactly as it
// was, for every source, the official repo included.

const officialWidget = "NodeSpy/conductor-plugins/connectors/widget"

// widgetBin is stubFor the widget publishing a build of its own.
func widgetBin(build string, tags ...string) stubAPI {
	api := stubFor("connectors/widget", tags...)
	api.bin = []byte("#!/bin/sh\necho " + build + "\n")
	return api
}

// sources: the official repo (no plugin_trust entry) and a plugin_trust'd
// third party — the same rule for both.
var sources = []struct {
	name, use string
	trust     *config.PackTrustConfig
}{
	{"official", officialWidget, nil},
	{"plugin_trust third party", "acme/plugins/connectors/widget", &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}},
}

// installWidget installs v1 declaring caps and returns the state, refs and
// its record.
func installWidget(t *testing.T, use string, trust *config.PackTrustConfig, shape func(config.PluginRef) config.PluginRef, caps Capabilities) (*InstallState, map[string]config.PluginRef, config.PluginRef, Installed) {
	t.Helper()
	st := stateAt(t)
	ref := shape(refFor(t, config.UseKindConnector, use))
	refs := map[string]config.PluginRef{ref.Key(): ref}
	res, err := Reconcile(refs, st, trust, widgetBin("v1", "connectors/widget/v1.0.0"), Options{Describe: widgetDecl(caps)})
	if err != nil || len(res) != 1 || res[0].Action != ActionInstalled {
		t.Fatalf("install: %+v %v", res, err)
	}
	v1, _ := st.Get(ref.Use.InstallKey())
	return st, refs, ref, v1
}

// A verified release (official or not) that changes the boundary is held:
// the installed record is untouched (same binary, sha and declaration), its
// binary still verifies, the new build is staged at a path of its own, and
// approving it swaps it in.
func TestBoundaryChangeIsHeldAndTheRunningBuildIsUntouched(t *testing.T) {
	for _, src := range sources {
		for _, shape := range shapes {
			t.Run(src.name+"/"+shape.name, func(t *testing.T) {
				st, refs, ref, v1 := installWidget(t, src.use, src.trust, shape.ref, widgetNarrow)
				v1Bytes, _ := os.ReadFile(v1.Path)

				res, err := Reconcile(refs, st, src.trust, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
				if err != nil || len(res) != 1 || res[0].Action != ActionHeld {
					t.Fatalf("a boundary change must be held: %+v %v", res, err)
				}
				if res[0].Changed() {
					t.Fatal("a held update must not count as a change (nothing reloads)")
				}
				st = LoadInstallState(st.Dir())
				cur, _ := st.Get(ref.Use.InstallKey())
				if cur.Resolved != v1.Resolved || cur.Sha256 != v1.Sha256 || cur.Path != v1.Path || !sameDecl(cur.Manifest, v1.Manifest) {
					t.Fatalf("the running record changed: %+v, was %+v", cur, v1)
				}
				if b, _ := os.ReadFile(v1.Path); !bytes.Equal(b, v1Bytes) {
					t.Fatal("the running binary was overwritten")
				}
				if err := VerifyOnly(SpecFromRef(ref, "", cur, true)); err != nil {
					t.Fatalf("the running build no longer verifies: %v", err)
				}
				p, ok := st.HeldFor(ref.Use.InstallKey(), ref.Source())
				if !ok || p.Resolved != "connectors/widget/v2.0.0" || p.Replaces != "connectors/widget/v1.0.0" || p.Path == v1.Path || !binaryPresent(p.Path) {
					t.Fatalf("held: %+v", p)
				}
				if added := strings.Join(p.Widens, "; "); !strings.Contains(added, "listens on") || !strings.Contains(added, "reads") {
					t.Fatalf("adds: %q", added)
				}

				if _, err := st.Approve(ref.Name); err != nil {
					t.Fatal(err)
				}
				if cur, _ := st.Get(ref.Use.InstallKey()); cur.Resolved != "connectors/widget/v2.0.0" || cur.Path != p.Path {
					t.Fatalf("approved, the held build replaces the running one: %+v", cur)
				}
			})
		}
	}
}

// The same boundary is swapped in without asking; a narrowing is a change
// to the boundary too, and is held.
func TestSameBoundaryIsSwappedAndANarrowingIsHeld(t *testing.T) {
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			st, refs, _, _ := installWidget(t, src.use, src.trust, shapes[0].ref, widgetWide)
			res, _ := Reconcile(refs, st, src.trust, widgetBin("v1.1", "connectors/widget/v1.0.0", "connectors/widget/v1.1.0"), Options{Describe: widgetDecl(widgetWide)})
			if res[0].Action != ActionUpdated || res[0].Tag != "connectors/widget/v1.1.0" || !res[0].Changed() {
				t.Fatalf("same boundary: %+v", res[0])
			}
			res, _ = Reconcile(refs, st, src.trust, widgetBin("v1.2", "connectors/widget/v1.0.0", "connectors/widget/v1.1.0", "connectors/widget/v1.2.0"), Options{Describe: widgetDecl(widgetNarrow)})
			if res[0].Action != ActionHeld {
				t.Fatalf("a narrowing changes the boundary: %+v", res[0])
			}
			p := st.PendingUpdates()[0]
			if len(p.Widens) != 0 || len(p.Removes) != 2 {
				t.Fatalf("held narrowing: adds %v removes %v", p.Widens, p.Removes)
			}
			if !strings.Contains(p.Message(), "removes: listens on") {
				t.Fatalf("message: %s", p.Message())
			}
		})
	}
}

// A newer held release supersedes the older held one, compared with what
// runs, and is announced again; a restart keeps it held and announced.
func TestNewerHeldReleaseSupersedesAndIsAnnouncedAgain(t *testing.T) {
	st, refs, ref, _ := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
	Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	p, _ := st.HeldFor(ref.Use.InstallKey(), ref.Source())
	st.MarkAnnounced(p)
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	// A restart: the held update and its announcement survive.
	st = LoadInstallState(st.Dir())
	if p, ok := st.HeldFor(ref.Use.InstallKey(), ref.Source()); !ok || !p.Announced {
		t.Fatalf("after a restart: %+v", p)
	}
	if cur, _ := st.Get(ref.Use.InstallKey()); cur.Resolved != "connectors/widget/v1.0.0" {
		t.Fatalf("after a restart v1 runs: %s", cur.Resolved)
	}
	// The same release on the next pass: still announced.
	Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	if p, _ := st.HeldFor(ref.Use.InstallKey(), ref.Source()); !p.Announced {
		t.Fatalf("the same held build must not be announced again: %+v", p)
	}

	wider := widgetWide
	wider.Env = []string{"WIDGET_TOKEN"}
	Reconcile(refs, st, nil, widgetBin("v3", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0", "connectors/widget/v3.0.0"), Options{Describe: widgetDecl(wider)})
	ps := st.PendingUpdates()
	if len(ps) != 1 || ps[0].Resolved != "connectors/widget/v3.0.0" || ps[0].Replaces != "connectors/widget/v1.0.0" || ps[0].Announced {
		t.Fatalf("v3 supersedes v2, against v1, to be announced: %+v", ps)
	}
	if added := strings.Join(ps[0].Widens, "; "); !strings.Contains(added, "WIDGET_TOKEN") || !strings.Contains(added, "listens on") {
		t.Fatalf("the diff is against what runs: %q", added)
	}
}

// Declining keeps the running build and stops asking about that
// declaration; one that declares something else is asked about again.
func TestDeclineKeepsTheRunningBuild(t *testing.T) {
	st, refs, ref, _ := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
	Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	if _, err := st.Decline("nope"); err == nil {
		t.Fatal("declining a plugin with nothing waiting must fail")
	}
	if _, err := st.Decline(ref.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.HeldFor(ref.Use.InstallKey(), ref.Source()); ok {
		t.Fatal("declined, nothing waits")
	}
	Reconcile(refs, st, nil, widgetBin("v2.1", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0", "connectors/widget/v2.1.0"), Options{Describe: widgetDecl(widgetWide)})
	if _, ok := st.HeldFor(ref.Use.InstallKey(), ref.Source()); ok {
		t.Fatal("a newer build declaring what was declined is not asked about again")
	}
	if cur, _ := st.Get(ref.Use.InstallKey()); cur.Resolved != "connectors/widget/v1.0.0" {
		t.Fatalf("declined, v1 keeps running: %s", cur.Resolved)
	}
	wider := widgetWide
	wider.Env = []string{"WIDGET_TOKEN"}
	Reconcile(refs, st, nil, widgetBin("v3", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0", "connectors/widget/v2.1.0", "connectors/widget/v3.0.0"), Options{Describe: widgetDecl(wider)})
	if p, ok := st.HeldFor(ref.Use.InstallKey(), ref.Source()); !ok || p.Resolved != "connectors/widget/v3.0.0" {
		t.Fatalf("a different declaration waits again: %+v", p)
	}
}

// `--force` taking a re-published release that changes the boundary holds
// it without touching the installed binary: it is staged beside it.
func TestRepublishedBuildIsStagedBesideTheRunningOne(t *testing.T) {
	st, refs, ref, v1 := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
	v1Bytes, _ := os.ReadFile(v1.Path)
	res, _ := Reconcile(refs, st, nil, widgetBin("v1-republished", "connectors/widget/v1.0.0"), Options{Describe: widgetDecl(widgetWide), Force: true})
	if res[0].Action != ActionHeld {
		t.Fatalf("re-published with a changed boundary: %+v", res[0])
	}
	if b, _ := os.ReadFile(v1.Path); !bytes.Equal(b, v1Bytes) {
		t.Fatal("the running binary was overwritten by the re-published one")
	}
	cur, _ := LoadInstallState(st.Dir()).Get(ref.Use.InstallKey())
	if err := VerifyOnly(SpecFromRef(ref, "", cur, true)); err != nil || cur.Sha256 != v1.Sha256 {
		t.Fatalf("the running build must still verify: %v (%+v)", err, cur)
	}
	p, _ := st.HeldFor(ref.Use.InstallKey(), ref.Source())
	if p.Path == v1.Path || !binaryPresent(p.Path) || filepath.Dir(p.Path) != filepath.Dir(v1.Path)+"+"+p.Sha256[:12] {
		t.Fatalf("held build staged at %s", p.Path)
	}

	// Its directory goes when nothing runs from it, and never before: kept
	// while it is the held build, kept (and the version's own directory
	// removed) once approved it is the running one, removed with its
	// version when that is dropped.
	vk := VersionKey{Key: v1.Key, Resolved: v1.Resolved, Source: v1.Source}
	if _, err := st.GCVersions(map[VersionKey]bool{vk: true}); err != nil || !binaryPresent(p.Path) || !binaryPresent(v1.Path) {
		t.Fatalf("a prune removed the held or the running build: %v", err)
	}
	if _, err := st.Approve(ref.Name); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.Get(v1.Key); cur.Path != p.Path {
		t.Fatalf("approved, it runs from %s", cur.Path)
	}
	if _, err := st.GCVersions(map[VersionKey]bool{vk: true}); err != nil || !binaryPresent(p.Path) {
		t.Fatalf("a prune removed the running build: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(v1.Path)); !os.IsNotExist(err) {
		t.Fatalf("the replaced build's directory is left behind: %v", err)
	}
	if _, err := st.GCVersions(map[VersionKey]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(p.Path)); !os.IsNotExist(err) {
		t.Fatalf("the staged build's directory outlived its version: %v", err)
	}
}

// The #247 case: a build that ran before the plugin jail, whose update
// declares a file the build reads but its record does not, keeps running as
// it did before the jail while that update is held; approving it replaces
// the build with the jailed new one (the mark is the build's, not the
// plugin's). An update that declares the same replaces it too.
func TestPreJailBuildKeepsWorkingWhileItsUpdateIsHeld(t *testing.T) {
	defer SetHostJailForTests(HostJail{OK: true})()
	for _, approve := range []bool{true, false} {
		st, refs, ref, v1 := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
		st.Plugins[0].RanBeforeJail = true
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
		st = LoadInstallState(st.Dir())
		next := widgetWide
		if !approve {
			next = widgetNarrow
		}
		res, _ := Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(next)})
		if approve {
			if res[0].Action != ActionHeld {
				t.Fatalf("held: %+v", res[0])
			}
			cur, _ := LoadInstallState(st.Dir()).Get(v1.Key)
			if _, before := runsAsBefore(SpecFromRef(ref, "", cur, true)); cur.Resolved != v1.Resolved || !before {
				t.Fatalf("while its update is held, the build that ran before the jail runs as it did: %+v", cur)
			}
			if _, err := st.Approve(ref.Name); err != nil {
				t.Fatal(err)
			}
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
		} else if res[0].Action != ActionUpdated {
			t.Fatalf("the same declaration replaces it: %+v", res[0])
		}
		cur, _ := LoadInstallState(st.Dir()).Get(v1.Key)
		if _, before := runsAsBefore(SpecFromRef(ref, "", cur, true)); cur.Resolved != "connectors/widget/v2.0.0" || !cur.PreJail || cur.RanBeforeJail || before {
			t.Fatalf("the build that replaces it was installed under the jail and runs jailed: %+v", cur)
		}
	}
}

// Only a build that itself ran before the jail runs as before: a build of a
// pre-jail plugin installed under the jail keeps its jail, held update or
// not, and so does a describe of the build that did.
func TestOnlyABuildThatRanBeforeTheJailRunsAsBefore(t *testing.T) {
	defer SetHostJailForTests(HostJail{OK: true})()
	st, refs, ref, v1 := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
	st.MarkPreJail(v1.Key)
	Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	cur, _ := LoadInstallState(st.Dir()).Get(v1.Key)
	if _, before := runsAsBefore(SpecFromRef(ref, "", cur, true)); !cur.PreJail || before {
		t.Fatalf("a build installed under the jail keeps its jail while its update is held: %+v", cur)
	}
	if _, before := runsAsBefore(Spec{RanBeforeJail: true, Probe: true}); before {
		t.Fatal("a describe runs from an empty jail")
	}
	if _, before := runsAsBefore(Spec{RanBeforeJail: true, TrustFull: true}); before {
		t.Fatal("trust: full is its own launch")
	}
}

// An approval is of a declaration: one recorded while the held build's
// binary was gone carries to the next build declaring the same.
func TestApprovalCarriesToANewerBuildDeclaringTheSame(t *testing.T) {
	st, refs, ref, _ := installWidget(t, officialWidget, nil, shapes[0].ref, widgetNarrow)
	Reconcile(refs, st, nil, widgetBin("v2", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0"), Options{Describe: widgetDecl(widgetWide)})
	p, _ := st.HeldFor(ref.Use.InstallKey(), ref.Source())
	if err := os.Remove(p.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Approve(ApproveArg(ref.Name, p.Installed)); err != nil {
		t.Fatal(err)
	}
	res, _ := Reconcile(refs, st, nil, widgetBin("v2.1", "connectors/widget/v1.0.0", "connectors/widget/v2.0.0", "connectors/widget/v2.1.0"), Options{Describe: widgetDecl(widgetWide)})
	if res[0].Action != ActionUpdated || res[0].Tag != "connectors/widget/v2.1.0" {
		t.Fatalf("the approved declaration installs: %+v", res[0])
	}
}

// A first install is not held: there is nothing running to compare with.
func TestFirstInstallIsNotHeld(t *testing.T) {
	for _, src := range sources {
		installWidget(t, src.use, src.trust, shapes[0].ref, widgetWide)
	}
}

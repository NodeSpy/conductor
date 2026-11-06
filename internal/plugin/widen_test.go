package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// Manifest.Widens unit cases (install.go): it lists what a NEXT declaration
// asks for that the current one did not, so Reconcile knows to hold an
// update rather than silently running it with more than the user agreed to.

func TestManifestWidensEmptyWhenNextIsSameOrNarrower(t *testing.T) {
	m := Manifest{Egress: []string{"a.example:443", "b.example:443"}, Commands: []string{"git", "gh"}, Env: []string{"TOKEN"}}
	if w := m.Widens(m); len(w) != 0 {
		t.Fatalf("an identical declaration must not widen: %v", w)
	}
	narrower := Manifest{Egress: []string{"a.example:443"}, Commands: []string{"git"}}
	if w := m.Widens(narrower); len(w) != 0 {
		t.Fatalf("a strictly narrower declaration must not widen: %v", w)
	}
}

func TestManifestWidensNewEgressDestination(t *testing.T) {
	m := Manifest{Egress: []string{"a.example:443"}}
	next := Manifest{Egress: []string{"a.example:443", "b.example:443"}}
	w := m.Widens(next)
	if len(w) != 1 || !strings.Contains(w[0], "b.example:443") {
		t.Fatalf("a brand-new egress destination must be reported as widening: %v", w)
	}
}

func TestManifestWidensEgressCoveredByExistingGlobIsNotNew(t *testing.T) {
	m := Manifest{Egress: []string{"*.example.com:443"}}
	next := Manifest{Egress: []string{"*.example.com:443", "api.example.com:443"}}
	if w := m.Widens(next); len(w) != 0 {
		t.Fatalf("a destination already covered by a declared glob is not new: %v", w)
	}
}

func TestManifestWidensWriteImpliesRead(t *testing.T) {
	// A path the current manifest may WRITE already covers reading it: a
	// next manifest that only reads what the current one writes is not new.
	m := Manifest{Write: []string{"/data"}}
	next := Manifest{Read: []string{"/data"}}
	if w := m.Widens(next); len(w) != 0 {
		t.Fatalf("reading an already-writable path must not widen: %v", w)
	}
	// But a next manifest that newly WRITES somewhere it could only read
	// before is new.
	m2 := Manifest{Read: []string{"/data"}}
	next2 := Manifest{Write: []string{"/data"}}
	w2 := m2.Widens(next2)
	if len(w2) != 1 || !strings.Contains(w2[0], "/data") {
		t.Fatalf("newly writing a read-only path must widen: %v", w2)
	}
}

func TestManifestWidensNewCommandEnvAndKeychain(t *testing.T) {
	m := Manifest{}
	next := Manifest{Commands: []string{"curl"}, Env: []string{"SECRET"}, Keychain: true}
	w := m.Widens(next)
	var sawCmd, sawEnv, sawKeychain bool
	for _, e := range w {
		sawCmd = sawCmd || strings.Contains(e, "curl")
		sawEnv = sawEnv || strings.Contains(e, "SECRET")
		sawKeychain = sawKeychain || strings.Contains(e, "Keychain")
	}
	if !sawCmd || !sawEnv || !sawKeychain {
		t.Fatalf("a new command, env var and Keychain grant must all be reported: %v", w)
	}
	// Keychain already granted: asking again is not new.
	m2 := Manifest{Keychain: true}
	if w2 := m2.Widens(Manifest{Keychain: true}); len(w2) != 0 {
		t.Fatalf("an already-granted Keychain must not widen: %v", w2)
	}
}

// --- Reconcile: a widening update is held, a narrowing/unchanged one applies ---

// TestReconcileWideningUpdateIsHeld proves docs/wiki/Plugins.md "Updates
// that ask for more": a new release that declares more than the installed
// build is fetched and verified, but held — recorded in Pending with what it
// Widens, the installed build's record (version, sha, manifest) is left
// alone, and Resolution reports ActionHeld rather than ActionUpdated.
func TestReconcileWideningUpdateIsHeld(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	narrow := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"},
		}}, nil
	}
	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{Describe: narrow}); err != nil {
		t.Fatal(err)
	}
	firstRec, ok := st.Get("connectors/jira")
	if !ok {
		t.Fatal("v1 not recorded")
	}

	wide := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"}, Commands: []string{"git"},
		}}, nil
	}
	newer := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	res, err := Reconcile(refs, st, trust, newer, Options{Describe: wide})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionHeld {
		t.Fatalf("resolution = %+v, want held", res)
	}
	if len(res[0].Widens) == 0 || !strings.Contains(strings.Join(res[0].Widens, ";"), "git") {
		t.Fatalf("Widens must name the new command: %v", res[0].Widens)
	}
	// The resolution still reports the OLD build (it is what keeps running).
	if res[0].Tag != "jira/v1.0.0" || res[0].Sha != firstRec.Sha256 {
		t.Fatalf("a held update must report the installed build, not the new one: %+v", res[0])
	}

	// The install record itself is untouched.
	stillInstalled, ok := st.Get("connectors/jira")
	if !ok || stillInstalled.Resolved != "jira/v1.0.0" || stillInstalled.Sha256 != firstRec.Sha256 {
		t.Fatalf("a held update must leave the installed record alone: %+v", stillInstalled)
	}

	// And it shows up as a pending approval naming what it widens.
	pending := st.PendingUpdates()
	if len(pending) != 1 {
		t.Fatalf("expected exactly one pending update, got %d: %+v", len(pending), pending)
	}
	if pending[0].Replaces != "jira/v1.0.0" || pending[0].Resolved != "jira/v2.0.0" {
		t.Fatalf("pending update does not name the right versions: %+v", pending[0])
	}
	if len(pending[0].Widens) == 0 {
		t.Fatalf("pending update must record what it widens: %+v", pending[0])
	}
	if pending[0].Approved {
		t.Fatalf("a held update must not already be approved: %+v", pending[0])
	}
}

// TestReconcileNarrowingUpdateAppliesWithoutHolding proves the other half:
// an update that declares the same or less than the installed build is not
// held — it is a normal ActionUpdated, and nothing lands in Pending.
func TestReconcileNarrowingUpdateAppliesWithoutHolding(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	wide := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"}, Commands: []string{"git"},
		}}, nil
	}
	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{Describe: wide}); err != nil {
		t.Fatal(err)
	}

	// v2 declares the SAME egress and drops the command — strictly narrower.
	narrower := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"},
		}}, nil
	}
	newer := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	res, err := Reconcile(refs, st, trust, newer, Options{Describe: narrower})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionUpdated {
		t.Fatalf("a narrowing update must apply normally: %+v", res)
	}
	if res[0].Tag != "jira/v2.0.0" {
		t.Fatalf("the new build must be what's running: %+v", res[0])
	}
	got, ok := st.Get("connectors/jira")
	if !ok || got.Resolved != "jira/v2.0.0" {
		t.Fatalf("install state must have moved to v2: %+v", got)
	}
	if len(st.PendingUpdates()) != 0 {
		t.Fatalf("a narrowing update must never be held: %+v", st.PendingUpdates())
	}
}

// TestApproveLetsHeldWideningUpdateThrough proves `conductor plugin approve`:
// after a widening update is held, Approve(name) installs it (the binary was
// already fetched and verified when the hold was recorded) and clears it
// from Pending.
func TestApproveLetsHeldWideningUpdateThrough(t *testing.T) {
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	narrow := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"},
		}}, nil
	}
	if _, err := Reconcile(refs, st, trust, stubFor("jira", "jira/v1.0.0"), Options{Describe: narrow}); err != nil {
		t.Fatal(err)
	}

	wide := func(context.Context, Spec) (*Decl, error) {
		return &Decl{Kind: KindConnector, Type: "jira", Capabilities: Capabilities{
			Egress: []string{"acme.atlassian.net:443"}, Commands: []string{"git"},
		}}, nil
	}
	newer := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	if _, err := Reconcile(refs, st, trust, newer, Options{Describe: wide}); err != nil {
		t.Fatal(err)
	}
	if len(st.PendingUpdates()) != 1 {
		t.Fatalf("expected one held update before approval: %+v", st.PendingUpdates())
	}

	done, err := st.Approve("jira")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if len(done) != 1 || done[0].Resolved != "jira/v2.0.0" {
		t.Fatalf("Approve must return the approved update: %+v", done)
	}

	// It is installed immediately (the binary from the hold is still on
	// disk) and dropped from Pending.
	got, ok := st.Get("connectors/jira")
	if !ok || got.Resolved != "jira/v2.0.0" {
		t.Fatalf("approval must install the held update: %+v", got)
	}
	if len(st.PendingUpdates()) != 0 {
		t.Fatalf("an approved update must be cleared from Pending: %+v", st.PendingUpdates())
	}

	// Approving again (nothing left waiting) is a named error, not a panic
	// or a silent no-op.
	if _, err := st.Approve("jira"); err == nil {
		t.Fatal("approving with nothing pending must error")
	}
}

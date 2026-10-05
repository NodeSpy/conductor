package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// Side-by-side plugin versions (docs/wiki/Plugins.md "Side-by-side
// versions"): the bug this fixes was that config.PluginRefs folded every
// connector instance of one plugin NAME into a single PluginRef keyed by
// "<kind>/<name>" alone — `use: acme-echo@v1.0.0` on one connector and
// `use: acme-echo@v2.0.0` on another silently ran whichever instance's Use
// PluginRefs happened to see FIRST for both. These tests drive the real
// fetch -> install -> Manager pipeline (Reconcile, ExplodeRefs, NewManager)
// against two releases of the SAME compiled fixture plugin (test/plugins/
// acme-echo), built twice with different `-ldflags` so the two versions
// disagree about their declared verb surface (pingVerb) exactly the way two
// real releases of a plugin would — and prove each configured instance is
// served by ITS OWN resolved version's live process and declaration.

// buildAcmeEchoVariant compiles test/plugins/acme-echo with pingVerb set as
// requested, returning the built binary's bytes (not just its path — the
// fake release API below publishes these bytes as the release asset, the
// same shape FetchRemoteVerified downloads and installs).
func buildAcmeEchoVariant(t *testing.T, pingVerb bool) []byte {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-echo")
	args := []string{"build", "-o", bin}
	if pingVerb {
		args = append(args, "-ldflags", "-X main.pingVerb=true")
	}
	args = append(args, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd := exec.Command("go", args...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-echo (pingVerb=%v): %v\n%s", pingVerb, err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// perTagAPI is a ReleaseAPI stub that publishes a DIFFERENT binary per
// release tag — unlike remote_test.go's stubAPI (one binary for every tag),
// which cannot exercise two versions disagreeing with each other. ListTags
// reports every key of bins; Download serves that tag's own bytes (and a
// checksums.txt computed from them), so FetchRemoteVerified's sha check
// passes for either version independently.
type perTagAPI struct {
	tags  []string
	bins  map[string][]byte // tag -> asset bytes
	asset string
}

func (s perTagAPI) ListTags(RemoteSource) ([]string, error) {
	return append([]string(nil), s.tags...), nil
}

func (s perTagAPI) Download(_ RemoteSource, tag, file, destDir string) (string, error) {
	p := filepath.Join(destDir, file)
	bin, ok := s.bins[tag]
	if !ok {
		return "", errors.New("perTagAPI: no release published for " + tag)
	}
	if file == "checksums.txt" {
		sum := sha256.Sum256(bin)
		return p, os.WriteFile(p, []byte(hex.EncodeToString(sum[:])+"  "+s.asset+"\n"), 0o644)
	}
	return p, os.WriteFile(p, bin, 0o755)
}

// sideBySideRef builds the config.PluginRef two connector instances ("a"
// pinned to v1.0.0, "b" pinned to v2.0.0) of the SAME plugin name produce —
// exactly what config.PluginRefs derives from:
//
//	connectors:
//	  a: { use: acme/plugins/acme-echo@1.0.0 }
//	  b: { use: acme/plugins/acme-echo@2.0.0 }
func sideBySideRef(t *testing.T) config.PluginRef {
	t.Helper()
	base := config.Use{
		Kind: config.UseKindConnector, Name: "acme-echo",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Component: "acme-echo",
	}
	uA, uB := base, base
	uA.Version, uA.Raw = "=1.0.0", "acme/plugins/acme-echo@1.0.0"
	uB.Version, uB.Raw = "=2.0.0", "acme/plugins/acme-echo@2.0.0"
	return config.PluginRef{
		Name: "acme-echo", Instance: "a", Use: uA,
		Instances: map[string]config.ConnectorGrant{
			"a": {Use: uA},
			"b": {Use: uB},
		},
	}
}

// TestSideBySideVersionsTwoProcessesOwnDecl is the core regression test for
// the silent-first-wins bug: two connectors pinning different versions of
// the SAME plugin are fetched, installed, and run as two INDEPENDENT
// processes, each describing itself according to its OWN build — "a" (pinned
// v1.0.0) never sees "ping" (only the v2.0.0 build declares it), and "b"
// (pinned v2.0.0) does.
func TestSideBySideVersionsTwoProcessesOwnDecl(t *testing.T) {
	v1 := buildAcmeEchoVariant(t, false)
	v2 := buildAcmeEchoVariant(t, true)
	api := perTagAPI{
		tags:  []string{"acme-echo/1.0.0", "acme-echo/2.0.0"},
		bins:  map[string][]byte{"acme-echo/1.0.0": v1, "acme-echo/2.0.0": v2},
		asset: RemoteSource{Component: "acme-echo"}.AssetName(),
	}

	ref := sideBySideRef(t)
	key := ref.Key()
	state := LoadInstallState(t.TempDir())

	// Reconcile is handed the RAW, name-grouped ref (exactly what
	// cfg.PluginRefs() produces — no pre-exploding): finding 1, it resolves
	// "a" (pinned =1.0.0) and "b" (pinned =2.0.0) each against its OWN
	// constraint internally, fetching each one under its own `use:`, never
	// folding them into one fetch that only one instance's Use would have
	// named.
	refs := map[string]config.PluginRef{key: ref}
	if _, err := Reconcile(refs, state, nil, api, Options{AllowUnlisted: true}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Both versions are now installed, side by side, under the SAME key.
	all := state.AllVersions(key)
	if len(all) != 2 {
		t.Fatalf("expected 2 installed versions under %s, got %d: %+v", key, len(all), all)
	}

	// Re-exploded now that install state actually holds both versions —
	// this is what a real boot's Manager construction sees.
	exploded, _ := ExplodeRefs(refs, "", state)
	if len(exploded) != 2 {
		t.Fatalf("expected ExplodeRefs to split into 2 groups, got %d: %v", len(exploded), keysOf(exploded))
	}

	mgr := NewManager(exploded, "", state, Deps{})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		pingDecl   = map[string]bool{} // group key -> does its Decl declare "ping"
		instanceOf = map[string]string{}
		pids       = map[string]int{}
	)
	for _, gkey := range mgr.Names() {
		spec, ok := mgr.Spec(gkey)
		if !ok {
			t.Fatalf("spec not found for %s", gkey)
		}
		decl, err := mgr.StartAndDescribe(ctx, gkey)
		if err != nil {
			t.Fatalf("StartAndDescribe(%s): %v", gkey, err)
		}
		pingDecl[gkey] = hasVerb(decl.Verbs, "ping")
		for inst := range spec.Instances {
			instanceOf[inst] = gkey
		}
		cl, ok := mgr.Client(gkey)
		if !ok {
			t.Fatalf("no shared client for %s", gkey)
		}
		pids[gkey] = cl.PID()
	}

	groupA, okA := instanceOf["a"]
	groupB, okB := instanceOf["b"]
	if !okA || !okB {
		t.Fatalf("expected both instances bound to a group, got %+v", instanceOf)
	}
	if groupA == groupB {
		t.Fatalf("instances pinned to DIFFERENT versions must land in DIFFERENT groups, both got %s", groupA)
	}
	if pingDecl[groupA] {
		t.Errorf("instance a (pinned v1.0.0) must NOT see the ping verb — it would if the old bug (first-wins) were still here")
	}
	if !pingDecl[groupB] {
		t.Errorf("instance b (pinned v2.0.0) must see the ping verb")
	}
	if pids[groupA] == 0 || pids[groupB] == 0 {
		t.Fatalf("expected two live subprocess pids, got %v", pids)
	}
	if pids[groupA] == pids[groupB] {
		t.Fatalf("instances on different versions must run in DIFFERENT processes, both got pid %d", pids[groupA])
	}
}

// TestSideBySideVersionsSameResolvedVersionShareOneProcess is the other half
// of the design: two instances whose constraints resolve to the SAME
// concrete version must still share one process (exactly as multi-instance
// isolation's shared-by-default process did before versions could differ at
// all) — the split is by RESOLVED version, not by `use:` text.
func TestSideBySideVersionsSameResolvedVersionShareOneProcess(t *testing.T) {
	v1 := buildAcmeEchoVariant(t, false)
	api := perTagAPI{
		tags:  []string{"acme-echo/1.0.0"},
		bins:  map[string][]byte{"acme-echo/1.0.0": v1},
		asset: RemoteSource{Component: "acme-echo"}.AssetName(),
	}

	base := config.Use{
		Kind: config.UseKindConnector, Name: "acme-echo",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Component: "acme-echo",
	}
	uPinned, uUnpinned := base, base
	uPinned.Version, uPinned.Raw = "=1.0.0", "acme/plugins/acme-echo@1.0.0"
	uUnpinned.Raw = "acme/plugins/acme-echo"
	ref := config.PluginRef{
		Name: "acme-echo", Instance: "a", Use: uPinned,
		Instances: map[string]config.ConnectorGrant{
			"a": {Use: uPinned},
			"b": {Use: uUnpinned}, // tracks latest — resolves to the SAME v1.0.0, the only release
		},
	}
	key := ref.Key()
	state := LoadInstallState(t.TempDir())
	refs := map[string]config.PluginRef{key: ref}
	if _, err := Reconcile(refs, state, nil, api, Options{AllowUnlisted: true}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	exploded, _ := ExplodeRefs(refs, "", state)
	if len(exploded) != 1 {
		t.Fatalf("two instances resolving to the SAME version must land in ONE group, got %d: %v", len(exploded), keysOf(exploded))
	}
	mgr := NewManager(exploded, "", state, Deps{})
	defer mgr.Close()

	gkey := mgr.Names()[0]
	ca, err := mgr.InstanceClient(gkey, "a")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := mgr.InstanceClient(gkey, "b")
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatal("both instances must resolve to the SAME *Client — they share one resolved version")
	}
}

// TestManagerReloadTouchesOnlyItsOwnGroup is "reload moves one group only"
// (docs/wiki/Plugins.md "Side-by-side versions"): with two resolved-version
// groups of the same plugin name live, Reload()ing ONE of them (as a
// dependency refresh that moved only an unpinned instance's build would)
// swaps that group's client to the new binary and leaves the OTHER group's
// client — a different resolved version, still referenced — completely
// alone: same *Client pointer, same live subprocess pid.
func TestManagerReloadTouchesOnlyItsOwnGroup(t *testing.T) {
	v1 := buildAcmeEchoVariant(t, false)
	v2 := buildAcmeEchoVariant(t, true)
	api := perTagAPI{
		tags:  []string{"acme-echo/1.0.0", "acme-echo/2.0.0"},
		bins:  map[string][]byte{"acme-echo/1.0.0": v1, "acme-echo/2.0.0": v2},
		asset: RemoteSource{Component: "acme-echo"}.AssetName(),
	}
	ref := sideBySideRef(t)
	key := ref.Key()
	state := LoadInstallState(t.TempDir())
	refs := map[string]config.PluginRef{key: ref}
	if _, err := Reconcile(refs, state, nil, api, Options{AllowUnlisted: true}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	exploded, _ := ExplodeRefs(refs, "", state)
	mgr := NewManager(exploded, "", state, Deps{})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	groupOf := map[string]string{} // instance -> group key
	for _, gkey := range mgr.Names() {
		if _, err := mgr.StartAndDescribe(ctx, gkey); err != nil {
			t.Fatalf("StartAndDescribe(%s): %v", gkey, err)
		}
		spec, _ := mgr.Spec(gkey)
		for inst := range spec.Instances {
			groupOf[inst] = gkey
		}
	}
	groupA, groupB := groupOf["a"], groupOf["b"]
	if groupA == groupB {
		t.Fatalf("expected two distinct groups, got %q for both a and b", groupA)
	}

	clientA, _ := mgr.Client(groupA)
	clientB, _ := mgr.Client(groupB)
	pidBBefore := clientB.PID()

	// Reload group A's spec in place (same binary bytes — Client.Reload just
	// needs a valid new Spec; this test is about WHICH group is touched, not
	// about exercising a real binary swap, which resolve_test.go/reload_test.go
	// already cover).
	specA, _ := mgr.Spec(groupA)
	if err := mgr.Reload(groupA, specA); err != nil {
		t.Fatalf("Reload(%s): %v", groupA, err)
	}

	clientAAfter, _ := mgr.Client(groupA)
	clientBAfter, _ := mgr.Client(groupB)
	if clientAAfter != clientA {
		t.Fatal("Reload keeps the SAME *Client pointer for the reloaded group (swap in place)")
	}
	if clientBAfter != clientB {
		t.Fatal("a sibling group's *Client must be a completely different object, untouched by Reload")
	}
	if clientBAfter.PID() != pidBBefore {
		t.Fatalf("sibling group B's live subprocess must NOT be touched by reloading group A: pid was %d, now %d", pidBBefore, clientBAfter.PID())
	}
}

func hasVerb(verbs []Verb, name string) bool {
	for _, v := range verbs {
		if v.Name == name {
			return true
		}
	}
	return false
}

func keysOf(m map[string]config.PluginRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package plugin

import (
	"context"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// countingAPI wraps a ReleaseAPI and counts ListTags/Download calls — used to
// prove a fetch/describe pass happens at most once per distinct release
// (finding 4), not once per configured instance that resolves to it.
type countingAPI struct {
	mu                 sync.Mutex
	inner              ReleaseAPI
	listCalls, dlCalls int
	dlCallsByAssetFile map[string]int
}

func (c *countingAPI) ListTags(rs RemoteSource) ([]string, error) {
	c.mu.Lock()
	c.listCalls++
	c.mu.Unlock()
	return c.inner.ListTags(rs)
}

func (c *countingAPI) Download(rs RemoteSource, tag, file, destDir string) (string, error) {
	c.mu.Lock()
	c.dlCalls++
	if c.dlCallsByAssetFile == nil {
		c.dlCallsByAssetFile = map[string]int{}
	}
	c.dlCallsByAssetFile[tag+"/"+file]++
	c.mu.Unlock()
	return c.inner.Download(rs, tag, file, destDir)
}

// twoInstanceRef builds one PluginRef with two configured connector
// instances of the same plugin NAME: "x" tracks latest (stay-current), "y"
// pins exactly @1.0.0 — the reviewer's reproduction shape for finding 1.
func twoInstanceRef(t *testing.T) config.PluginRef {
	t.Helper()
	base := config.Use{
		Kind: config.UseKindConnector, Name: "jira",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Component: "jira",
	}
	uX, uY := base, base
	uX.Raw = "acme/plugins/jira"
	uY.Version, uY.Raw = "=1.0.0", "acme/plugins/jira@1.0.0"
	return config.PluginRef{
		Name: "jira", Instance: "x", Use: uX,
		Instances: map[string]config.ConnectorGrant{
			"x": {Use: uX},
			"y": {Use: uY},
		},
	}
}

// TestReconcilePinnedInstanceSurvivesSiblingUpdate is the reviewer's
// reproduction for finding 1 (CRITICAL): reconcile must resolve EACH
// instance's own constraint, never a representative's. Before the fix,
// Reconcile was handed a map ALREADY pre-grouped by a representative
// instance's resolved version; once x (unpinned) and y (pinned @=1.0.0)
// both happened to sit on 1.0.0, they shared ONE group keyed by whichever
// instance narrowRef picked as representative. When v2.0.0 appeared, the
// unpinned representative's resolve moved the WHOLE group (and both
// instances' install record) to 2.0.0, and the GC pass then deleted 1.0.0
// from installed.yaml and disk out from under y, which still pinned it —
// uninstalling a configured, still-referenced instance with no config
// change at all.
//
//   - round 1: x (unpinned) and y (@=1.0.0) both resolve to 1.0.0, installed.
//   - v2.0.0 appears.
//   - round 2, with Prune: x moves to 2.0.0; y stays on 1.0.0; BOTH versions
//     remain installed; the config now derives two process groups.
func TestReconcilePinnedInstanceSurvivesSiblingUpdate(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	ref := twoInstanceRef(t)
	key := ref.Key()
	refs := map[string]config.PluginRef{key: ref}

	// Round 1: only v1.0.0 exists. Both instances land on it.
	api1 := stubFor("jira", "jira/v1.0.0")
	if _, err := Reconcile(refs, st, trust, api1, Options{Prune: true}); err != nil {
		t.Fatalf("round 1 reconcile: %v", err)
	}
	all := st.AllVersions(key)
	if len(all) != 1 || all[0].Resolved != "jira/v1.0.0" {
		t.Fatalf("round 1: expected only jira/v1.0.0 installed, got %+v", all)
	}

	// v2.0.0 appears.
	api2 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	if _, err := Reconcile(refs, st, trust, api2, Options{Prune: true}); err != nil {
		t.Fatalf("round 2 reconcile: %v", err)
	}

	all = st.AllVersions(key)
	have := map[string]bool{}
	for _, a := range all {
		have[a.Resolved] = true
	}
	if !have["jira/v1.0.0"] {
		t.Fatalf("round 2: y is pinned @=1.0.0 and must still be installed, got %+v", all)
	}
	if !have["jira/v2.0.0"] {
		t.Fatalf("round 2: x (unpinned) must have moved to v2.0.0, got %+v", all)
	}
	if len(all) != 2 {
		t.Fatalf("round 2: expected exactly 2 installed versions (x's and y's), got %+v", all)
	}

	// The derived process-group set now shows two groups — x and y run
	// independently.
	exploded := ExplodeRefs(refs, "", st)
	if len(exploded) != 2 {
		t.Fatalf("round 2: expected 2 process groups (x moved, y pinned), got %d: %v", len(exploded), keysOf(exploded))
	}
}

// TestReconcileDedupesFetchAndDescribeAcrossInstances is finding 4: two
// configured instances whose constraint TEXT differs but who resolve to the
// SAME release must be fetched (ListTags/Download) and described exactly
// ONCE this pass, never once per instance.
func TestReconcileDedupesFetchAndDescribeAcrossInstances(t *testing.T) {
	st := stateAt(t)
	ref := twoInstanceRef(t) // x unpinned, y pinned @=1.0.0 — both resolve to the only release
	refs := map[string]config.PluginRef{ref.Key(): ref}

	counting := &countingAPI{inner: stubFor("jira", "jira/v1.0.0")}
	describeCalls := 0
	opts := Options{
		AllowUnlisted: true,
		Describe: func(_ context.Context, _ Spec) (*Decl, error) {
			describeCalls++
			return &Decl{}, nil
		},
	}
	if _, err := Reconcile(refs, st, nil, counting, opts); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if counting.dlCalls != 2 { // the binary + checksums.txt — one of EACH, not two of each
		t.Fatalf("expected exactly one Download of the binary and one of checksums.txt (2 total), got %d: %+v", counting.dlCalls, counting.dlCallsByAssetFile)
	}
	if describeCalls != 1 {
		t.Fatalf("expected exactly 1 describe call across both instances sharing jira/v1.0.0, got %d", describeCalls)
	}
}

// TestReconcileTwoPinnedSiblingsNoRepeatedDownload is the reviewer's
// reproduction for finding 1 (HIGH): two configured instances PINNED to the
// identical resolved version, but written with different literal text
// (@1.0.0 vs @v1.0.0), must fetch that version exactly once, ever — never
// again once both are installed and the binary is present on disk, no
// matter how many more times reconcile runs with nothing upstream changed.
//
// Before the fix, "this pin is satisfied" compared the shared (Key,
// Resolved) install record's single Use field against THIS instance's own
// text. Use holds only one instance's text at a time, so whichever instance
// processed last each pass (alphabetically) left ITS text there; the OTHER
// pinned instance's check then failed on the very next pass, forcing a full
// CheckFetchable+Download even though nothing needed to change. With no
// always-refetching unpinned sibling around to prime the shared per-pass
// fetch bucket for free, this repeated every single pass after the first,
// forever, alternating which of the two instances paid for it — never
// settling. (A single pinned instance alongside an unpinned one does not
// show up in a raw call count here: the unpinned sibling already primes the
// bucket/tag-cache every pass regardless of this bug, so the pinned
// instance's own wasted re-checks are free-riding on a call that would have
// happened anyway. Two pinned instances disagreeing on text is the shape
// that makes the extra cost actually bill.)
func TestReconcileTwoPinnedSiblingsNoRepeatedDownload(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	p1, err := config.ParseUse(config.UseKindConnector, "acme/plugins/jira@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := config.ParseUse(config.UseKindConnector, "acme/plugins/jira@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !p1.IsPinned() || !p2.IsPinned() {
		t.Fatalf("both references must be TRUE pins for this test to exercise the bug: p1.IsPinned=%v p2.IsPinned=%v", p1.IsPinned(), p2.IsPinned())
	}
	if p1.String() == p2.String() {
		t.Fatalf("the two pins must be written with DIFFERENT text (that is what triggers the bug): %q", p1.String())
	}

	ref := config.PluginRef{
		Name: "jira", Instance: "p1", Use: p1,
		Instances: map[string]config.ConnectorGrant{
			"p1": {Use: p1},
			"p2": {Use: p2},
		},
	}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	counting := &countingAPI{inner: stubFor("jira", "jira/v1.0.0")}
	for round := 1; round <= 4; round++ {
		if _, err := Reconcile(refs, st, trust, counting, Options{}); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}

	if counting.dlCalls != 2 { // one asset + one checksums.txt, from round 1's genuine install — never again
		t.Fatalf("four reconcile rounds over two pinned siblings on the same version downloaded %d times total (dlCallsByAssetFile=%+v), want exactly 2 (downloaded once, ever)", counting.dlCalls, counting.dlCallsByAssetFile)
	}
	if counting.listCalls != 1 {
		t.Fatalf("four reconcile rounds listed release tags %d times total, want exactly 1 — both pinned instances must skip CheckFetchable entirely once satisfied", counting.listCalls)
	}

	all := st.AllVersions(ref.Key())
	if len(all) != 1 || all[0].Resolved != "jira/v1.0.0" {
		t.Fatalf("expected exactly one installed version jira/v1.0.0, got %+v", all)
	}
}

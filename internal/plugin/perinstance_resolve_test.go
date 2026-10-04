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

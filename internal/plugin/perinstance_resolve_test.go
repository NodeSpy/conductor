package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
	exploded, _ := ExplodeRefs(refs, "", st)
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
	// TEST GAP (resolve.go ~330): the ListTags memo (pinnedTagCache) must
	// cover BOTH instances in this one pass — x's own CheckFetchable call
	// warms it, and FetchRemoteVerified's internal re-check, plus y's own
	// CheckFetchable call, must all hit the memo rather than each costing a
	// real ListTags round trip.
	if counting.listCalls != 1 {
		t.Fatalf("expected exactly 1 ListTags call across both instances resolving one source in one pass, got %d", counting.listCalls)
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

// TestReconcilePinnedInstanceRefetchesWhenBinaryDeleted is a TEST GAP at
// resolve.go ~442: the pin-satisfied check is binaryPresent(prev.Path), not
// just "is there a record" — a pinned instance whose installed binary was
// deleted out from under it (an operator's `rm`, a wiped /tmp, a botched
// upgrade script) must be detected as unsatisfied and re-fetched, never
// left believing it is "pinned" and permanently running nothing.
func TestReconcilePinnedInstanceRefetchesWhenBinaryDeleted(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	p, err := config.ParseUse(config.UseKindConnector, "acme/plugins/jira@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsPinned() {
		t.Fatalf("reference must be a true pin for this test: %q", p.Version)
	}
	ref := config.PluginRef{
		Name: "jira", Instance: "p", Use: p,
		Instances: map[string]config.ConnectorGrant{"p": {Use: p}},
	}
	refs := map[string]config.PluginRef{ref.Key(): ref}
	counting := &countingAPI{inner: stubFor("jira", "jira/v1.0.0")}

	if _, err := Reconcile(refs, st, trust, counting, Options{}); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	installedAfterFirst := counting.dlCalls
	if installedAfterFirst == 0 {
		t.Fatal("expected the first pass to actually install the binary")
	}

	// Satisfied, binary present: a second pass must change nothing and
	// fetch nothing — the ordinary pinned-and-satisfied path.
	if _, err := Reconcile(refs, st, trust, counting, Options{}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if counting.dlCalls != installedAfterFirst {
		t.Fatalf("a satisfied pin with its binary present must not re-fetch, got %d more download(s)", counting.dlCalls-installedAfterFirst)
	}

	// Delete the installed binary out from under the pin.
	rec, ok := st.GetVersion(ref.Key(), "jira/v1.0.0", ref.Use.Source())
	if !ok {
		t.Fatal("expected an installed record for jira/v1.0.0")
	}
	if err := os.Remove(rec.Path); err != nil {
		t.Fatal(err)
	}

	if _, err := Reconcile(refs, st, trust, counting, Options{}); err != nil {
		t.Fatalf("third pass (binary deleted): %v", err)
	}
	if counting.dlCalls == installedAfterFirst {
		t.Fatal("a pinned instance whose binary was deleted must be re-fetched, not left believing it is still satisfied")
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatalf("the binary must be restored to disk after the re-fetch: %v", err)
	}
}

// TestReconcileRetaggedReleaseIsReVerifiedNotSilentlyCurrent is a TEST GAP
// at resolve.go ~485: the "current" classification compares the fetched
// sha against what was already installed for that TAG, not just the tag
// text — a release that gets RETAGGED upstream (the same tag, "jira/v1.0.0"
// say, now pointing at different bytes — a forge mistake, or a forced
// push) must be detected as a real change and re-verified/re-recorded,
// never silently called "up to date" just because the tag string the
// instance resolved to didn't move.
func TestReconcileRetaggedReleaseIsReVerifiedNotSilentlyCurrent(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	u, err := config.ParseUse(config.UseKindConnector, "acme/plugins/jira")
	if err != nil {
		t.Fatal(err)
	}
	ref := config.PluginRef{
		Name: "jira", Instance: "x", Use: u,
		Instances: map[string]config.ConnectorGrant{"x": {Use: u}},
	}
	refs := map[string]config.PluginRef{ref.Key(): ref}

	apiV1 := stubAPI{tags: []string{"jira/v1.0.0"}, bin: []byte("original release bytes"), assetName: RemoteSource{Component: "jira"}.AssetName()}
	res, err := Reconcile(refs, st, trust, apiV1, Options{})
	if err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if len(res) != 1 || res[0].Action != ActionInstalled {
		t.Fatalf("expected the first pass to install, got %+v", res)
	}
	firstSha := res[0].Sha

	// The SAME tag gets retagged upstream, pointing at DIFFERENT bytes —
	// the tag string never changes, only what it resolves to.
	apiV2 := stubAPI{tags: []string{"jira/v1.0.0"}, bin: []byte("retagged, completely different bytes"), assetName: RemoteSource{Component: "jira"}.AssetName()}
	res2, err := Reconcile(refs, st, trust, apiV2, Options{})
	if err != nil {
		t.Fatalf("retag pass: %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("expected one resolution, got %+v", res2)
	}
	if res2[0].Tag != "jira/v1.0.0" {
		t.Fatalf("the tag itself must not have changed: %q", res2[0].Tag)
	}
	if res2[0].Sha == firstSha {
		t.Fatalf("a retagged release must be re-verified (new sha), got the same sha %q as before", firstSha)
	}
	if res2[0].Action != ActionUpdated {
		t.Fatalf("a retagged release under an unchanged tag must classify as %q, not %q — silently calling it current hides that the bytes actually running changed", ActionUpdated, res2[0].Action)
	}

	rec, ok := st.GetVersion(ref.Key(), "jira/v1.0.0", ref.Use.Source())
	if !ok {
		t.Fatal("expected an installed record for jira/v1.0.0")
	}
	if rec.Sha256 != res2[0].Sha {
		t.Fatalf("install state must record the NEW sha %q, got %q", res2[0].Sha, rec.Sha256)
	}
	got, err := os.ReadFile(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "retagged, completely different bytes" {
		t.Fatalf("the binary on disk must be the retagged bytes, got %q", got)
	}
}

// TestReconcileNeverSeedsManifestFromADifferentSource is the reviewer's
// reproduction for finding 1 (HIGH): plugin "widget" is pre-installed from
// repo-a with a recorded permission manifest (egress X). A second instance
// of the SAME connector name is then added pointing at repo-b, whose
// release happens to tag the identical version text ("widget/v1.0.0"). A
// `conductor plugin update` style reconcile pass (no DescribeFunc — opts.
// Describe is nil) must install repo-b's own record with NO manifest
// inherited from repo-a's — the install-state identity is (Key, Resolved,
// Source), and GetVersion must never fall back to a different source's
// record just because the tag text collides (internal/plugin/install.go
// GetVersion, finding 1).
func TestReconcileNeverSeedsManifestFromADifferentSource(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}

	useA := config.Use{Kind: config.UseKindConnector, Name: "widget", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins-a", Component: "widget", Raw: "acme/plugins-a/widget"}
	useB := config.Use{Kind: config.UseKindConnector, Name: "widget", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins-b", Component: "widget", Raw: "acme/plugins-b/widget"}
	if useA.Source() == useB.Source() {
		t.Fatalf("the two sources must differ for this test: %q", useA.Source())
	}

	refA := config.PluginRef{
		Name: "widget", Instance: "a", Use: useA,
		Instances: map[string]config.ConnectorGrant{"a": {Use: useA}},
	}
	api := stubFor("widget", "widget/v1.0.0")

	// Install A (repo-a), then simulate a prior Describe having recorded a
	// real permission manifest for it — exactly what a successful
	// `conductor init`/`plugin add` with a DescribeFunc would have left
	// behind.
	resA, err := Reconcile(map[string]config.PluginRef{refA.Key(): refA}, st, trust, api, Options{})
	if err != nil {
		t.Fatalf("installing A: %v", err)
	}
	if len(resA) != 1 || resA[0].Action != ActionInstalled {
		t.Fatalf("expected A installed, got %+v", resA)
	}
	recA, ok := st.GetVersion(refA.Key(), "widget/v1.0.0", useA.Source())
	if !ok {
		t.Fatal("expected A's record in install state")
	}
	recA.Manifest = Manifest{Egress: []string{"api.repo-a.example:443"}}
	st.Put(recA)

	// Now B (repo-b) is added alongside A under the SAME connector name —
	// same install key, different source, same tag text. Reconcile WITHOUT
	// a DescribeFunc, exactly `conductor plugin update` with no Describe
	// wired (the reviewer's trigger).
	refAB := config.PluginRef{
		Name: "widget", Instance: "a", Use: useA,
		Instances: map[string]config.ConnectorGrant{"a": {Use: useA}, "b": {Use: useB}},
	}
	resAB, err := Reconcile(map[string]config.PluginRef{refAB.Key(): refAB}, st, trust, api, Options{})
	if err != nil {
		t.Fatalf("adding B: %v", err)
	}

	var sawBInstalled bool
	for _, r := range resAB {
		if r.Source == useB.Source() {
			sawBInstalled = true
			if r.Action != ActionInstalled {
				t.Fatalf("expected B freshly installed, got action %q (%+v)", r.Action, r)
			}
		}
	}
	if !sawBInstalled {
		t.Fatalf("expected a resolution for B's source among %+v", resAB)
	}

	recB, ok := st.GetVersion(refAB.Key(), "widget/v1.0.0", useB.Source())
	if !ok {
		t.Fatal("expected B's own record in install state")
	}
	if len(recB.Manifest.Egress) != 0 {
		t.Fatalf("B's record must NOT inherit A's manifest just because the tag text matches, got egress %+v (A's was %+v)", recB.Manifest.Egress, recA.Manifest.Egress)
	}

	// A's own record must be untouched by B's arrival.
	recA2, ok := st.GetVersion(refAB.Key(), "widget/v1.0.0", useA.Source())
	if !ok || len(recA2.Manifest.Egress) != 1 || recA2.Manifest.Egress[0] != "api.repo-a.example:443" {
		t.Fatalf("A's manifest must survive unchanged, got %+v, ok=%v", recA2.Manifest, ok)
	}
}

// TestReconcileInstancesCurrentRemovesDuplicateFetchAtOldRecordedPath is the
// same finding 2 (LOW-MEDIUM) regression as
// TestReconcileCurrentRemovesDuplicateFetchAtOldRecordedPath (resolve_test.go),
// exercised through reconcileInstances (the per-instance path a configured
// connector with Instances set takes) rather than reconcileOne (runtimes/
// engines, or any ref with no Instances) — the duplicate-fetch cleanup on an
// ActionCurrent classification must hold on both paths.
func TestReconcileInstancesCurrentRemovesDuplicateFetchAtOldRecordedPath(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	u, err := config.ParseUse(config.UseKindConnector, "acme/plugins/widget")
	if err != nil {
		t.Fatal(err)
	}
	ref := config.PluginRef{
		Name: "widget", Instance: "x", Use: u,
		Instances: map[string]config.ConnectorGrant{"x": {Use: u}},
	}
	bin := []byte("#!/bin/sh\necho plugin\n")
	api := stubFor("widget", "widget/v1.0.0")
	api.bin = bin

	sum := sha256.Sum256(bin)
	sha := hex.EncodeToString(sum[:])

	key := ref.Key()
	source := u.Source()
	if source == "" {
		t.Fatal("test setup bug: need a non-empty source")
	}

	oldDir := filepath.Join(BinDirFor(st.Dir(), key), sanitizeVersionDir("widget/v1.0.0"))
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDir, api.assetName)
	if err := os.WriteFile(oldPath, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	st.Put(Installed{Key: key, Kind: ref.Kind(), Name: ref.Name, Use: u.String(), Source: source, Resolved: "widget/v1.0.0", Sha256: sha, Path: oldPath, ReleaseVerified: true})

	newDir := BinDirForVersion(st.Dir(), key, "widget/v1.0.0", source)
	if newDir == oldDir {
		t.Fatal("test setup bug: old and new dirs must differ")
	}

	res, err := Reconcile(map[string]config.PluginRef{key: ref}, st, trust, api, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionCurrent {
		t.Fatalf("expected a single ActionCurrent resolution, got %+v", res)
	}
	if res[0].Path != oldPath {
		t.Fatalf("resolution must keep reporting the RECORDED path %q, got %q", oldPath, res[0].Path)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatalf("the freshly fetched duplicate directory %s must be removed, stat err = %v", newDir, err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("the RECORDED path must survive untouched: %v", err)
	}
}

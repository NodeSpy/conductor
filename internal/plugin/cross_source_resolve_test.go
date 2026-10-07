package plugin

import (
	"fmt"
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// perSourceAPI is a ReleaseAPI stub that serves DIFFERENT tags/bytes per
// RemoteSource.URL — unlike stubAPI (one fixed set of tags/bytes for every
// source), this is what it takes to reproduce finding 2: two DIFFERENT
// repos that both happen to tag "widget/v1.0.0" but serve different bytes.
type perSourceAPI struct {
	bySource map[string]stubAPI // key: RemoteSource.URL
}

func (m perSourceAPI) stub(rs RemoteSource) (stubAPI, error) {
	api, ok := m.bySource[rs.URL]
	if !ok {
		return stubAPI{}, fmt.Errorf("perSourceAPI: no stub configured for source %q", rs.URL)
	}
	return api, nil
}

func (m perSourceAPI) ListTags(rs RemoteSource) ([]string, error) {
	api, err := m.stub(rs)
	if err != nil {
		return nil, err
	}
	return api.ListTags(rs)
}

func (m perSourceAPI) Download(rs RemoteSource, tag, file, destDir string) (string, error) {
	api, err := m.stub(rs)
	if err != nil {
		return "", err
	}
	return api.Download(rs, tag, file, destDir)
}

// twoSourceSameTagRef builds one PluginRef, under ONE install key
// ("connectors/widget"), whose two configured instances point at TWO
// DIFFERENT repos ("source a" and "source b") that both tag "widget/v1.0.0"
// — the reviewer's reproduction shape for finding 2. config.validatePluginRefs
// refuses exactly this shape within a single config (two sources under one
// name), but that guard runs at config LOAD time; it is not a guarantee
// Reconcile (or anything in this package) itself enforces, and a sequential
// config change plus persisted install state can still produce it. Building
// the refs map directly here, bypassing config.Load, is how that gap is
// actually reachable and testable.
func twoSourceSameTagRef(t *testing.T) (ref config.PluginRef, uA, uB config.Use) {
	t.Helper()
	uA = config.Use{
		Kind: config.UseKindConnector, Name: "widget", Raw: "repo-a/plugins/widget@1.0.0",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "repo-a/plugins", Component: "widget", Version: "1.0.0",
	}
	uB = config.Use{
		Kind: config.UseKindConnector, Name: "widget", Raw: "repo-b/plugins/widget@1.0.0",
		Origin: config.OriginGitHub, Host: "github.com", Repo: "repo-b/plugins", Component: "widget", Version: "1.0.0",
	}
	if uA.InstallKey() != uB.InstallKey() {
		t.Fatalf("test setup: both instances must share one install key, got %q and %q", uA.InstallKey(), uB.InstallKey())
	}
	if uA.Source() == uB.Source() {
		t.Fatalf("test setup: the two instances must have DIFFERENT sources, both got %q", uA.Source())
	}
	ref = config.PluginRef{
		Name: "widget", Instance: "a", Use: uA,
		Instances: map[string]config.ConnectorGrant{
			"a": {Use: uA},
			"b": {Use: uB},
		},
	}
	return ref, uA, uB
}

// TestReconcileTwoSourcesSameTagTextGetOwnFetchAndRecord is the reviewer's
// reproduction for finding 2 (HIGH, pre-existing, now reachable):
// reconcileInstances' fetch-dedup bucket was keyed by tag TEXT alone, so two
// instances of one install key resolving to the identical tag text from
// DIFFERENT sources shared one fetch — the second instance's install-state
// record claimed its own source, but carried the FIRST source's sha and
// binary bytes, never its own.
//
// Both repos here tag exactly "widget/v1.0.0" but serve different bytes;
// after Reconcile, each instance's own resolved record must carry ITS OWN
// source's sha, path, and bytes on disk.
func TestReconcileTwoSourcesSameTagTextGetOwnFetchAndRecord(t *testing.T) {
	st := stateAt(t)
	trust := &config.PackTrustConfig{Allow: []string{"github.com/*"}}
	ref, uA, uB := twoSourceSameTagRef(t)
	refs := map[string]config.PluginRef{ref.Key(): ref}

	rsA := RemoteSource{URL: uA.GitURL(), Component: "widget"}
	rsB := RemoteSource{URL: uB.GitURL(), Component: "widget"}
	api := perSourceAPI{bySource: map[string]stubAPI{
		rsA.URL: {tags: []string{"widget/v1.0.0"}, bin: []byte("repo A bytes"), assetName: rsA.AssetName()},
		rsB.URL: {tags: []string{"widget/v1.0.0"}, bin: []byte("repo B bytes"), assetName: rsB.AssetName()},
	}}

	res, err := Reconcile(refs, st, trust, api, Options{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Two distinct sources must never be folded into one process group —
	// each gets its own resolution.
	if len(res) != 2 {
		t.Fatalf("expected 2 resolutions (two distinct sources never share a process group), got %d: %+v", len(res), res)
	}
	for _, r := range res {
		if r.Action != ActionInstalled {
			t.Fatalf("expected both instances to install cleanly, got %+v", r)
		}
	}

	// Each instance's OWN install-state record must be reachable from ITS
	// OWN constraint, and reflect ITS OWN source's bytes — never the other
	// instance's.
	recA, ok := st.GetForConstraint(ref.Key(), uA)
	if !ok {
		t.Fatal("expected an installed record reachable from instance a's own constraint")
	}
	recB, ok := st.GetForConstraint(ref.Key(), uB)
	if !ok {
		t.Fatal("expected an installed record reachable from instance b's own constraint")
	}

	if recA.Source != uA.Source() {
		t.Fatalf("instance a's resolved record has source %q, want %q", recA.Source, uA.Source())
	}
	if recB.Source != uB.Source() {
		t.Fatalf("instance b's resolved record has source %q, want %q", recB.Source, uB.Source())
	}
	if recA.Sha256 == "" || recB.Sha256 == "" {
		t.Fatalf("expected both records to carry a verified sha, got a=%q b=%q", recA.Sha256, recB.Sha256)
	}
	if recA.Sha256 == recB.Sha256 {
		t.Fatalf("the two sources serve DIFFERENT bytes — their recorded sha256 must differ, both got %q", recA.Sha256)
	}
	if recA.Path == recB.Path {
		t.Fatalf("the two sources must install to DISTINCT paths, both got %q", recA.Path)
	}

	gotA, err := os.ReadFile(recA.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "repo A bytes" {
		t.Fatalf("instance a's binary on disk = %q, want %q", gotA, "repo A bytes")
	}
	gotB, err := os.ReadFile(recB.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "repo B bytes" {
		t.Fatalf("instance b's binary on disk = %q, want %q", gotB, "repo B bytes")
	}

	// GC must be able to tell the two apart too (finding 2's VersionKey /
	// keep-set half): keeping only instance a's (key, resolved, source)
	// must drop instance b's record and binary, never both or neither.
	keep := map[VersionKey]bool{{Key: ref.Key(), Resolved: "widget/v1.0.0", Source: uA.Source()}: true}
	dropped, err := st.GCVersions(keep)
	if err != nil {
		t.Fatalf("GCVersions: %v", err)
	}
	if len(dropped) != 1 || dropped[0].Source != uB.Source() {
		t.Fatalf("expected exactly instance b's (source-scoped) record dropped, got %+v", dropped)
	}
	if _, err := os.Stat(recA.Path); err != nil {
		t.Fatalf("instance a's binary must survive GC, got %v", err)
	}
	if _, err := os.Stat(recB.Path); !os.IsNotExist(err) {
		t.Fatalf("instance b's binary must be removed by GC, stat err = %v", err)
	}
}

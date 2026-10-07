package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// Side-by-side versions (docs/wiki/Plugins.md): install state keys Installed
// records by (Key, Resolved) now, not Key alone, so a plugin can have more
// than one version on disk at once. The ON-DISK FORMAT did not change —
// Installed already carried both Key and Resolved — so an existing,
// single-version install state file (at most one record per Key, written by
// a pre-versioning build) loads exactly as before: this is the "migration",
// and it is the in-memory matching rules that changed, not the file shape.

// TestInstallStateLoadsOldSingleVersionFileTransparently proves a state file
// from before side-by-side versions existed loads with no special-casing —
// Get, GetForConstraint and AllVersions all answer exactly as they would
// have pre-versioning for a key with only one record.
func TestInstallStateLoadsOldSingleVersionFileTransparently(t *testing.T) {
	dir := t.TempDir()
	// The literal backtick trick above doesn't work in a raw string; write a
	// clean fixture directly instead.
	doc := `version: 1
plugins:
    - key: connectors/widget
      kind: connector
      name: widget
      use: acme/plugins/widget
      source: github.com/acme/plugins//widget
      resolved: widget/v1.2.3
      sha256: deadbeef
      path: /var/lib/conductor/plugins/connectors/widget/widget/conductor-widget_linux_amd64
    - key: runtimes/paseo
      kind: runtime
      name: paseo
      use: acme/plugins/paseo
      resolved: paseo/v0.9.0
      sha256: cafef00d
      path: /var/lib/conductor/plugins/runtimes/paseo/paseo/conductor-paseo_linux_amd64
`
	if err := os.WriteFile(filepath.Join(dir, "installed.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	s := LoadInstallState(dir)
	if len(s.Keys()) != 2 {
		t.Fatalf("expected 2 distinct keys, got %v", s.Keys())
	}

	got, ok := s.Get("connectors/widget")
	if !ok || got.Resolved != "widget/v1.2.3" || got.Sha256 != "deadbeef" {
		t.Fatalf("Get: unexpected record %+v", got)
	}

	// An UNCONSTRAINED instance (no @version in its use:) must still find
	// the single pre-existing record.
	u := config.Use{Kind: config.UseKindConnector, Name: "widget"}
	got2, ok := s.GetForConstraint("connectors/widget", u)
	if !ok || got2.Resolved != "widget/v1.2.3" {
		t.Fatalf("GetForConstraint (unconstrained): unexpected %+v, ok=%v", got2, ok)
	}

	all := s.AllVersions("connectors/widget")
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 version for a pre-versioning key, got %d: %+v", len(all), all)
	}
}

// TestInstallStateAddsSecondVersionAlongsideOld proves that TAKING an
// old-format, single-version state file and installing a SECOND version of
// the same plugin (the first time two connectors pin different versions)
// produces side-by-side records rather than overwriting the original.
func TestInstallStateAddsSecondVersionAlongsideOld(t *testing.T) {
	dir := t.TempDir()
	doc := `version: 1
plugins:
    - key: connectors/widget
      kind: connector
      name: widget
      resolved: widget/v1.2.3
      sha256: deadbeef
      path: /old/path/conductor-widget
`
	if err := os.WriteFile(filepath.Join(dir, "installed.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s := LoadInstallState(dir)
	s.Put(Installed{Key: "connectors/widget", Kind: "connector", Name: "widget", Resolved: "widget/v2.0.0", Sha256: "f00d", Path: "/new/path/conductor-widget"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded := LoadInstallState(dir)
	all := reloaded.AllVersions("connectors/widget")
	if len(all) != 2 {
		t.Fatalf("expected both the old and new version to coexist, got %d: %+v", len(all), all)
	}
	if v1, ok := reloaded.GetVersion("connectors/widget", "widget/v1.2.3", ""); !ok || v1.Sha256 != "deadbeef" {
		t.Fatalf("the OLD version must survive unchanged: %+v, ok=%v", v1, ok)
	}
	if v2, ok := reloaded.GetVersion("connectors/widget", "widget/v2.0.0", ""); !ok || v2.Sha256 != "f00d" {
		t.Fatalf("the NEW version must be recorded: %+v, ok=%v", v2, ok)
	}
}

// TestGCVersionsKeepsReferencedDropsUnreferenced is the GC half of
// side-by-side versions: a version no live config group resolves to any
// more is uninstalled (its directory removed), while a SIBLING version of
// the same key that IS still referenced is left exactly as it is.
func TestGCVersionsKeepsReferencedDropsUnreferenced(t *testing.T) {
	dir := t.TempDir()
	s := LoadInstallState(dir)
	keep := Installed{Key: "connectors/widget", Kind: "connector", Name: "widget", Resolved: "widget/v2.0.0", Path: filepath.Join(BinDirForVersion(dir, "connectors/widget", "widget/v2.0.0", ""), "conductor-widget")}
	drop := Installed{Key: "connectors/widget", Kind: "connector", Name: "widget", Resolved: "widget/v1.0.0", Path: filepath.Join(BinDirForVersion(dir, "connectors/widget", "widget/v1.0.0", ""), "conductor-widget")}
	unrelated := Installed{Key: "runtimes/paseo", Kind: "runtime", Name: "paseo", Resolved: "paseo/v1.0.0", Path: filepath.Join(BinDirForVersion(dir, "runtimes/paseo", "paseo/v1.0.0", ""), "conductor-paseo")}
	for _, in := range []Installed{keep, drop, unrelated} {
		if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Path, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		s.Put(in)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	keepSet := map[VersionKey]bool{
		{Key: keep.Key, Resolved: keep.Resolved}:           true,
		{Key: unrelated.Key, Resolved: unrelated.Resolved}: true,
	}
	dropped, err := s.GCVersions(keepSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0].Key != drop.Key || dropped[0].Resolved != drop.Resolved {
		t.Fatalf("expected exactly the unreferenced version dropped, got %+v", dropped)
	}

	if _, ok := s.GetVersion(keep.Key, keep.Resolved, ""); !ok {
		t.Fatal("the referenced version must still be in install state")
	}
	if _, ok := s.GetVersion(drop.Key, drop.Resolved, ""); ok {
		t.Fatal("the unreferenced version must be gone from install state")
	}
	if _, ok := s.GetVersion(unrelated.Key, unrelated.Resolved, ""); !ok {
		t.Fatal("an unrelated key must be untouched")
	}

	if _, err := os.Stat(filepath.Dir(drop.Path)); !os.IsNotExist(err) {
		t.Fatalf("the dropped version's install directory must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(keep.Path); err != nil {
		t.Fatalf("the kept version's binary must survive: %v", err)
	}
	if _, err := os.Stat(unrelated.Path); err != nil {
		t.Fatalf("the unrelated key's binary must survive: %v", err)
	}
}

// TestGetVersionNeverFallsBackToADifferentSource is finding 1 (HIGH): two
// DIFFERENT sources under one key can tag the identical version text (two
// repos both releasing "v1.0.0"). Querying GetVersion for one source's
// record when only the OTHER source's record exists under that (key,
// version) must report not found — never silently hand back the other
// source's record (and, upstream in reconcileInstances, its Manifest), or a
// freshly added plugin instance from a different repo inherits a stranger's
// permission manifest. A record with no recorded Source at all (a legacy,
// pre-source-keying entry) is the one and only case that still matches any
// query source, since it cannot possibly belong to a different source.
func TestGetVersionNeverFallsBackToADifferentSource(t *testing.T) {
	s := &InstallState{}
	s.Put(Installed{Key: "connectors/acme", Resolved: "v1.0.0", Source: "github.com/acme/plugins-a//acme", Sha256: "aaa", Manifest: Manifest{Egress: []string{"api.a.example"}}})

	// Querying under a DIFFERENT, non-empty source for the same (key,
	// version) must find nothing — not the other source's record.
	if rec, ok := s.GetVersion("connectors/acme", "v1.0.0", "github.com/acme/plugins-b//acme"); ok {
		t.Fatalf("GetVersion must not fall back to a different source's record, got %+v", rec)
	}

	// The querying source's own record is still found normally.
	if rec, ok := s.GetVersion("connectors/acme", "v1.0.0", "github.com/acme/plugins-a//acme"); !ok || rec.Sha256 != "aaa" {
		t.Fatalf("GetVersion must find the matching source's own record, got %+v, ok=%v", rec, ok)
	}

	// A legacy record with no recorded Source still matches any query
	// source — it predates source-keying and cannot be a different
	// source's record.
	s2 := &InstallState{}
	s2.Put(Installed{Key: "connectors/legacy", Resolved: "v1.0.0", Source: "", Sha256: "legacy-sha"})
	if rec, ok := s2.GetVersion("connectors/legacy", "v1.0.0", "github.com/acme/plugins-a//legacy"); !ok || rec.Sha256 != "legacy-sha" {
		t.Fatalf("GetVersion must match a legacy empty-Source record regardless of query source, got %+v, ok=%v", rec, ok)
	}
}

package config

import (
	"strings"
	"testing"
)

// TestParsePackRef: a pack reference is written under the packs: field its
// shape reads as — a local path or an owner/repo name under use:, an
// explicit git source under source: — and fetched in the resolver's form.
func TestParsePackRef(t *testing.T) {
	for _, tc := range []struct {
		in, key, fetch  string
		local, official bool
	}{
		{"./packs/house", "use", "./packs/house", true, false},
		{"/srv/registry/kit", "use", "/srv/registry/kit", true, false},
		{PacksNamespaceAlias + "/kit", "use", "", false, true},
		{"acme/packs/kit", "use", "", false, false},
		{"git::https://example.test/acme/packs.git//kit", "source", "git::https://example.test/acme/packs.git//kit", false, false},
		{"example.test/acme/packs//kit", "source", "example.test/acme/packs//kit", false, false},
		{"git@example.test:acme/packs.git//kit", "source", "git@example.test:acme/packs.git//kit", false, false},
	} {
		got, err := ParsePackRef(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got.Key != tc.key || got.Value != tc.in || got.Local != tc.local || got.Official != tc.official {
			t.Errorf("%s: %+v", tc.in, got)
		}
		if tc.fetch != "" && got.Fetch != tc.fetch {
			t.Errorf("%s: fetch %q, want %q", tc.in, got.Fetch, tc.fetch)
		}
		if got.Fetch == "" {
			t.Errorf("%s: no fetch source", tc.in)
		}
		if b := PackConfigBlock(got); len(b) != 1 || b[tc.key] != tc.in {
			t.Errorf("%s: block %+v", tc.in, b)
		}
	}
	if _, err := ParsePackRef(""); err == nil {
		t.Error("empty ref should be refused")
	}
}

// TestPackCatalogLatestForInstalled: an installed pack the registry also
// lists is listed once (installed), carrying the registry's release as its
// latest — what the dashboard compares to offer an update.
func TestPackCatalogLatestForInstalled(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/tiny", stagedTinyPack(">=0.1", "one"))
	path := writeDoc(t, dir, `
connectors:
  hook: { use: webhook }
packs:
  tiny:
    source: ./src/tiny
`)
	if _, err := resolveAndLoad(t, path); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	reg := t.TempDir()
	writePackSource(t, reg, "tiny", strings.Replace(stagedTinyPack(">=0.1", "two"), `version: "1.0.0"`, `version: "1.2.0"`, 1))
	writePackSource(t, reg, "other", strings.Replace(stagedTinyPack(">=0.1", "x"), "name: tiny", "name: other", 1))

	r := PackCatalog(path, "", reg, false, false)
	if r.RegistryError != "" || r.ConfigError != "" {
		t.Fatalf("catalog errors: %+v", r)
	}
	if len(r.Packs) != 2 {
		t.Fatalf("packs: %+v", r.Packs)
	}
	for _, p := range r.Packs {
		switch p.Name {
		case "tiny":
			if !p.Installed || p.Version != "1.0.0" || p.Latest != "1.2.0" || p.Manifest == nil {
				t.Errorf("installed tiny: %+v", p)
			}
		case "other":
			if p.Installed || p.Latest != "" || p.Manifest == nil {
				t.Errorf("registry other: %+v", p)
			}
		}
	}
}

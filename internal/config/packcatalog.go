package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// THE PACK CATALOG (docs/wiki/Packs.md#the-catalog): the packs installed in
// a config and the packs a registry offers, each with the CONTENT KINDS it
// ships — derived from what its manifest actually contains, never from a
// label the pack declares about itself. A pack that ships only bot templates
// (and the collections and seed documents those carry) is a TEMPLATE-ONLY
// pack: it belongs to `conductor bot new`/`bot templates`, and the general
// catalog leaves it out unless asked.

// Content kinds.
const (
	KindWorkflows    = "workflows"
	KindTriggers     = "triggers"
	KindChecks       = "checks"
	KindModels       = "models"
	KindPolicy       = "policy"
	KindBotTemplates = "bot_templates"
	KindCollections  = "collections"
	KindSeed         = "seed"
)

// templateKinds are what a bot template carries with it.
var templateKinds = map[string]bool{KindBotTemplates: true, KindCollections: true, KindSeed: true}

// PackContents is what a pack ships, by kind (and how many of each).
type PackContents struct {
	Kinds  []string       `json:"kinds"`
	Counts map[string]int `json:"counts"`
}

// ManifestContents derives a pack's content kinds from its manifest.
func ManifestContents(man *PackManifest) PackContents {
	counts := map[string]int{}
	add := func(kind string, n int) {
		if n > 0 {
			counts[kind] += n
		}
	}
	add(KindWorkflows, len(man.Workflows))
	add(KindTriggers, len(man.Triggers))
	add(KindChecks, len(man.Checks))
	add(KindModels, len(man.Models))
	if man.Policy != nil {
		add(KindPolicy, 1)
	}
	add(KindBotTemplates, len(man.Bots))
	for _, t := range man.Bots {
		add(KindCollections, len(t.Collections))
		if t.Seed != "" {
			add(KindSeed, 1)
		}
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return PackContents{Kinds: kinds, Counts: counts}
}

// TemplateOnly reports a pack that ships bot templates and nothing else.
func (p PackContents) TemplateOnly() bool {
	if p.Counts[KindBotTemplates] == 0 {
		return false
	}
	for _, k := range p.Kinds {
		if !templateKinds[k] {
			return false
		}
	}
	return true
}

// PackSummary is one catalog entry.
type PackSummary struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Description  string       `json:"description"`
	Contents     PackContents `json:"contents"`
	TemplateOnly bool         `json:"template_only"`
	Installed    bool         `json:"installed"`
	Instance     string       `json:"pack_instance,omitempty"`
	// Source is where the pack comes from; Install the packs: entry that
	// installs it (a registry pack).
	Source  string `json:"source,omitempty"`
	Install string `json:"install,omitempty"`
}

// InstalledPacks are the config's top-level pack instances, from their
// vendored manifests.
func (c *Config) InstalledPacks() []PackSummary {
	vendor := packVendorDir(c.baseDir)
	var out []PackSummary
	for _, inst := range sortedPackKeys(c.Packs) {
		man, err := loadPackManifest(filepath.Join(vendor, inst))
		if err != nil {
			continue // not vendored yet (`conductor init`)
		}
		pc := ManifestContents(man)
		out = append(out, PackSummary{Name: man.Pack.Name, Version: man.Pack.Version, Description: man.Pack.Description,
			Contents: pc, TemplateOnly: pc.TemplateOnly(), Installed: true, Instance: inst, Source: packSourceOf(c.Packs[inst])})
	}
	return out
}

func packSourceOf(p PackInstance) string {
	if p.Use != "" {
		return p.Use
	}
	return p.Source
}

// RegistrySource is the packs: entry value that installs a registry's pack:
// the official repo by name; a git registry's pack by its git source (so the
// trust allowlist governs it like any remote pack — never conductor's local
// clone of it); a local registry's by its path.
func RegistrySource(registry, dir, name string, official bool) (key, value string) {
	switch {
	case official:
		return "use", PacksNamespaceAlias + "/" + name
	case strings.HasPrefix(registry, "https://"):
		// host/path//pack: the form an operator writes (and pack_trust.allow
		// matches), fetched over https by the resolver.
		return "source", strings.TrimSuffix(strings.TrimPrefix(registry, "https://"), ".git") + "//" + name
	case strings.Contains(registry, "://") && !strings.HasPrefix(registry, "file://") || strings.Contains(registry, "@"):
		return "source", "git::" + registry + "//" + name
	case strings.HasPrefix(registry, "file://") && isGitRepo(dir):
		return "source", "git::" + registry + "//" + name
	}
	return "use", filepath.Join(dir, name)
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// RegistryPacks are every pack a registry directory offers (one per
// top-level directory), with what installs each.
func RegistryPacks(registry, dir string, official bool) ([]PackSummary, []string) {
	var out []PackSummary
	var problems []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		pdir := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(pdir, PackManifestFile)); err != nil {
			continue
		}
		man, err := loadPackManifest(pdir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		pc := ManifestContents(man)
		k, v := RegistrySource(registry, dir, e.Name(), official)
		out = append(out, PackSummary{Name: man.Pack.Name, Version: man.Pack.Version, Description: man.Pack.Description,
			Contents: pc, TemplateOnly: pc.TemplateOnly(), Source: v,
			Install: fmt.Sprintf("packs: { %s: { %s: %s } }", e.Name(), k, v)})
	}
	return out, problems
}

// FilterPacks keeps the packs a search matches (every word, in the name or
// description or a kind), and template-only packs only when asked.
func FilterPacks(ps []PackSummary, search string, includeTemplates bool) (kept []PackSummary, hiddenTemplates int) {
	words := strings.Fields(strings.ToLower(search))
	for _, p := range ps {
		hay := strings.ToLower(p.Name + " " + p.Description + " " + strings.Join(p.Contents.Kinds, " "))
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(hay, w)
		}
		if !ok {
			continue
		}
		if p.TemplateOnly && !includeTemplates {
			hiddenTemplates++
			continue
		}
		kept = append(kept, p)
	}
	return kept, hiddenTemplates
}

// TemplateUpdate is whether a newer version of a bot's template pack is
// available, from the official registry as conductor last fetched it —
// never a fetch of its own (`conductor bot templates` or `pack search`
// refresh it). Known false when conductor can't tell: the pack isn't from
// the official registry, or the registry hasn't been fetched.
type TemplateUpdate struct {
	Known     bool   `json:"known"`
	Available bool   `json:"available,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Checked   string `json:"checked,omitempty"`
	Why       string `json:"why,omitempty"`
	How       string `json:"how,omitempty"`
}

// CheckTemplateUpdate compares a bot's template pack with the official
// registry's cached copy.
func CheckTemplateUpdate(o *BotOrigin, instance string) TemplateUpdate {
	if o == nil {
		return TemplateUpdate{}
	}
	s, remote := RemoteSourceRef(o.Source)
	official := strings.HasPrefix(o.Source, PacksNamespaceAlias+"/") || (remote && (s == OfficialPacksSource || strings.HasPrefix(s, OfficialPacksSource+"/")))
	if !official {
		return TemplateUpdate{Why: "its pack is not from the official registry: check its source"}
	}
	h := sha256.Sum256([]byte(DefaultRegistry))
	dir := filepath.Join(StateDir(), "registry", hex.EncodeToString(h[:6]))
	st, err := os.Stat(filepath.Join(dir, ".git", "FETCH_HEAD"))
	if err != nil {
		return TemplateUpdate{Why: "the registry has not been fetched yet (`conductor bot templates` fetches it)"}
	}
	man, err := loadPackManifest(filepath.Join(dir, registryDirName(dir, o.Pack)))
	if err != nil {
		return TemplateUpdate{Why: "the registry has no pack " + o.Pack}
	}
	u := TemplateUpdate{Known: true, Latest: man.Pack.Version, Checked: st.ModTime().UTC().Format(time.RFC3339)}
	if CompareVersions(man.Pack.Version, o.Version) > 0 {
		u.Available = true
		u.How = "conductor pack update " + instance + " (the template's changes then reach the bot as a proposal you review)"
	}
	return u
}

// registryDirName is the registry directory holding a pack (named for it,
// or the one whose manifest names it).
func registryDirName(dir, pack string) string {
	if _, err := os.Stat(filepath.Join(dir, pack, PackManifestFile)); err == nil {
		return pack
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if man, err := loadPackManifest(filepath.Join(dir, e.Name())); err == nil && man.Pack.Name == pack {
			return e.Name()
		}
	}
	return pack
}

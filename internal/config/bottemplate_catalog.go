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

// The template catalog: what `conductor bot templates` and the dashboard's
// "start from a community bot" list — the templates of installed packs, and
// those in a pack registry (the official one by default, or any directory
// or git repo laid out like it: one pack per top-level directory).

// TemplateSummary is one template, as a listing shows it.
type TemplateSummary struct {
	// Ref is what a bot's use: names (installed), or the registry reference
	// of a template not installed yet.
	Ref         string         `json:"ref"`
	Template    string         `json:"template"`
	Pack        string         `json:"pack"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags,omitempty"`
	Installed   bool           `json:"installed"`
	Instance    string         `json:"pack_instance,omitempty"`
	Source      string         `json:"source,omitempty"`
	Install     string         `json:"install,omitempty"` // the packs: block that installs it
	// InstallAs, InstallKey and InstallValue are that block's parts: the
	// instance name, and use: or source: with its value.
	InstallAs    string `json:"install_as,omitempty"`
	InstallKey   string `json:"-"`
	InstallValue string `json:"-"`
	Inputs      []InputView    `json:"inputs,omitempty"`
	Verbs       []VerbView     `json:"verbs,omitempty"`
	ReadOnly    int            `json:"read_only"`
	Above       int            `json:"above_observe"`
	Triggers    []TriggerView  `json:"triggers,omitempty"`
	Proactive   *ProactiveView `json:"proactive,omitempty"`
	Model       string         `json:"model,omitempty"`
	Runtime     string         `json:"runtime,omitempty"`
	Seed        []string       `json:"seed,omitempty"`
	Collections []string       `json:"collections,omitempty"`
	UsedBy      []string       `json:"used_by,omitempty"`
	Digest      string         `json:"digest"`
	Charter     string         `json:"charter,omitempty"`
	// Identity is the template's suggested identity.
	Identity *Identity `json:"identity,omitempty"`
}

// InputView is a template input.
type InputView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Version  string `json:"version,omitempty"`
	Required bool   `json:"required"`
	Desc     string `json:"desc,omitempty"`
}

// VerbView is a requested verb.
type VerbView struct {
	Verb   string `json:"verb"`
	Max    string `json:"max"`
	Amount string `json:"amount,omitempty"`
	Why    string `json:"why,omitempty"`
}

// TriggerView is a suggested trigger.
type TriggerView struct {
	Name string `json:"name"`
	On   string `json:"on"`
	Desc string `json:"desc,omitempty"`
}

// ProactiveView is a suggested proactive sweep.
type ProactiveView struct {
	Every     string `json:"every,omitempty"`
	Checklist string `json:"checklist,omitempty"`
	NotifyAt  string `json:"notify_at,omitempty"`
}

// Summarize renders a loaded template for a listing.
func Summarize(lt LoadedTemplate, full bool) TemplateSummary {
	t := lt.T
	s := TemplateSummary{Ref: lt.Ref(), Template: lt.Name, Pack: lt.Pack.Name, Version: lt.Pack.Version, Description: t.Description,
		Tags: lt.Pack.Tags, Instance: lt.Instance, Source: lt.Source, Digest: lt.Digest, Runtime: t.Runtime}
	for _, in := range sortedInputKeys(t.Connectors) {
		r := t.Connectors[in]
		s.Inputs = append(s.Inputs, InputView{Name: in, Type: r.Type(in), Version: r.Version, Required: r.IsRequired(), Desc: r.Desc})
	}
	for _, v := range sortedRequestKeys(t.Verbs) {
		r := t.Verbs[v]
		s.Verbs = append(s.Verbs, VerbView{Verb: v, Max: r.Max, Amount: r.Amount, Why: r.Why})
		if r.Max == TierObserve {
			s.ReadOnly++
		} else {
			s.Above++
		}
	}
	for _, tr := range t.Triggers {
		s.Triggers = append(s.Triggers, TriggerView{Name: tr.Name, On: tr.On, Desc: tr.Desc})
	}
	if p := t.Proactive; p != nil {
		pv := &ProactiveView{NotifyAt: p.NotifyAt}
		if p.Every != 0 {
			pv.Every = p.Every.D().String()
		}
		switch {
		case p.Checklist.Section != "":
			pv.Checklist = "charter section " + p.Checklist.Section
		case p.Checklist.File != "":
			pv.Checklist = "file " + filepath.Base(p.Checklist.File)
		}
		s.Proactive = pv
	}
	if t.Model.Set() {
		s.Model = strings.Join(t.Model.Acceptable(), ", ")
	}
	for c := range t.Collections {
		s.Collections = append(s.Collections, c)
	}
	sort.Strings(s.Collections)
	if lt.SeedDir != "" {
		_ = filepath.WalkDir(lt.SeedDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(lt.SeedDir, p)
			if strings.Contains(rel, string(filepath.Separator)) {
				s.Seed = append(s.Seed, filepath.ToSlash(rel))
			}
			return nil
		})
		sort.Strings(s.Seed)
	}
	if full {
		s.Charter = lt.Charter
	}
	if id := t.Identity; id != (Identity{}) {
		s.Identity = &id
	}
	return s
}

// InstalledTemplates summarizes every installed template, with the bots
// instantiated from each.
func (c *Config) InstalledTemplates(full bool) []TemplateSummary {
	var out []TemplateSummary
	for _, lt := range c.Templates() {
		s := Summarize(lt, full)
		s.Installed = true
		for _, b := range c.BotNames() {
			if c.Bots[b].Use == lt.Ref() {
				s.UsedBy = append(s.UsedBy, b)
			}
		}
		out = append(out, s)
	}
	return out
}

// DefaultRegistry is the official pack registry, as a git source.
const DefaultRegistry = "https://github.com/" + OfficialPacksRepo

// RegistryTemplates scans a registry checkout: every <pack>/conductor-pack.yaml
// that ships templates. A pack that does not load is reported, not fatal.
// official says the registry is the official one (refs read
// conductor-packs/<pack>/<template>).
func RegistryTemplates(registry, dir string, official bool) ([]TemplateSummary, []string) {
	var out []TemplateSummary
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
		if len(man.Bots) == 0 {
			continue
		}
		lts, err := loadTemplates(man, pdir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		// The ref names the template in the registry; the install entry
		// names the pack by its real source (RegistrySource): a git
		// registry's pack by its git source, so trust governs it.
		ref := PacksNamespaceAlias + "/" + e.Name()
		if !official {
			ref = pdir
		}
		k, v := RegistrySource(registry, dir, e.Name(), official)
		for _, n := range sortedLoadedKeys(lts) {
			lt := lts[n]
			lt.Instance = e.Name()
			s := Summarize(lt, false)
			s.Ref = ref + "/" + n
			s.Instance = ""
			s.Source = v
			s.Install = fmt.Sprintf("packs: { %s: { %s: %s } }", e.Name(), k, v)
			s.InstallAs, s.InstallKey, s.InstallValue = e.Name(), k, v
			out = append(out, s)
		}
	}
	return out, problems
}

// SearchTemplates keeps the templates matching every word of q (in the ref,
// pack, description, tags, inputs or requested verbs).
func SearchTemplates(ts []TemplateSummary, q string) []TemplateSummary {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return ts
	}
	var out []TemplateSummary
	for _, t := range ts {
		hay := strings.ToLower(strings.Join([]string{t.Ref, t.Pack, t.Description, strings.Join(t.Tags, " ")}, " "))
		for _, in := range t.Inputs {
			hay += " " + in.Name + " " + in.Type
		}
		for _, v := range t.Verbs {
			hay += " " + v.Verb
		}
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(hay, w)
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// RegistryDir is a registry's local directory: a directory as given, or a
// shallow clone of a git source cached under the state dir (refreshed when
// older than an hour).
func RegistryDir(src string) (string, bool, error) {
	if src == "" {
		src = DefaultRegistry
	}
	official := src == DefaultRegistry
	if p := expandHome(strings.TrimPrefix(src, "file://")); !strings.Contains(src, "://") && !strings.Contains(src, "@") || strings.HasPrefix(src, "file://") {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p, false, nil
		}
		return "", false, fmt.Errorf("registry %q: not a directory (or a git source: https://…, ssh://…, git@host:…)", src)
	}
	if !safeGitTransport(src) {
		return "", false, fmt.Errorf("registry %q: use https://, ssh:// or file:// (plaintext transports are refused)", src)
	}
	h := sha256.Sum256([]byte(src))
	dir := filepath.Join(StateDir(), "registry", hex.EncodeToString(h[:6]))
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
		if st, err := os.Stat(filepath.Join(dir, ".git", "FETCH_HEAD")); err == nil && time.Since(st.ModTime()) < time.Hour {
			return dir, official, nil
		}
		if _, err := runGit(dir, "fetch", "-q", "--depth", "1", "origin"); err == nil {
			if _, err := runGit(dir, "reset", "-q", "--hard", "FETCH_HEAD"); err == nil {
				return dir, official, nil
			}
		}
		_ = os.RemoveAll(dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", false, err
	}
	if _, err := runGit(filepath.Dir(dir), "clone", "-q", "--depth", "1", "--single-branch", src, dir); err != nil {
		return "", false, fmt.Errorf("registry %s: %w", src, err)
	}
	_, _ = runGit(dir, "fetch", "-q", "--depth", "1", "origin") // stamps FETCH_HEAD for the cache
	return dir, official, nil
}

package repocache

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed recipes.yaml
var bundledYAML []byte

// Bundled are the recipes conductor ships (recipes.yaml).
func Bundled() []Recipe {
	var rs []Recipe
	if err := yaml.Unmarshal(bundledYAML, &rs); err != nil {
		panic("repocache: bundled recipes: " + err.Error())
	}
	return rs
}

// Recipes merges the operator's recipes over the bundled ones: same name
// replaces, a new name adds. A replacing recipe with `disabled: true`
// removes the bundled one.
func Recipes(operator []Recipe) []Recipe {
	byName := map[string]int{}
	out := Bundled()
	for i, r := range out {
		byName[r.Name] = i
	}
	for _, r := range operator {
		if i, ok := byName[r.Name]; ok {
			out[i] = r
			continue
		}
		byName[r.Name] = len(out)
		out = append(out, r)
	}
	kept := out[:0]
	for _, r := range out {
		if !r.Disabled {
			kept = append(kept, r)
		}
	}
	return kept
}

// RepoFileName is the file at a repository's root that defines its caches,
// read from the default branch only.
const RepoFileName = ".conductor.yaml"

// RepoFile is a repository's own conductor file. Version 1 knows caches:
// and autodetect:; every other key is reported and ignored, so a file can
// carry settings a newer conductor reads.
type RepoFile struct {
	Version    int     `yaml:"version"`
	Autodetect *bool   `yaml:"autodetect,omitempty"`
	Caches     []Cache `yaml:"caches,omitempty"`
}

// maxRepoFile bounds the repo file's size.
const maxRepoFile = 256 << 10

// ParseRepoFile parses a repo file: its definitions and warnings for what
// it carries that this conductor does not read. An unknown version is an
// error (the file is then ignored).
func ParseRepoFile(b []byte) (RepoFile, []string, error) {
	var rf RepoFile
	if len(b) > maxRepoFile {
		return rf, nil, fmt.Errorf("%s is larger than %d bytes", RepoFileName, maxRepoFile)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return rf, nil, fmt.Errorf("%s: %w", RepoFileName, err)
	}
	if len(root.Content) == 0 {
		return rf, nil, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return rf, nil, fmt.Errorf("%s: not a mapping", RepoFileName)
	}
	var warn []string
	known := map[string]bool{"version": true, "autodetect": true, "caches": true}
	cacheKeys := map[string]bool{"name": true, "recipe": true, "disabled": true, "key": true, "restore_keys": true, "paths": true, "env": true, "warm": true}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		k := doc.Content[i].Value
		if !known[k] {
			warn = append(warn, fmt.Sprintf("%s: %q is not read by this conductor (ignored)", RepoFileName, k))
			continue
		}
		if k == "caches" && doc.Content[i+1].Kind == yaml.SequenceNode {
			for j, c := range doc.Content[i+1].Content {
				if c.Kind != yaml.MappingNode {
					continue
				}
				for x := 0; x+1 < len(c.Content); x += 2 {
					if ck := c.Content[x].Value; !cacheKeys[ck] {
						warn = append(warn, fmt.Sprintf("%s: caches[%d]: %q is not read by this conductor (ignored)", RepoFileName, j, ck))
					}
				}
			}
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&rf); err != nil {
		return RepoFile{}, warn, fmt.Errorf("%s: %w", RepoFileName, err)
	}
	if rf.Version != 1 {
		return RepoFile{}, warn, fmt.Errorf("%s: version %d is not supported (this conductor reads version 1)", RepoFileName, rf.Version)
	}
	return rf, warn, nil
}

// Sources are the inputs a repository's cache definitions resolve from,
// highest precedence first.
type Sources struct {
	// Repo and Defaults are the operator's: sandbox.caches.repos[<repo>]
	// and sandbox.caches.defaults.
	Repo, Defaults []Cache
	// File is the repo file from the default branch (nil: none, or not
	// read).
	File *RepoFile
	// Recipes are the recipes to resolve references and to detect with.
	Recipes []Recipe
	// Detect reports whether the checkout has any of the given files
	// (nil: nothing is detected).
	Detect func(globs []string) bool
	// Autodetect is the operator's switch (the repo file may turn it off
	// for its repository too).
	Autodetect bool
	// Protected are further home paths no cache may touch.
	Protected []string
}

// Resolve merges the sources into the repository's cache definitions, by
// name: the operator's per-repo config over its defaults over the repo
// file over detected recipes. A definition that does not validate is
// dropped with a warning; so is a disabled one.
func Resolve(s Sources) ([]Def, []string) {
	recipes := map[string]Cache{}
	for _, r := range s.Recipes {
		recipes[r.Name] = r.Cache
	}
	var warn []string
	byName := map[string]Def{}
	set := func(c Cache, origin string) {
		if c.Recipe != "" {
			base, ok := recipes[c.Recipe]
			if !ok {
				warn = append(warn, fmt.Sprintf("cache %q (%s): no recipe %q", c.Name, origin, c.Recipe))
				return
			}
			if c.Name == "" {
				c.Name = c.Recipe
			}
			c = base.merge(c)
		}
		if _, done := byName[c.Name]; done {
			return // a higher-precedence source already defined it
		}
		byName[c.Name] = Def{Cache: c, Origin: origin}
	}
	for _, c := range s.Repo {
		set(c, OriginConfig)
	}
	for _, c := range s.Defaults {
		set(c, OriginConfig)
	}
	auto := s.Autodetect
	if s.File != nil {
		for _, c := range s.File.Caches {
			set(c, OriginRepoFile)
		}
		if s.File.Autodetect != nil && !*s.File.Autodetect {
			auto = false
		}
	}
	if auto && s.Detect != nil {
		for _, r := range s.Recipes {
			if s.Detect(r.Detect) {
				set(r.Cache, OriginAutodetect)
			}
		}
	}
	var out []Def
	for _, d := range byName {
		if d.Disabled {
			continue
		}
		if err := d.Validate(s.Protected); err != nil {
			warn = append(warn, fmt.Sprintf("%v (%s; ignored)", err, d.Origin))
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(warn)
	return out, warn
}

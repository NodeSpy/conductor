package repocache

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"

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
	// fileEnv is the environment a repository's own file set itself (not
	// the recipe's it names): held to an allow-list.
	fileEnv := map[string]map[string]string{}
	set := func(c Cache, origin string) {
		own := c.Env
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
		if origin == OriginRepoFile {
			fileEnv[c.Name] = own
		}
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
		if d.Origin == OriginRepoFile {
			if err := checkRepoFileEnv(fileEnv[d.Name], d.Cache, s.Protected); err != nil {
				warn = append(warn, fmt.Sprintf("%v (%s; ignored)", err, d.Origin))
				continue
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Strings(warn)
	return out, warn
}

// repoFileEnvPaths are the variables a repository's own file may set: each
// names where a tool keeps its download or build cache, and its value must
// be inside one of the cache's own paths. A repository file grants nothing,
// so it never sets a checksum database, registry, proxy, insecure
// transport, toolchain, toolexec or shell variable.
var repoFileEnvPaths = map[string]bool{
	"GOMODCACHE": true, "GOCACHE": true,
	"npm_config_cache": true, "NPM_CONFIG_CACHE": true, "npm_config_store_dir": true, "NPM_CONFIG_STORE_DIR": true,
	"YARN_CACHE_FOLDER": true, "PIP_CACHE_DIR": true, "UV_CACHE_DIR": true, "BUNDLE_PATH": true,
}

// repoFileEnvExact are variables a repository file may set to one value.
var repoFileEnvExact = map[string]string{"GOFLAGS": "-modcacherw"}

// checkRepoFileEnv holds the environment a repository file set to the
// allow-list.
func checkRepoFileEnv(env map[string]string, c Cache, protected []string) error {
	paths := c.ParsedPaths(protected)
	for _, k := range sortedKeys(env) {
		v := env[k]
		if want, ok := repoFileEnvExact[k]; ok {
			if v != want {
				return fmt.Errorf("cache %s: env %s: a repository file may only set it to %q", c.Name, k, want)
			}
			continue
		}
		if !repoFileEnvPaths[k] {
			return fmt.Errorf("cache %s: env %s: a repository file may only set cache-location variables", c.Name, k)
		}
		var raw string
		if rel, ok := strings.CutPrefix(v, "{{home}}/"); ok {
			raw = "~/" + rel
		} else if rel, ok := strings.CutPrefix(v, "{{workspace}}/"); ok {
			raw = rel
		} else {
			return fmt.Errorf("cache %s: env %s: must be {{home}}/<path> or {{workspace}}/<path>", c.Name, k)
		}
		p, err := ParsePath(raw, protected)
		if err != nil {
			return fmt.Errorf("cache %s: env %s: %w", c.Name, k, err)
		}
		inside := false
		for _, q := range paths {
			if q.Area == p.Area && (p.Rel == q.Rel || strings.HasPrefix(p.Rel, q.Rel+"/")) {
				inside = true
			}
		}
		if !inside {
			return fmt.Errorf("cache %s: env %s: %s is not inside one of the cache's paths", c.Name, k, p)
		}
	}
	return nil
}

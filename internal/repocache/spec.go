// Package repocache keeps per-repository dependency caches for sandboxed
// agents, in the manner of a CI cache: a cache is named, keyed by a template
// over the checkout's files, and restored into an agent's home or workspace
// without copying (an overlay with the shared copy as its read-only lower
// layer on Linux, a copy-on-write clone on macOS). Only conductor writes a
// cache: a warm step in a checkout of the repository's default branch, run in
// the same sandbox as an agent, publishes the declared paths atomically. An
// agent's run — on any branch — only ever reads one.
//
// Core knows no ecosystem. A recipe is data: files that detect it, a key
// template, paths, environment and a warm command. The bundled recipes
// (recipes.yaml) are defaults an operator's config replaces or extends.
package repocache

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Cache is one cache definition: from the operator's config, a repository's
// own file, or a detected recipe.
type Cache struct {
	// Name identifies the cache within its repository ([A-Za-z0-9._-]).
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Recipe names a recipe whose definition this entry takes; the entry's
	// own fields override it.
	Recipe string `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	// Disabled turns the named cache off (a lower-precedence source's
	// definition of it included).
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// Key is a template over the checkout's files ({{hash "go.sum"}},
	// {{os}}, {{arch}}) naming the exact cache to restore.
	Key string `yaml:"key,omitempty" json:"key,omitempty"`
	// RestoreKeys are key prefixes (templates) tried in order when the key
	// misses: the newest cache whose key starts with one is restored.
	RestoreKeys []string `yaml:"restore_keys,omitempty" json:"restore_keys,omitempty"`
	// Paths are what the cache holds: "~/rel" in the agent's home, "rel" in
	// its workspace. Never absolute, never outside either.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// Env are variables the run (and the warm step) gets so its tools use
	// the cached paths; {{home}} and {{workspace}} expand.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Warm is the shell command that fills the cache in a checkout of the
	// default branch ("" = the cache is never warmed, so never published).
	Warm string `yaml:"warm,omitempty" json:"warm,omitempty"`
}

// Recipe is a cache definition with the files that detect it.
type Recipe struct {
	Cache  `yaml:",inline"`
	Detect []string `yaml:"detect,omitempty" json:"detect,omitempty"`
}

// Origins of a cache definition, highest precedence first.
const (
	OriginConfig     = "config"
	OriginRepoFile   = "repo-file"
	OriginAutodetect = "autodetect"
)

// Def is a resolved cache definition and where it came from.
type Def struct {
	Cache
	Origin string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Area is where a cache path lives.
type Area string

const (
	AreaHome      Area = "home"
	AreaWorkspace Area = "workspace"
)

// Path is a parsed cache path.
type Path struct {
	Area Area
	Rel  string // clean, slash-separated, relative, never "." or ".."
}

func (p Path) String() string {
	if p.Area == AreaHome {
		return "~/" + p.Rel
	}
	return p.Rel
}

// ParsePath parses a cache path: "~/rel" is in the agent's home, "rel" in
// its workspace. Absolute paths, "..", ".git" and the home or workspace
// itself are refused; so is a home path that is, holds or sits inside one
// of protected (tool state, credentials, shell start-up files: what a cache
// must never plant or hide).
func ParsePath(s string, protected []string) (Path, error) {
	raw := s
	area := AreaWorkspace
	if r, ok := strings.CutPrefix(s, "~/"); ok {
		area, s = AreaHome, r
	}
	if s == "" || s == "~" || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.Contains(s, `\`) {
		return Path{}, fmt.Errorf("cache path %q: must be ~/<path> in the home or <path> in the workspace", raw)
	}
	for _, c := range strings.Split(s, "/") {
		switch c {
		case "..":
			return Path{}, fmt.Errorf("cache path %q: may not leave the home or workspace", raw)
		case ".git":
			return Path{}, fmt.Errorf("cache path %q: a git directory is never cached", raw)
		}
		if strings.ContainsAny(c, ",:\x00\n") {
			return Path{}, fmt.Errorf("cache path %q: may not contain ',', ':' or control characters", raw)
		}
	}
	rel := path.Clean(s)
	if rel == "." || rel == "" {
		return Path{}, fmt.Errorf("cache path %q: the %s itself is never cached", raw, area)
	}
	if area == AreaHome {
		for _, p := range append(append([]string(nil), alwaysProtected...), protected...) {
			p = path.Clean(strings.TrimPrefix(p, "~/"))
			if overlaps(rel, p) {
				return Path{}, fmt.Errorf("cache path %q: overlaps ~/%s, which a cache may never hold or hide", raw, p)
			}
		}
	}
	return Path{Area: area, Rel: rel}, nil
}

// alwaysProtected are home paths no cache may touch, whatever the tool:
// credentials, git identity and shell start-up files, and conductor's own.
var alwaysProtected = []string{
	".ssh", ".gnupg", ".gitconfig", ".config/git", ".netrc", ".git-credentials",
	".aws", ".kube", ".docker", ".config/gh", ".bashrc", ".bash_profile", ".bash_login",
	".profile", ".zshrc", ".zshenv", ".zprofile", ".zlogin", ".config/fish", ".tmp",
	LayersDir,
}

// LayersDir is the directory in a home that holds the run's overlay upper
// layers: conductor's, hidden from the agent.
const LayersDir = ".conductor-cache-layers"

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// deniedEnv are variables a cache definition may never set: they reach the
// loader, the shell, the network path, git, or conductor itself.
var deniedEnv = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "SHELL": true, "USER": true, "LOGNAME": true,
	"BASH_ENV": true, "ENV": true, "PROMPT_COMMAND": true, "IFS": true, "CDPATH": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "REQUESTS_CA_BUNDLE": true,
	"CURL_CA_BUNDLE": true, "NODE_OPTIONS": true, "PYTHONSTARTUP": true, "PYTHONPATH": true,
	"PERL5OPT": true, "PERL5LIB": true, "RUBYOPT": true, "RUBYLIB": true, "JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS": true, "XDG_CONFIG_HOME": true, "XDG_RUNTIME_DIR": true, "SSH_AUTH_SOCK": true,
}

// deniedEnvPrefix are prefixes of variables a definition may never set.
var deniedEnvPrefix = []string{"LD_", "DYLD_", "GIT_", "CONDUCTOR_", "SSH_", "GPG_"}

// deniedEnvFrag are fragments of credential and proxy variable names.
var deniedEnvFrag = []string{"PROXY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "API_KEY", "APIKEY", "ACCESS_KEY", "PRIVATE_KEY", "AUTH"}

// CheckEnvName refuses a variable a cache definition may not set.
func CheckEnvName(k string) error {
	if !envNameRE.MatchString(k) {
		return fmt.Errorf("cache env %q: not a variable name", k)
	}
	u := strings.ToUpper(k)
	if deniedEnv[u] {
		return fmt.Errorf("cache env %s: may not be set by a cache", k)
	}
	for _, p := range deniedEnvPrefix {
		if strings.HasPrefix(u, p) {
			return fmt.Errorf("cache env %s: %s* may not be set by a cache", k, p)
		}
	}
	for _, f := range deniedEnvFrag {
		if strings.Contains(u, f) {
			return fmt.Errorf("cache env %s: credential and proxy variables may not be set by a cache", k)
		}
	}
	return nil
}

// Validate checks a resolved definition (a recipe reference already
// applied). protected are further home paths a cache may not touch.
func (c Cache) Validate(protected []string) error {
	if !nameRE.MatchString(c.Name) {
		return fmt.Errorf("cache %q: name must be [A-Za-z0-9._-], up to 64 characters", c.Name)
	}
	if c.Disabled {
		return nil
	}
	if strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("cache %s: no key", c.Name)
	}
	if err := checkTemplate(c.Key); err != nil {
		return fmt.Errorf("cache %s: key: %w", c.Name, err)
	}
	for _, rk := range c.RestoreKeys {
		if err := checkTemplate(rk); err != nil {
			return fmt.Errorf("cache %s: restore_keys: %w", c.Name, err)
		}
	}
	if len(c.Paths) == 0 {
		return fmt.Errorf("cache %s: no paths", c.Name)
	}
	if len(c.Paths) > 16 {
		return fmt.Errorf("cache %s: at most 16 paths", c.Name)
	}
	var seen []Path
	for _, s := range c.Paths {
		p, err := ParsePath(s, protected)
		if err != nil {
			return fmt.Errorf("cache %s: %w", c.Name, err)
		}
		for _, q := range seen {
			if q.Area == p.Area && overlaps(q.Rel, p.Rel) {
				return fmt.Errorf("cache %s: paths %s and %s overlap", c.Name, q, p)
			}
		}
		seen = append(seen, p)
	}
	for k, v := range c.Env {
		if err := CheckEnvName(k); err != nil {
			return fmt.Errorf("cache %s: %w", c.Name, err)
		}
		if strings.ContainsAny(v, "\x00\n") {
			return fmt.Errorf("cache %s: env %s: control characters", c.Name, k)
		}
	}
	if strings.ContainsRune(c.Warm, 0) {
		return fmt.Errorf("cache %s: warm: NUL byte", c.Name)
	}
	return nil
}

// ParsedPaths returns the definition's paths parsed (Validate first).
func (c Cache) ParsedPaths(protected []string) []Path {
	var out []Path
	for _, s := range c.Paths {
		if p, err := ParsePath(s, protected); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// ExpandEnv renders the definition's environment for a launch.
func (c Cache) ExpandEnv(home, workspace string) []string {
	r := strings.NewReplacer("{{home}}", home, "{{workspace}}", workspace)
	out := make([]string, 0, len(c.Env))
	for _, k := range sortedKeys(c.Env) {
		out = append(out, k+"="+r.Replace(c.Env[k]))
	}
	return out
}

// merge returns c with o's set fields over it.
func (c Cache) merge(o Cache) Cache {
	if o.Name != "" {
		c.Name = o.Name
	}
	if o.Key != "" {
		c.Key = o.Key
	}
	if o.RestoreKeys != nil {
		c.RestoreKeys = o.RestoreKeys
	}
	if o.Paths != nil {
		c.Paths = o.Paths
	}
	if o.Env != nil {
		env := map[string]string{}
		for k, v := range c.Env {
			env[k] = v
		}
		for k, v := range o.Env {
			env[k] = v
		}
		c.Env = env
	}
	if o.Warm != "" {
		c.Warm = o.Warm
	}
	c.Disabled = o.Disabled
	c.Recipe = ""
	return c
}

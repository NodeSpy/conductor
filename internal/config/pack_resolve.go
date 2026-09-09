package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LockfileName is the sha-pinned, committable lockfile `conductor init` writes
// next to the config. It records the whole resolved pack graph so `conductor
// init` on another machine yields a byte-identical setup (§20), and is
// tamper-evident on `update` (§11).
const LockfileName = "conductor.lock.yaml"

// Lockfile is the resolved pack graph.
type Lockfile struct {
	Version int         `yaml:"version"`
	Packs   []LockEntry `yaml:"packs"`
}

// LockEntry pins one resolved pack node (an instance or a nested dependency).
type LockEntry struct {
	// Instance is the namespace path from the root (e.g. "review" or
	// "review/base").
	Instance string `yaml:"instance"`
	// Name is the pack's canonical identity from its manifest.
	Name string `yaml:"name"`
	// Source is the source as written in the config.
	Source string `yaml:"source"`
	// Version is the requested version constraint, if any.
	Version string `yaml:"version,omitempty"`
	// Resolved is the concrete revision: a git commit sha, or "local" for a
	// path source.
	Resolved string `yaml:"resolved"`
	// Digest is the sha256 of the vendored tree — tamper-evidence on update.
	Digest string `yaml:"digest"`
}

// sourceSpec is a parsed pack source.
type sourceSpec struct {
	git    bool
	local  string // absolute local path (local sources)
	gitURL string // clone URL (git sources)
	subdir string // //subdir within the repo, if any
	ref    string // @ref (tag/branch/sha), if any
}

// parseSource parses a pack source into its transport, location, subdir, and
// ref. Local paths are resolved relative to baseDir. Supported forms:
//
//	./local/path                              (local copy, relative to config)
//	/abs/local/path                           (local copy)
//	github.com/org/repo//subdir@ref           (git over https)
//	https://host/org/repo.git//subdir@ref     (git)
//	git::ssh://git@host/org/repo//subdir@ref  (git, explicit transport)
//	git::file:///path/to/repo//subdir@ref     (git, local repo — used in tests)
func parseSource(src, baseDir string) (sourceSpec, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return sourceSpec{}, fmt.Errorf("empty pack source")
	}
	forceGit := false
	if rest, ok := strings.CutPrefix(s, "git::"); ok {
		forceGit = true
		s = rest
	}
	isGit := forceGit ||
		strings.HasPrefix(s, "github.com/") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://")
	if !isGit {
		// Local path source.
		p := strings.TrimPrefix(s, "file://")
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		return sourceSpec{local: filepath.Clean(p)}, nil
	}

	spec := sourceSpec{git: true}
	// Peel a trailing @ref (only when the tail has no path separator, so an SSH
	// user@host is not mistaken for a ref).
	if at := strings.LastIndex(s, "@"); at >= 0 {
		tail := s[at+1:]
		if !strings.ContainsAny(tail, "/:") {
			spec.ref = tail
			s = s[:at]
		}
	}
	// Split off a //subdir, skipping the scheme's own "://".
	scheme := ""
	if i := strings.Index(s, "://"); i >= 0 {
		scheme = s[:i+3]
		s = s[i+3:]
	}
	if dd := strings.Index(s, "//"); dd >= 0 {
		spec.subdir = strings.Trim(s[dd+2:], "/")
		s = s[:dd]
	}
	repo := scheme + s
	if scheme == "" && strings.HasPrefix(repo, "github.com/") {
		repo = "https://" + repo
	}
	spec.gitURL = repo
	return spec, nil
}

// ResolvePacks fetches every pack declared in the config's `packs:` block into
// the vendor dir, recursing through pack dependencies, and writes the sha-pinned
// lockfile. This is the network step (`conductor init`); everything else is
// offline. Returns the lockfile and any warnings.
func ResolvePacks(configPath string) (*Lockfile, error) {
	dir := filepath.Dir(configPath)
	packs, err := loadPacksBlock(configPath)
	if err != nil {
		return nil, err
	}
	if len(packs) == 0 {
		return &Lockfile{Version: 1}, nil
	}
	vendor := packVendorDir(dir)
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		return nil, err
	}
	r := &resolver{configDir: dir, lock: &Lockfile{Version: 1}}
	for _, name := range sortedPackKeys(packs) {
		if err := r.resolve([]string{name}, packs[name], filepath.Join(vendor, name)); err != nil {
			return nil, err
		}
	}
	sort.Slice(r.lock.Packs, func(i, j int) bool { return r.lock.Packs[i].Instance < r.lock.Packs[j].Instance })
	if err := writeLockfile(dir, r.lock); err != nil {
		return nil, err
	}
	return r.lock, nil
}

type resolver struct {
	configDir string
	lock      *Lockfile
	total     int
}

func (r *resolver) resolve(chain []string, inst PackInstance, destDir string) error {
	ns := strings.Join(chain, "/")
	if len(chain) > MaxPackDepth {
		return fmt.Errorf("pack %q: dependency depth exceeds the limit %d (chain: %s)", ns, MaxPackDepth, strings.Join(chain, " -> "))
	}
	r.total++
	if r.total > MaxTotalPacks {
		return fmt.Errorf("pack %q: total resolved packs exceeds the limit %d", ns, MaxTotalPacks)
	}
	spec, err := parseSource(inst.Source, r.configDir)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	resolved := "local"
	if spec.git {
		if resolved, err = fetchGit(spec, destDir); err != nil {
			return fmt.Errorf("pack %q: fetch %s: %w", ns, inst.Source, err)
		}
	} else {
		if err := fetchLocal(spec, destDir); err != nil {
			return fmt.Errorf("pack %q: fetch %s: %w", ns, inst.Source, err)
		}
	}
	// Read the fetched manifest to learn identity and dependencies.
	man, err := loadPackManifest(destDir)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	digest, err := digestTree(destDir)
	if err != nil {
		return err
	}
	r.lock.Packs = append(r.lock.Packs, LockEntry{
		Instance: ns,
		Name:     man.Pack.Name,
		Source:   inst.Source,
		Version:  inst.Version,
		Resolved: resolved,
		Digest:   digest,
	})
	// Recurse into declared dependencies.
	for _, alias := range sortedPackKeys(inst.Packs) {
		if contains(chain, alias) {
			return fmt.Errorf("pack cycle: %s", strings.Join(append(append([]string{}, chain...), alias), " -> "))
		}
		child := inst.Packs[alias]
		// A dependency may take its source from the instance block or default to
		// the source declared in the parent's requires.packs.
		if child.Source == "" {
			if dep, ok := man.Pack.Requires.Packs[alias]; ok {
				child.Source = dep.Source
				if child.Version == "" {
					child.Version = dep.Version
				}
			}
		}
		if child.Source == "" {
			return fmt.Errorf("pack %q: dependency %q has no source (set packs.%s.source or requires.packs.%s.source)", ns, alias, alias, alias)
		}
		if err := r.resolve(append(append([]string{}, chain...), alias), child, filepath.Join(destDir, ".deps", alias)); err != nil {
			return err
		}
	}
	return nil
}

// fetchLocal copies a local pack tree (optionally a //subdir) into destDir.
func fetchLocal(spec sourceSpec, destDir string) error {
	src := spec.local
	if spec.subdir != "" {
		src = filepath.Join(src, spec.subdir)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}
	return copyTree(src, destDir)
}

// fetchGit clones a pack with the minimal-fetch flags (repo hygiene / §12) and
// returns the resolved commit sha. Shells to the git CLI (the project's git
// transport everywhere); no new dependency.
func fetchGit(spec sourceSpec, destDir string) (string, error) {
	tmp, err := os.MkdirTemp("", "conductor-pack-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	clone := []string{"clone", "--filter=blob:none", "--depth", "1", "--single-branch"}
	if spec.ref != "" {
		clone = append(clone, "--branch", spec.ref)
	}
	clone = append(clone, spec.gitURL, tmp)
	if out, err := runGit("", clone...); err != nil {
		// --branch fails for a raw commit sha; fall back to a full-ish fetch.
		if spec.ref != "" {
			if out2, err2 := gitFetchRef(spec.gitURL, spec.ref, tmp); err2 != nil {
				return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(out+out2))
			}
		} else {
			return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
		}
	}
	sha, err := runGit(tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)

	src := tmp
	if spec.subdir != "" {
		src = filepath.Join(tmp, spec.subdir)
	}
	if err := os.RemoveAll(destDir); err != nil {
		return "", err
	}
	if err := copyTree(src, destDir); err != nil {
		return "", err
	}
	return sha, nil
}

func gitFetchRef(url, ref, dir string) (string, error) {
	if out, err := runGit("", "init", dir); err != nil {
		return out, err
	}
	if out, err := runGit(dir, "remote", "add", "origin", url); err != nil {
		return out, err
	}
	if out, err := runGit(dir, "fetch", "--depth", "1", "origin", ref); err != nil {
		return out, err
	}
	if out, err := runGit(dir, "checkout", "FETCH_HEAD"); err != nil {
		return out, err
	}
	return "", nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Never prompt: a missing credential should fail fast, not hang.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// copyTree recursively copies src into dst, skipping the .git directory.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(path, filepath.Join(dst, rel), info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// digestTree returns a stable sha256 over the vendored tree (paths + contents),
// excluding nested .deps so a parent's digest is independent of its children.
func digestTree(root string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".deps" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n", rel)
		h.Write(b)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// writeLockfile writes the lockfile next to the config.
func writeLockfile(configDir string, lock *Lockfile) error {
	b, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	header := "# conductor pack lockfile — generated by `conductor init`. Commit this.\n"
	return os.WriteFile(filepath.Join(configDir, LockfileName), append([]byte(header), b...), 0o644)
}

// ReadLockfile reads the lockfile next to the config (nil, nil if absent).
func ReadLockfile(configDir string) (*Lockfile, error) {
	b, err := os.ReadFile(filepath.Join(configDir, LockfileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lock Lockfile
	if err := yaml.Unmarshal(b, &lock); err != nil {
		return nil, fmt.Errorf("parse %s: %w", LockfileName, err)
	}
	return &lock, nil
}

// loadPacksBlock extracts just the `packs:` block from a config (with imports
// merged), without the full strict decode or pack instantiation — so it can run
// before packs are vendored.
func loadPacksBlock(path string) (map[string]PackInstance, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded, err := expandEnv(path, raw)
	if err != nil {
		return nil, err
	}
	var probe map[string]any
	if err := yaml.Unmarshal(expanded, &probe); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var doc []byte
	if hasAnyImports(probe) {
		merged, err := loadMerged(path, map[string]bool{})
		if err != nil {
			return nil, err
		}
		if doc, err = yaml.Marshal(merged); err != nil {
			return nil, err
		}
	} else {
		doc = expanded
	}
	var c struct {
		Packs map[string]PackInstance `yaml:"packs"`
	}
	if err := yaml.Unmarshal(doc, &c); err != nil {
		return nil, fmt.Errorf("parse packs: block: %w", err)
	}
	return c.Packs, nil
}

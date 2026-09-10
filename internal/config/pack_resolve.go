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

// Lockfile is the resolved pack graph. It records PACKS only: a pack is config,
// and config belongs in the repo. Plugins are installed BINARIES, and which
// build is installed is a property of the machine — that lives in local install
// state under the state dir instead (internal/plugin.InstallState).
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
	// Harden against argument injection and traversal: a source flows through
	// from a (possibly hostile) parent pack's requires.packs, so refuse a URL,
	// ref, or subdir that could be read as a git option or escape the checkout.
	if strings.HasPrefix(spec.gitURL, "-") || strings.HasPrefix(spec.ref, "-") || strings.HasPrefix(spec.subdir, "-") {
		return sourceSpec{}, fmt.Errorf("pack source %q: URL/ref/subdir may not begin with '-'", src)
	}
	if spec.subdir != "" && !safeSubdir(spec.subdir) {
		return sourceSpec{}, fmt.Errorf("pack source %q: //subdir %q escapes the repository", src, spec.subdir)
	}
	// Transport allowlist: only known-safe git transports. This rejects the git
	// remote-helper transports (ext::/fd::/…) that would run an arbitrary local
	// command as the "remote" — e.g. `git::ext::sh -c '…'` → RCE at fetch time.
	if !safeGitTransport(spec.gitURL) {
		return sourceSpec{}, fmt.Errorf("pack source %q: unsupported git transport — use https://, ssh://, file://, or git@host:path (plaintext http:// and git:// are refused — an unauthenticated fetch can't be safely sha-pinned)", src)
	}
	return spec, nil
}

// safeGitTransport reports whether a git URL uses an allowed transport. Only
// authenticated/encrypted transports — https/ssh/file — and the scp-like
// git@host:path form are permitted. Plaintext transports (http://, git://) are
// refused: the fetch is sha-pinned only AFTER it completes, so a first-fetch
// MITM would poison the pin itself — an unauthenticated fetch is never safe.
// Remote-helper transports (ext::, fd::, transport::…) are also refused.
func safeGitTransport(url string) bool {
	for _, p := range []string{"https://", "ssh://", "file://"} {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	// scp-like: git@host:path (user@host:...), but not a remote-helper `foo::…`.
	if i := strings.Index(url, "::"); i >= 0 {
		return false
	}
	if strings.Contains(url, "@") && strings.Contains(url, ":") {
		return true
	}
	return false
}

// withinDir reports whether target resolves inside root (root itself counts).
func withinDir(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// depAliases returns the sorted union of a pack's DECLARED dependencies
// (requires.packs) and any the consumer instantiated in the block — so a
// declared dependency is auto-pulled even when the consumer adds no override.
func depAliases(required map[string]PackDepReq, provided map[string]PackInstance) []string {
	seen := map[string]bool{}
	var out []string
	for a := range required {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for a := range provided {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// validPackAlias reports whether a pack instance/dependency name is safe to use
// as a vendor-directory component (no path separators, no traversal, no `.`).
func validPackAlias(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// safeSubdir rejects a //subdir that is absolute or contains a `..` segment, so
// a fetched subdir can never resolve outside the cloned/copied tree.
func safeSubdir(sub string) bool {
	if filepath.IsAbs(sub) {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(sub), "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ResolvePacks fetches every pack declared in the config's `packs:` block into
// the vendor dir, recursing through pack dependencies, and writes the sha-pinned
// lockfile. This is the network step (`conductor init`); everything else is
// offline. It enforces the `pack_trust:` source allowlist. Returns the lockfile.
func ResolvePacks(configPath string) (*Lockfile, error) {
	return resolvePacks(configPath, false)
}

// ResolvePacksAllowingUnlisted is ResolvePacks with the `pack_trust:` allowlist
// bypassed — the operator's explicit `conductor init --allow-unlisted` override.
func ResolvePacksAllowingUnlisted(configPath string) (*Lockfile, error) {
	return resolvePacks(configPath, true)
}

func resolvePacks(configPath string, allowUnlisted bool) (*Lockfile, error) {
	dir := filepath.Dir(configPath)
	packs, trust, err := loadPacksBlock(configPath)
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
	// Carry forward the previous lock: its plugin section is preserved (packs
	// don't own it) and its pack revisions back per-instance `hold:`.
	prevLock, _ := ReadLockfile(dir)
	prev := map[string]LockEntry{}
	if prevLock != nil {
		for _, e := range prevLock.Packs {
			prev[e.Instance] = e
		}
	}
	r := &resolver{configDir: dir, lock: &Lockfile{Version: 1}, trust: trust, allowUnlisted: allowUnlisted, prev: prev}
	for _, name := range sortedPackKeys(packs) {
		if !validPackAlias(name) {
			return nil, fmt.Errorf("pack instance name %q is invalid (letters, digits, '-', '_' only)", name)
		}
		if err := r.resolve([]string{name}, nil, packs[name], filepath.Join(vendor, name)); err != nil {
			return nil, err
		}
	}
	sort.Slice(r.lock.Packs, func(i, j int) bool { return r.lock.Packs[i].Instance < r.lock.Packs[j].Instance })
	if prevLock != nil {
	}
	if err := writeLockfile(dir, r.lock); err != nil {
		return nil, err
	}
	return r.lock, nil
}

type resolver struct {
	configDir     string
	lock          *Lockfile
	total         int
	trust         *PackTrustConfig
	allowUnlisted bool
	prev          map[string]LockEntry // previous lock, by instance — for `hold:`
}

func (r *resolver) resolve(chain, nameChain []string, inst PackInstance, destDir string) error {
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
	// Provenance allowlist (§21): a remote source at any depth must be trusted.
	if !r.allowUnlisted && !r.trust.SourceAllowed(inst.Source) {
		return fmt.Errorf("pack %q: source %q is not in pack_trust.allow — add it to the allowlist or re-run with `conductor init --allow-unlisted`", ns, inst.Source)
	}
	// A DEPENDENCY source (depth > 0) comes from a pack author, not the operator.
	// A local dependency source must stay INSIDE the config dir tree, so a hostile
	// pack cannot point requires.packs.dep.source at, say, ../../../etc and read
	// arbitrary directories on the machine running `conductor init`. (Top-level
	// sources are operator-authored and unconfined; git sources need a real repo.)
	if len(chain) > 1 && !spec.git {
		if !withinDir(r.configDir, spec.local) {
			return fmt.Errorf("pack %q: dependency local source %q escapes the config directory — use a remote source or a path inside the project", ns, inst.Source)
		}
	}
	// Version constraints (#59): an unpinned git source (no @ref) with a
	// `version:` constraint resolves to the highest matching tag. A hard @ref
	// pin wins; a bare unpinned/unconstrained source still tracks HEAD. A held
	// instance re-pins to its previously-locked revision instead of re-resolving,
	// so auto-update leaves it frozen (an explicit `pack update` clears the hold
	// path by carrying no prior lock for a changed source).
	if spec.git && spec.ref == "" {
		if inst.Hold {
			if p, ok := r.prev[ns]; ok && p.Resolved != "" && p.Resolved != "local" {
				spec.ref = p.Resolved
			}
		}
		if spec.ref == "" {
			if c := strings.TrimSpace(inst.Version); c != "" {
				tag, err := resolveVersionTag(spec, c)
				if err != nil {
					return fmt.Errorf("pack %q: %w", ns, err)
				}
				spec.ref = tag
			}
		}
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
	// Cycle detection by pack IDENTITY: the same canonical pack name repeating
	// in the ancestry is a cycle even when it is reached under a different alias.
	if contains(nameChain, man.Pack.Name) {
		return fmt.Errorf("pack cycle: %s -> %s", strings.Join(nameChain, " -> "), man.Pack.Name)
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
	// Recurse into every declared dependency (auto-pulled), plus any override the
	// consumer supplied in the block.
	for _, alias := range depAliases(man.Pack.Requires.Packs, inst.Packs) {
		if !validPackAlias(alias) {
			return fmt.Errorf("pack %q: dependency alias %q is invalid (letters, digits, '-', '_' only — it is a directory name)", ns, alias)
		}
		dep, declared := man.Pack.Requires.Packs[alias]
		if !declared {
			return fmt.Errorf("pack %q: %q is not a declared dependency (requires.packs: %s)", ns, alias, depNames(man.Pack.Requires.Packs))
		}
		if contains(chain, alias) {
			return fmt.Errorf("pack cycle: %s", strings.Join(append(append([]string{}, chain...), alias), " -> "))
		}
		child := inst.Packs[alias]
		// A dependency may take its source from the instance block or default to
		// the source declared in the parent's requires.packs.
		if child.Source == "" {
			child.Source = dep.Source
			if child.Version == "" {
				child.Version = dep.Version
			}
		}
		if child.Source == "" {
			return fmt.Errorf("pack %q: dependency %q has no source (set packs.%s.source or requires.packs.%s.source)", ns, alias, alias, alias)
		}
		if err := r.resolve(
			append(append([]string{}, chain...), alias),
			append(append([]string{}, nameChain...), man.Pack.Name),
			child, filepath.Join(destDir, ".deps", alias)); err != nil {
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

// resolveVersionTag lists the source repo's tags and returns the highest one
// satisfying the version constraint. For a //subdir source the tags are
// component-prefixed (`<subdir>/vX.Y.Z`, so one monorepo can version many
// packs); otherwise they are plain (`vX.Y.Z`).
func resolveVersionTag(spec sourceSpec, constraint string) (string, error) {
	out, err := runGit("", "ls-remote", "--tags", "--refs", "--", spec.gitURL)
	if err != nil {
		return "", fmt.Errorf("list tags for %s: %s", spec.gitURL, strings.TrimSpace(out))
	}
	prefix := ""
	if spec.subdir != "" {
		prefix = spec.subdir + "/"
	}
	tags := parseLsRemoteTags(out)
	tag, ok := bestMatch(tags, prefix, constraint)
	if !ok {
		return "", fmt.Errorf("no tag satisfies version %q (looked for %q<semver> among %d tags at %s)", constraint, prefix, len(tags), spec.gitURL)
	}
	return tag, nil
}

// parseLsRemoteTags extracts tag names from `git ls-remote --tags --refs`
// output (lines of "<sha>\trefs/tags/<name>").
func parseLsRemoteTags(out string) []string {
	const marker = "refs/tags/"
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			if name := strings.TrimSpace(line[i+len(marker):]); name != "" {
				tags = append(tags, name)
			}
		}
	}
	return tags
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
	// `--` ends option parsing so a hostile URL can't be read as a git flag
	// (the URL/ref were already refused a leading '-' in parseSource).
	clone = append(clone, "--", spec.gitURL, tmp)
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
	if out, err := runGit(dir, "remote", "add", "origin", "--", url); err != nil {
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
	// Defense-in-depth alongside the parseSource transport allowlist: disable the
	// remote-helper transports at the git level too, so a crafted URL can never
	// spawn an arbitrary command as the "remote".
	full := append([]string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.fd.allow=never",
	}, args...)
	cmd := exec.Command("git", full...)
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
		// Never follow a symlink out of the pack tree: a pack with a
		// `x -> /etc/passwd` link must not copy the target into the vendor dir.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // skip devices/pipes/sockets
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

// PackVendorDir is the exported vendor-dir path for the CLI (`conductor remove`).
func PackVendorDir(configDir string) string { return packVendorDir(configDir) }

// WriteLockfileTo writes a lockfile next to the config (exported for the CLI).
func WriteLockfileTo(configDir string, lock *Lockfile) error { return writeLockfile(configDir, lock) }

// FetchPackForReview fetches a single pack source into a temp dir and returns
// its manifest, for `conductor add` to render an install review WITHOUT touching
// the config or vendor dir. Dependencies are not fetched. The temp dir is
// removed before returning.
func FetchPackForReview(source, baseDir string) (*PackManifest, error) {
	spec, err := parseSource(source, baseDir)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "conductor-pack-review-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "pack")
	if spec.git {
		if _, err := fetchGit(spec, dest); err != nil {
			return nil, err
		}
	} else {
		if err := fetchLocal(spec, dest); err != nil {
			return nil, err
		}
	}
	man, err := loadPackManifest(dest)
	if err != nil {
		return nil, err
	}
	if err := man.checkNoEnvironment(); err != nil {
		return nil, err
	}
	return man, nil
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

// loadPacksBlock extracts the `packs:` and `pack_trust:` blocks from a config
// (with imports merged), without the full strict decode or pack instantiation —
// so it can run before packs are vendored.
func loadPacksBlock(path string) (map[string]PackInstance, *PackTrustConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	expanded, err := expandEnv(path, raw)
	if err != nil {
		return nil, nil, err
	}
	var probe map[string]any
	if err := yaml.Unmarshal(expanded, &probe); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	var doc []byte
	if hasAnyImports(probe) {
		merged, err := loadMerged(path, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		if doc, err = yaml.Marshal(merged); err != nil {
			return nil, nil, err
		}
	} else {
		doc = expanded
	}
	var c struct {
		Packs     map[string]PackInstance `yaml:"packs"`
		PackTrust *PackTrustConfig        `yaml:"pack_trust"`
	}
	if err := yaml.Unmarshal(doc, &c); err != nil {
		return nil, nil, fmt.Errorf("parse packs: block: %w", err)
	}
	return c.Packs, c.PackTrust, nil
}

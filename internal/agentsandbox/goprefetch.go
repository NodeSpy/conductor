package agentsandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// GoPrefetch is sandbox.prefetch: [go]. conductor downloads a dispatch's Go
// modules on the host, into one module cache of its own, before the agent
// launches; every sandboxed agent sees that cache read-only and runs Go
// offline (GOPROXY=off), so no Go host is ever on its allowlist.
//
// The download itself runs CONFINED (rt2 #11): a namespace jail with no
// filesystem but its own scratch dir and the module cache, and no network
// but the configured GOPROXY hosts. It never touches anything an agent can
// write (the module files are read from the workspace while it is still
// conductor's checkout — Workspace, at registration — then copied into a
// temp dir of the download's own) and the cache is mounted read-only in
// every sandbox, so an agent cannot plant a symlink there for the host's go
// to write through. The jail adds a second wall below that: even a
// MALICIOUS go.mod/go.sum this snapshot carries (an event- or
// agent-influenced workspace, before conductor's own checks run) runs its
// `go mod download` with the operator's real HOME/credentials invisible by
// absence and its network cut to exactly the hosts GOPROXY names — not the
// daemon's full view of the machine.
type GoPrefetch struct {
	// Cache is the module cache (GOMODCACHE), shared by every identity: what
	// it holds is public and checksum-verified.
	Cache string
	// Go is the go binary the download runs.
	Go string
	// Proxy is the download's GOPROXY — proxy.golang.org by default. Never
	// `direct` and never a `file://` layer onto the OPERATOR's own
	// GOMODCACHE (rt2 #11): a file:// proxy there would let a private
	// module the operator has cached from one identity's dispatch (or their
	// own box) be served into this ONE SHARED cache every identity reads —
	// the isolation goprefetch exists to give every OTHER identity, quietly
	// defeated. A deployment that wants a private upstream proxy sets one
	// here explicitly (an http(s) mirror it trusts, never a local path).
	Proxy string
	// Now is the clock (time.Now when nil); a test sets it.
	Now func() time.Time
	// Env are the operator variables the download keeps (PATH, the HTTP
	// proxy and CA settings); nothing else of the daemon's reaches it.
	Env []string
	// Timeout bounds one download (default 10m).
	Timeout time.Duration
	// Sandbox is the daemon-side sandbox wiring the confined `go mod
	// download` wraps through (sandbox.WrapLocalCommand's deps) — the same
	// self-exe resolver and egress-proxy minters the controller wires for
	// every other local launch (cmd/conductor's wireAgentSandbox). The zero
	// value means this box's goprefetch has no way to confine the download:
	// Fill then logs and skips the prefetch entirely rather than ever
	// falling back to an unconfined host download (fail closed).
	Sandbox sandbox.LocalWrapDeps
	// Warn logs a non-fatal notice: the download could not be confined (so
	// it was skipped), or the box has no systemd user bus for the optional
	// resource-limit scope (so the download runs confined but unlimited).
	// nil falls back to the standard logger.
	Warn func(format string, args ...any)
}

// warnf emits a non-fatal notice through the wired hook, or the standard
// logger when none is set.
func (g *GoPrefetch) warnf(format string, args ...any) {
	if g.Warn != nil {
		g.Warn(format, args...)
		return
	}
	log.Printf(format, args...)
}

// goPrefetchEnvKeep are the operator variables a download keeps.
var goPrefetchEnvKeep = map[string]bool{
	"PATH": true, "TMPDIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
	"https_proxy": true, "http_proxy": true, "no_proxy": true,
}

// NewGoPrefetch resolves the go binary and the operator's module cache from
// inherited (the daemon's environment) and creates cache.
func NewGoPrefetch(cache string, inherited []string) (*GoPrefetch, error) {
	if !filepath.IsAbs(cache) {
		return nil, fmt.Errorf("agentsandbox: go module cache %q is not an absolute path", cache)
	}
	bin, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("agentsandbox: sandbox.prefetch go: %w", err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, fmt.Errorf("agentsandbox: go module cache: %w", err)
	}
	// Proxy stays the public default (never derived from the operator's own
	// $GOMODCACHE, rt2 #11 — see the Proxy field doc): this ONE cache is
	// shared by every identity, and a file:// layer onto the operator's own
	// module cache would pull whatever private modules it holds (from this
	// box's OWN work, or a past dispatch under a different identity) into
	// it, available to every identity from then on.
	g := &GoPrefetch{Cache: cache, Go: bin, Proxy: "https://proxy.golang.org"}
	for _, kv := range inherited {
		if k, _, ok := strings.Cut(kv, "="); ok && goPrefetchEnvKeep[k] {
			g.Env = append(g.Env, kv)
		}
	}
	return g, nil
}

// AgentEnv is a sandboxed agent's Go: the cache, read-only, and nothing to
// fetch from. The cache's contents were verified when they were downloaded,
// so GOSUMDB is off rather than unreachable.
func (g *GoPrefetch) AgentEnv() []string {
	return []string{"GOMODCACHE=" + g.Cache, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local"}
}

// Workspace downloads the modules ws's Go module files need into the cache,
// with the Go toolchain they select when the host's go is older, and
// records that toolchain for ws's launches (ToolchainBin). A workspace with
// no go.mod is nothing to do.
func (g *GoPrefetch) Workspace(ctx context.Context, ws string) error {
	m, err := SnapshotGoModules(ws)
	if err == nil {
		var goroot string
		goroot, err = g.fill(ctx, m)
		if rerr := g.recordToolchain(ws, goroot); err == nil {
			err = rerr
		}
	}
	return err
}

// Apply gives a launch in ws its Go: the cache read-only, the offline
// environment, and — when ws's module files select a toolchain the
// prefetch put in the cache — that toolchain's bin first on PATH. Its go
// runs with GOTOOLCHAIN=local, so GOSUMDB stays off for every other module.
func (g *GoPrefetch) Apply(p *Policy, ws string) {
	p.ReadOnly = append(append([]string(nil), p.ReadOnly...), g.Cache)
	p.Env = append(append([]string(nil), p.Env...), g.AgentEnv()...)
	if bin := g.ToolchainBin(ws); bin != "" {
		p.PathFirst = append(append([]string(nil), p.PathFirst...), bin)
	}
}

// goToolchainPrefix is where Go keeps a downloaded toolchain in a module
// cache: golang.org/toolchain@v0.0.1-goX.Y.Z.GOOS-GOARCH.
const goToolchainPrefix = "golang.org/toolchain@"

// toolchainRel is goroot relative to the cache when it is a toolchain Go
// downloaded into it, "" otherwise (the host's own GOROOT, or anything
// else).
func (g *GoPrefetch) toolchainRel(goroot string) string {
	if goroot == "" || !filepath.IsAbs(goroot) {
		return ""
	}
	rel, err := filepath.Rel(filepath.Clean(g.Cache), filepath.Clean(goroot))
	if err != nil || !filepath.IsLocal(rel) || !strings.HasPrefix(filepath.ToSlash(rel), goToolchainPrefix) ||
		strings.Count(filepath.ToSlash(rel), "/") != 1 {
		return ""
	}
	return rel
}

// toolchainRecord is where ws's selected toolchain is recorded.
func (g *GoPrefetch) toolchainRecord(ws string) string {
	sum := sha256.Sum256([]byte(canon(ws)))
	return filepath.Join(g.Cache, ".conductor-toolchains", hex.EncodeToString(sum[:]))
}

// recordToolchain records goroot (a toolchain in the cache) as ws's, or
// forgets ws's when there is none.
func (g *GoPrefetch) recordToolchain(ws, goroot string) error {
	rec := g.toolchainRecord(ws)
	rel := g.toolchainRel(goroot)
	if rel == "" {
		if err := os.Remove(rec); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rec), 0o755); err != nil {
		return err
	}
	return os.WriteFile(rec, []byte(rel), 0o644)
}

// ToolchainBin is the bin directory of the toolchain recorded for ws, ""
// when there is none (the host's go is new enough, or nothing was
// prefetched).
func (g *GoPrefetch) ToolchainBin(ws string) string {
	b, err := os.ReadFile(g.toolchainRecord(ws))
	if err != nil {
		return ""
	}
	rel := g.toolchainRel(filepath.Join(g.Cache, strings.TrimSpace(string(b))))
	if rel == "" {
		return ""
	}
	bin := filepath.Join(g.Cache, rel, "bin")
	if fi, err := os.Lstat(filepath.Join(bin, "go")); err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return ""
	}
	return bin
}

// downloadPlan is the local `go mod download` Fill is about to run, resolved
// but not yet confined: argv/dir/env as the UNWRAPPED launch would see them.
// Split out from Fill so the plan's shape — the snapshot staged on disk, the
// env vars, the workspace-vs-single-module GOWORK switch — is covered by a
// fast unit test with no namespace/privilege involved at all; a Linux
// integration test is what proves the CONFINEMENT Fill wraps it in actually
// holds (goprefetch_jail_linux_test.go).
type downloadPlan struct {
	argv []string
	// goroot asks the same go which GOROOT the module files select, after
	// the download: the toolchain it fetched, or the host's own.
	goroot []string
	dir    string // the scratch root (tmp): also what gets bound/chdir'd into the jail
	env    []string
	tmp    string // == dir; named separately so callers don't have to know dir IS the cleanup root
}

// plan stages m's module files into a fresh temp dir and builds the argv/env
// `go mod download` runs with. The caller owns removing p.tmp.
func (g *GoPrefetch) plan(m *GoModules) (*downloadPlan, error) {
	tmp, err := os.MkdirTemp("", "conductor-gomod-")
	if err != nil {
		return nil, err
	}
	src := filepath.Join(tmp, "src")
	for rel, b := range m.Files {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("agentsandbox: go module file %q is not inside the workspace", rel)
		}
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	env := append(append([]string(nil), g.Env...),
		"HOME="+tmp, "GOENV=off", "GOFLAGS=", "GOPATH="+filepath.Join(tmp, "gopath"),
		"GOMODCACHE="+g.Cache, "GOCACHE="+filepath.Join(tmp, "gocache"),
		"GOPROXY="+g.Proxy, "GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOINSECURE=",
		// auto: a go/toolchain line newer than the host's go fetches that
		// toolchain (golang.org/toolchain, verified against the checksum
		// database through the proxy) into the cache and runs it.
		"GOTOOLCHAIN=auto", "GOVCS=*:off", "GIT_TERMINAL_PROMPT=0")
	if _, ok := m.Files["go.work"]; !ok {
		// Never a go.work above the temp dir.
		env = append(env, "GOWORK=off")
	}
	// `-C src` rather than cmd.Dir = src: dir/chdir below is tmp (the single
	// root the jail binds and re-enters into), and -C point the toolchain at
	// the module files inside it without needing a second, nested bind.
	return &downloadPlan{argv: []string{g.Go, "-C", src, "mod", "download"}, goroot: []string{g.Go, "-C", src, "env", "GOROOT"},
		dir: tmp, env: env, tmp: tmp}, nil
}

// Fill downloads what m names into the cache, once per distinct set of
// module files (a marker in the cache records it). The download runs
// CONFINED — a namespace jail with no filesystem but its scratch dir and the
// module cache, no network but the configured GOPROXY hosts (rt2 #11). If
// g.Sandbox has no way to realize that on this box (unwired, or the
// preflight Check refuses — e.g. no `unshare` here), Fill logs and skips the
// prefetch entirely: it NEVER falls back to an unconfined download, so the
// agent simply runs Go offline without these modules, same as if prefetch
// had never been configured.
func (g *GoPrefetch) Fill(ctx context.Context, m *GoModules) error {
	_, err := g.fill(ctx, m)
	return err
}

// fill is Fill, returning the GOROOT the module files select ("" when it
// is not known).
func (g *GoPrefetch) fill(ctx context.Context, m *GoModules) (string, error) {
	if m == nil || len(m.Files) == 0 {
		return "", nil
	}
	// v2: the marker holds the GOROOT the module files select (a v1 marker
	// was written by a download that never fetched a toolchain).
	done := filepath.Join(g.Cache, ".conductor-prefetched-v2", m.Digest())
	if b, err := os.ReadFile(done); err == nil {
		return string(b), nil
	}
	// A download that just failed for these module files fails the same way
	// for every other launch on them (a review starts many at once), so it
	// is not retried until retryFailedAfter has passed.
	failed := filepath.Join(g.Cache, ".conductor-prefetch-failed", m.Digest())
	if fi, err := os.Stat(failed); err == nil && g.now().Sub(fi.ModTime()) < retryFailedAfter {
		return "", ErrPrefetchRecentlyFailed
	}
	goroot, err := g.download(ctx, m, done)
	if err != nil && !errors.Is(err, context.Canceled) {
		if os.MkdirAll(filepath.Dir(failed), 0o755) == nil {
			_ = os.WriteFile(failed, nil, 0o644)
		}
		return "", err
	}
	_ = os.Remove(failed)
	return goroot, err
}

// retryFailedAfter is how long a failed download is not retried for the same
// module files.
const retryFailedAfter = 10 * time.Minute

// ErrPrefetchRecentlyFailed is fill's answer for module files whose download
// failed less than retryFailedAfter ago; the failure itself was logged then.
var ErrPrefetchRecentlyFailed = errors.New("go prefetch: the same module files failed to download recently; not retried yet")

func (g *GoPrefetch) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// download runs the confined download for m and records done.
func (g *GoPrefetch) download(ctx context.Context, m *GoModules, done string) (string, error) {
	p, err := g.plan(m)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(p.tmp)

	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(g.Cache, 0o755); err != nil {
		return "", fmt.Errorf("agentsandbox: go module cache: %w", err)
	}
	spec := &sandbox.Spec{Mode: "namespace", Deny: true, HasEgress: true, Egress: goProxyHosts(g.Proxy)}
	deps := g.Sandbox
	deps.Confine = true
	// The module cache (GOMODCACHE) is the one path outside the scratch root
	// the download needs, read-write — everything else of the operator's
	// (their real HOME, credentials, the rest of the filesystem) is hidden by
	// the jail's absence, same as every other confined code step.
	deps.ExtraBinds = append(deps.ExtraBinds, sandbox.BindMount{Path: g.Cache})
	g.applyLimits(spec)
	// run runs argv confined; ok=false: it could not be confined (logged).
	run := func(argv []string) (stdout []byte, ok bool, err error) {
		wrappedArgv, wrappedEnv, cleanup, werr := sandbox.WrapLocalCommand(spec, argv, p.dir, p.env, deps)
		if werr != nil {
			g.warnf("agent sandbox: go prefetch: cannot confine the download (%v) — skipping (the agent runs Go offline without these modules)", werr)
			return nil, false, nil
		}
		defer cleanup()
		var stderr strings.Builder
		cmd := exec.CommandContext(ctx, wrappedArgv[0], wrappedArgv[1:]...)
		cmd.Env = wrappedEnv
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			msg := urlQuery.ReplaceAllString(strings.TrimSpace(stderr.String()+"\n"+string(out)), "$1?…")
			if len(msg) > 600 {
				msg = "…" + msg[len(msg)-600:]
			}
			return nil, true, fmt.Errorf("go %s: %w: %s", strings.Join(argv[3:], " "), err, msg)
		}
		return out, true, nil
	}
	if _, ok, err := run(p.argv); !ok || err != nil {
		return "", err
	}
	// The download fetched the toolchain the module files select, if the
	// host's go is too old for them; ask which GOROOT that is, jailed the
	// same way, through the same proxy (a toolchain is always checked against
	// the checksum database, which the proxy serves).
	out, ok, err := run(p.goroot)
	if !ok || err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	goroot := strings.TrimSpace(lines[len(lines)-1])
	if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
		return "", err
	}
	return goroot, os.WriteFile(done, []byte(goroot), 0o644)
}

// applyLimits adds the optional cgroup resource ceiling (a conservative
// tasks/pids cap — the download is `go mod download`, not a build, so it
// never legitimately needs many processes) when this box can actually run
// it: `systemd-run --user --scope` needs a running user/session D-Bus, which
// is not guaranteed wherever conductor runs (a system service with no login
// session, a container — and, unlike a login session, NOT reliably signaled
// by XDG_RUNTIME_DIR alone: plenty of non-interactive environments export it
// with no bus actually listening behind it). Rather than let that surface as
// a confusing download failure for an orthogonal reason, probe for it once
// (cheap: a transient scope that only has to CONNECT, not do anything) and
// just run without the limit (logged once) when it is not there — the jail
// itself (namespace + egress allowlist) is the actual security boundary; the
// limit is a resource-exhaustion backstop on top of it.
func (g *GoPrefetch) applyLimits(spec *sandbox.Spec) {
	if !systemdUserBusAvailable() {
		g.warnf("agent sandbox: go prefetch: no systemd user bus here — the confined download runs without a cgroup pids limit")
		return
	}
	spec.Pids = 256
}

// systemdUserBusAvailableFunc is the scope probe (sandbox.ScopeAvailable:
// the launch's own prefix, run with a launch's bare environment), a package
// var so a test can override it.
var systemdUserBusAvailableFunc = sandbox.ScopeAvailable

func systemdUserBusAvailable() bool { return systemdUserBusAvailableFunc() }

// goProxyHosts extracts the egress allow-list from a GOPROXY value: a
// comma-separated (fallback-on-any-error) or pipe-separated
// (fallback-on-404-only) list of proxy URLs, "direct", and "off" — we only
// care about which HOSTS the download may need to reach, not the fallback
// semantics between them, so both separators are treated the same and
// "direct"/"off" (not a reachable proxy host) are skipped.
func goProxyHosts(proxy string) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(proxy, func(r rune) bool { return r == ',' || r == '|' }) {
		part = strings.TrimSpace(part)
		if part == "" || part == "direct" || part == "off" {
			continue
		}
		u, err := url.Parse(part)
		if err != nil || u.Host == "" {
			continue
		}
		host := u.Host
		if u.Port() == "" {
			port := "443"
			if u.Scheme == "http" {
				port = "80"
			}
			host += ":" + port
		}
		for _, h := range append([]string{host}, goProxyRedirects[host]...) {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// goProxyRedirects are the other hosts a download through a known proxy
// reaches. proxy.golang.org answers a large zip (a Go toolchain) with a
// redirect to a signed URL in its storage bucket, and Go verifies a module
// missing from go.sum (a toolchain always is) against the checksum database
// at sum.golang.org directly. The bucket serves only what the proxy signed,
// and every zip is verified whichever host served it, so admitting these
// hosts adds no trust in what is downloaded.
var goProxyRedirects = map[string][]string{
	"proxy.golang.org:443": {"storage.googleapis.com:443", "sum.golang.org:443"},
}

// urlQuery is a URL's query string in an error message: a redirect's signed
// URL carries its signature there, which has no place in a log line.
var urlQuery = regexp.MustCompile(`(https?://[^\s"?]+)\?[^\s"]*`)

// GoModules are a workspace's Go module files — go.work, go.work.sum, each
// module's go.mod and go.sum — keyed by slash path relative to it.
type GoModules struct {
	Files map[string][]byte
}

// Digest names this set of module files.
func (m *GoModules) Digest() string {
	keys := make([]string, 0, len(m.Files))
	for k := range m.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:", len(k), k, len(m.Files[k]))
		h.Write(m.Files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

const (
	goModFileMax  = 4 << 20
	goModTotalMax = 64 << 20
)

// SnapshotGoModules reads ws's Go module files: go.work and the modules it
// uses when there is one, the root module otherwise, and every module a
// local replace points at. Only regular files inside ws are read — a
// symlink, or a path out of ws, is skipped. nil: ws holds no go.mod.
func SnapshotGoModules(ws string) (*GoModules, error) {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	m := &GoModules{Files: map[string][]byte{}}
	total := 0
	read := func(rel string) ([]byte, error) {
		fi, err := root.Lstat(filepath.FromSlash(rel))
		if err != nil || !fi.Mode().IsRegular() {
			return nil, nil
		}
		if fi.Size() > goModFileMax {
			return nil, fmt.Errorf("agentsandbox: %s is %d bytes, over %d", rel, fi.Size(), goModFileMax)
		}
		b, err := root.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			return nil, nil
		}
		if total += len(b); total > goModTotalMax {
			return nil, fmt.Errorf("agentsandbox: %s's go module files are over %d bytes", ws, goModTotalMax)
		}
		m.Files[rel] = b
		return b, nil
	}
	dirs := []string{"."}
	work, err := read("go.work")
	if err != nil {
		return nil, err
	}
	if work != nil {
		if _, err := read("go.work.sum"); err != nil {
			return nil, err
		}
		dirs = nil
		for _, u := range goDirective(work, "use") {
			dirs = append(dirs, localModDir(".", u[0]))
		}
		dirs = append(dirs, localReplaces(".", work)...)
	}
	seen := map[string]bool{}
	mods := 0
	for len(dirs) > 0 {
		d := dirs[0]
		dirs = dirs[1:]
		if d == "" || seen[d] || !filepath.IsLocal(filepath.FromSlash(d)) {
			continue
		}
		seen[d] = true
		mod, err := read(path.Join(d, "go.mod"))
		if err != nil {
			return nil, err
		}
		if mod == nil {
			continue
		}
		mods++
		if _, err := read(path.Join(d, "go.sum")); err != nil {
			return nil, err
		}
		dirs = append(dirs, localReplaces(d, mod)...)
	}
	if mods == 0 {
		return nil, nil
	}
	return m, nil
}

// localReplaces are the module directories data's replace directives point
// at inside the workspace, relative to its root.
func localReplaces(dir string, data []byte) []string {
	var out []string
	for _, args := range goDirective(data, "replace") {
		for i, a := range args {
			if a == "=>" && len(args) == i+2 {
				if d := localModDir(dir, args[i+1]); d != "" {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// localModDir is a local module path (./x, ../y) relative to dir, as a
// slash path from the workspace root; "" for anything else.
func localModDir(dir, p string) string {
	if p != "." && p != ".." && !strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") {
		return ""
	}
	return path.Clean(path.Join(dir, p))
}

// goDirective returns each entry of a go.mod/go.work directive — single-line
// or a ( … ) block — as its fields, unquoted, comments dropped.
func goDirective(data []byte, name string) [][]string {
	var out [][]string
	block := false
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case block && f[0] == ")":
			block = false
		case block:
			out = append(out, unquoteAll(f))
		case f[0] == name && len(f) == 2 && f[1] == "(":
			block = true
		case f[0] == name+"(" && len(f) == 1:
			block = true
		case f[0] == name && len(f) > 1:
			out = append(out, unquoteAll(f[1:]))
		}
	}
	return out
}

func unquoteAll(f []string) []string {
	for i, s := range f {
		if u, err := strconv.Unquote(s); err == nil {
			f[i] = u
		}
	}
	return f
}

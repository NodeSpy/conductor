package agentsandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GoPrefetch is sandbox.prefetch: [go]. conductor downloads a dispatch's Go
// modules on the host, into one module cache of its own, before the agent
// launches; every sandboxed agent sees that cache read-only and runs Go
// offline (GOPROXY=off), so no Go host is ever on its allowlist.
//
// The download runs as the operator, so it never touches anything an agent
// can write: the module files are read from the workspace while it is still
// conductor's checkout (Workspace, at registration), copied into a temp dir
// of the download's own, and the cache is mounted read-only in every
// sandbox — an agent cannot plant a symlink there for the host's go to
// write through.
type GoPrefetch struct {
	// Cache is the module cache (GOMODCACHE), shared by every identity: what
	// it holds is public and checksum-verified.
	Cache string
	// Go is the go binary the download runs.
	Go string
	// Proxy is the download's GOPROXY: the operator's own module cache, then
	// proxy.golang.org. Never `direct` — no VCS runs against a module path a
	// go.mod names.
	Proxy string
	// Env are the operator variables the download keeps (PATH, the HTTP
	// proxy and CA settings); nothing else of the daemon's reaches it.
	Env []string
	// Timeout bounds one download (default 10m).
	Timeout time.Duration
	// Warm is prefetch with conductor's package proxy on for Go: the
	// download goes through the proxy (WarmEnv: its GOPROXY and GOSUMDB),
	// warming its verified cache — the one every agent's Go fetches from —
	// into a module cache of the download's own that is removed after it.
	// Cache then holds only the markers, and a launch's policy is left to
	// the proxy: no read-only cache, Go not offline.
	Warm    bool
	WarmEnv []string
}

// NewGoWarm is prefetch warming the package proxy: proxyEnv is the proxy's
// Go environment for the prefetch's own requester; markers is where Fill
// records what it has warmed.
func NewGoWarm(markers string, inherited, proxyEnv []string) (*GoPrefetch, error) {
	if !filepath.IsAbs(markers) {
		return nil, fmt.Errorf("agentsandbox: go prefetch markers %q is not an absolute path", markers)
	}
	bin, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("agentsandbox: sandbox.prefetch go: %w", err)
	}
	if err := os.MkdirAll(markers, 0o700); err != nil {
		return nil, fmt.Errorf("agentsandbox: go prefetch markers: %w", err)
	}
	// GOPROXY off unless proxyEnv names the proxy: never a default upstream.
	g := &GoPrefetch{Cache: markers, Go: bin, Proxy: "off", Warm: true, WarmEnv: append([]string(nil), proxyEnv...)}
	for _, kv := range inherited {
		if k, _, ok := strings.Cut(kv, "="); ok && goPrefetchEnvKeep[k] {
			g.Env = append(g.Env, kv)
		}
	}
	return g, nil
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
	g := &GoPrefetch{Cache: cache, Go: bin, Proxy: "https://proxy.golang.org"}
	for _, kv := range inherited {
		if k, _, ok := strings.Cut(kv, "="); ok && goPrefetchEnvKeep[k] {
			g.Env = append(g.Env, kv)
		}
	}
	cmd := exec.Command(bin, "env", "GOMODCACHE")
	cmd.Env = inherited
	if out, err := cmd.Output(); err == nil {
		dl := filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
		if fi, err := os.Stat(dl); err == nil && fi.IsDir() && filepath.Clean(dl) != filepath.Join(cache, "cache", "download") {
			g.Proxy = "file://" + filepath.ToSlash(dl) + "," + g.Proxy
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

// Workspace downloads the modules ws's Go module files need into the cache.
// A workspace with no go.mod is nothing to do.
func (g *GoPrefetch) Workspace(ctx context.Context, ws string) error {
	m, err := SnapshotGoModules(ws)
	if err != nil {
		return err
	}
	return g.Fill(ctx, m)
}

// Fill downloads what m names into the cache, once per distinct set of
// module files (a marker in the cache records it).
func (g *GoPrefetch) Fill(ctx context.Context, m *GoModules) error {
	if m == nil || len(m.Files) == 0 {
		return nil
	}
	done := filepath.Join(g.Cache, ".conductor-prefetched", m.Digest())
	if _, err := os.Stat(done); err == nil {
		return nil
	}
	tmp, err := os.MkdirTemp("", "conductor-gomod-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	src := filepath.Join(tmp, "src")
	for rel, b := range m.Files {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return fmt.Errorf("agentsandbox: go module file %q is not inside the workspace", rel)
		}
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return err
		}
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.Go, "mod", "download")
	cmd.Dir = src
	modcache, flags := g.Cache, ""
	if g.Warm {
		// A throwaway cache, writable so it can be removed with tmp.
		modcache, flags = filepath.Join(tmp, "modcache"), "-modcacherw"
	}
	cmd.Env = append(append([]string(nil), g.Env...),
		"HOME="+tmp, "GOENV=off", "GOFLAGS="+flags, "GOPATH="+filepath.Join(tmp, "gopath"),
		"GOMODCACHE="+modcache, "GOCACHE="+filepath.Join(tmp, "gocache"),
		"GOPROXY="+g.Proxy, "GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOINSECURE=",
		"GOTOOLCHAIN=local", "GOVCS=*:off", "GIT_TERMINAL_PROMPT=0")
	if g.Warm {
		// The proxy's GOPROXY, GOSUMDB and GONOSUMDB win (the last of a
		// duplicated key is the one used).
		cmd.Env = append(cmd.Env, g.WarmEnv...)
	}
	if _, ok := m.Files["go.work"]; !ok {
		// Never a go.work above the temp dir.
		cmd.Env = append(cmd.Env, "GOWORK=off")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		return fmt.Errorf("go mod download: %w: %s", err, msg)
	}
	if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
		return err
	}
	return os.WriteFile(done, nil, 0o644)
}

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

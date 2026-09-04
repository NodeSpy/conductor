package vaults

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// execTimeout bounds one external helper invocation (`op read`, `pass show`)
// so a hung helper can't stall a step or config load forever.
const execTimeout = 30 * time.Second

// ExecFn runs an external secret helper with optional stdin and extra
// environment, returning trimmed stdout. Injectable for tests.
type ExecFn func(ctx context.Context, stdin string, env []string, name string, args ...string) (string, error)

// execHelper is the OS-backed ExecFn.
func execHelper(ctx context.Context, stdin string, env []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// cache is the per-backend read cache: reads resolve once per process
// lifetime (config reload rebuilds the backends, dropping it).
type cache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *cache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *cache) put(key, value string) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	c.m[key] = value
	c.mu.Unlock()
}

func (c *cache) drop(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// onepassword — `op read` over the 1Password CLI. Read-only. Keys are op
// item paths without the scheme: {{ vault "op" "Private/GitHub/token" }} →
// `op read -n op://Private/GitHub/token`.
// ---------------------------------------------------------------------------

// OnePasswordVault reads from 1Password via the op CLI. service_account
// (resolved bootstrap material) is passed as OP_SERVICE_ACCOUNT_TOKEN;
// account adds --account for multi-account setups.
type OnePasswordVault struct {
	Account        string
	ServiceAccount string // resolved token ("" = ambient op session)
	Exec           ExecFn
	c              cache
}

func (o *OnePasswordVault) Read(ctx context.Context, key string) (string, error) {
	if v, ok := o.c.get(key); ok {
		return v, nil
	}
	run := o.Exec
	if run == nil {
		run = execHelper
	}
	args := []string{"read", "-n", "op://" + strings.TrimPrefix(key, "op://")}
	if o.Account != "" {
		args = append(args, "--account", o.Account)
	}
	var env []string
	if o.ServiceAccount != "" {
		env = append(env, "OP_SERVICE_ACCOUNT_TOKEN="+o.ServiceAccount)
	}
	v, err := run(ctx, "", env, "op", args...)
	if err != nil {
		return "", err
	}
	o.c.put(key, v)
	return v, nil
}

// ---------------------------------------------------------------------------
// pass — the GPG password store. Writable: `pass insert -m -f` /`pass rm -f`.
// The GPG agent is the unlock mechanism; an optional prefix scopes every key
// under a directory in the store.
// ---------------------------------------------------------------------------

// PassVault reads and writes the pass GPG store.
type PassVault struct {
	Prefix string // optional store subdirectory every key lives under
	Exec   ExecFn
	c      cache
}

func (p *PassVault) entry(key string) string {
	if p.Prefix == "" {
		return key
	}
	return strings.TrimSuffix(p.Prefix, "/") + "/" + key
}

func (p *PassVault) run(ctx context.Context, stdin string, args ...string) (string, error) {
	run := p.Exec
	if run == nil {
		run = execHelper
	}
	return run(ctx, stdin, nil, "pass", args...)
}

func (p *PassVault) Read(ctx context.Context, key string) (string, error) {
	if v, ok := p.c.get(key); ok {
		return v, nil
	}
	out, err := p.run(ctx, "", "show", p.entry(key))
	if err != nil {
		return "", err
	}
	// pass entries may carry extra lines (metadata); the secret is line one.
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	p.c.put(key, out)
	return out, nil
}

func (p *PassVault) Write(ctx context.Context, key, value string) error {
	if _, err := p.run(ctx, value+"\n", "insert", "-m", "-f", p.entry(key)); err != nil {
		return err
	}
	p.c.put(key, value)
	return nil
}

func (p *PassVault) Delete(ctx context.Context, key string) error {
	if _, err := p.run(ctx, "", "rm", "-f", p.entry(key)); err != nil {
		return err
	}
	p.c.drop(key)
	return nil
}

// ---------------------------------------------------------------------------
// file — a mounted directory of secret files (k8s/docker/systemd secrets).
// Read-only; the mounting system owns writes. A key is a path relative to
// dir and may not escape it.
// ---------------------------------------------------------------------------

// FileVault reads secrets from files under one directory.
type FileVault struct {
	Dir      string
	ReadFile func(string) ([]byte, error) // default os.ReadFile
	ReadDir  func(string) ([]os.DirEntry, error)
	c        cache
}

func (f *FileVault) Read(ctx context.Context, key string) (string, error) {
	if v, ok := f.c.get(key); ok {
		return v, nil
	}
	clean := filepath.Clean(key)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("key %q escapes the vault directory", key)
	}
	read := f.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	raw, err := read(filepath.Join(expandHome(f.Dir), clean))
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(raw), "\r\n")
	f.c.put(key, v)
	return v, nil
}

func (f *FileVault) List(ctx context.Context) ([]string, error) {
	readDir := f.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	ents, err := readDir(expandHome(f.Dir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		// Skip directories and the ..data/..yyyy symlink machinery k8s
		// projected volumes use.
		if e.IsDir() || strings.HasPrefix(e.Name(), "..") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

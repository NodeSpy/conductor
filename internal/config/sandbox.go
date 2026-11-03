package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// SandboxConfig is the top-level `sandbox:` block: conductor's agent sandbox
// (docs/design/agent-sandbox.md). Agents are read-only and conductor
// performs every write: every local cli/acp agent launch, and every paseo
// session started through `conductor sandbox exec`, runs under the built-in
// sandbox with its workspace read-write, a per-identity HOME, the conductor
// socket, enforced egress to the model endpoints (plus `egress:`), and none
// of the operator's credentials. It fails closed.
type SandboxConfig struct {
	// Agents turns the sandbox for agent launches on (the default) or off.
	// `off` is the explicit opt-out: agents run as they did before — with
	// the daemon's view of the machine and the operator's credentials — and
	// conductor logs that it is so at boot. A runtime/step `isolation:`
	// block with `mode: none` opts one runtime or step out the same way;
	// mode user/container, or namespace with `privileged: true`, keep their
	// own wrappers.
	Agents string `yaml:"agents,omitempty"`
	// Egress are allowlist patterns every sandboxed agent may reach beyond
	// its model endpoints ("registry.npmjs.org", "*.golang.org",
	// "host:port"). A bare host means :443.
	Egress []string `yaml:"egress,omitempty"`
	// Prefetch are the package ecosystems conductor downloads for an agent
	// on the host, before its launch, so the agent builds offline with none
	// of their hosts on its allowlist. "go": a dispatch's Go modules (its
	// checkout's go.work / go.mod) into one conductor-owned module cache,
	// read-only in every sandbox, with the agent's GOPROXY off.
	Prefetch []string `yaml:"prefetch,omitempty"`
	// Packages is conductor's package proxy: a dependency mirror conductor
	// serves to every sandboxed agent, fetching upstream with its own
	// network, so no package host is ever on an agent's allowlist. Go is on
	// by default whenever the sandbox is.
	Packages *SandboxPackages `yaml:"packages,omitempty"`
	// StateDir holds the per-identity tool state (each identity's HOME).
	// Default: <state dir>/agent-state.
	StateDir string `yaml:"state_dir,omitempty" path:"local"`
	// Push configures conductor's pushes of agents' work (git.push and the
	// end-of-step push).
	Push *SandboxPush `yaml:"push,omitempty"`
}

// SandboxPush configures how conductor pushes and signs agents' commits.
type SandboxPush struct {
	// AtStepEnd pushes a sandboxed agent step's workspace HEAD to its
	// dispatch branch when the step ends (default true).
	AtStepEnd *bool `yaml:"at_step_end,omitempty"`
	// Sign signs every pushed commit with SigningKey (default true). With
	// signing on and no key resolvable, a push is refused.
	Sign *bool `yaml:"sign,omitempty"`
	// SigningKey is the SSH key commits are signed with: a private key file,
	// or a .pub whose private half is in the ssh-agent. Default: git's own
	// user.signingkey when gpg.format is ssh.
	SigningKey string `yaml:"signing_key,omitempty" path:"local"`
	// Name / Email are the identity signed commits are committed as.
	// Default: git's user.name / user.email.
	Name  string `yaml:"name,omitempty"`
	Email string `yaml:"email,omitempty"`
	// Unlanded is what happens to a sandboxed step whose agent committed
	// work conductor did not push at the step's end (no branch to push to,
	// a refused push): `fail` (default) fails the step — the work would be
	// lost with its workspace — unless the step is an explicit review (a
	// review trigger, a decide step, `checkout: none`); `warn` only records
	// it (the step's push output, the log, a git_push audit row), which
	// happens either way.
	Unlanded string `yaml:"unlanded,omitempty"`
}

// SandboxPackages configures the package proxy (sandbox.packages).
type SandboxPackages struct {
	// CacheMax bounds the proxy's shared on-disk cache (default 10GB); the
	// least recently used artifacts are evicted past it.
	CacheMax ByteSize `yaml:"cache_max,omitempty"`
	// Go is the Go module proxy (GOPROXY + checksum database).
	Go *GoPackages `yaml:"go,omitempty"`
}

// GoPackages configures the Go module proxy.
type GoPackages struct {
	// Enabled turns it off (false); default on with the agent sandbox.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Upstream is the one GOPROXY conductor fetches from (default
	// https://proxy.golang.org). An agent never names a host: it asks for a
	// module path and version, and conductor asks exactly this server.
	Upstream string `yaml:"upstream,omitempty"`
	// SumDB is the checksum database every module is verified against, in
	// GOSUMDB form: "name", "name+key" or "name+key url" (default
	// sum.golang.org). It cannot be turned off.
	SumDB string `yaml:"sumdb,omitempty"`
	// Private are module path patterns (GOPRIVATE syntax) served from
	// Upstream without the checksum database — only with an Upstream of
	// your own; never cached. Any other module the checksum database does
	// not know is refused.
	Private []string `yaml:"private,omitempty"`
}

// DefaultGoUpstream / DefaultGoSumDB are the Go module proxy's defaults.
const (
	DefaultGoUpstream = "https://proxy.golang.org"
	DefaultGoSumDB    = "sum.golang.org"
)

// DefaultPackageCacheMax bounds the package proxy's cache when cache_max is unset.
const DefaultPackageCacheMax = ByteSize(10 << 30)

// GoPackageProxyOn reports the Go module proxy on: the agent sandbox is on
// and sandbox.packages.go.enabled is not false.
func (c *Config) GoPackageProxyOn() bool {
	if !c.AgentSandboxOn() {
		return false
	}
	if c == nil || c.Sandbox == nil || c.Sandbox.Packages == nil || c.Sandbox.Packages.Go == nil {
		return true
	}
	e := c.Sandbox.Packages.Go.Enabled
	return e == nil || *e
}

// GoPackages returns the Go module proxy's settings with defaults filled in.
func (s *SandboxConfig) GoPackages() GoPackages {
	var g GoPackages
	if s != nil && s.Packages != nil && s.Packages.Go != nil {
		g = *s.Packages.Go
	}
	if g.Upstream == "" {
		g.Upstream = DefaultGoUpstream
	}
	if g.SumDB == "" {
		g.SumDB = DefaultGoSumDB
	}
	return g
}

// PackageCacheMax is sandbox.packages.cache_max, defaulted.
func (s *SandboxConfig) PackageCacheMax() int64 {
	if s != nil && s.Packages != nil && s.Packages.CacheMax > 0 {
		return s.Packages.CacheMax.Bytes()
	}
	return DefaultPackageCacheMax.Bytes()
}

func validatePackages(p *SandboxPackages) error {
	if p == nil {
		return nil
	}
	if p.CacheMax < 0 {
		return fmt.Errorf("config: sandbox.packages.cache_max must be positive")
	}
	g := p.Go
	if g == nil {
		return nil
	}
	if g.Upstream != "" {
		u, err := url.Parse(g.Upstream)
		if err != nil || strings.ContainsAny(g.Upstream, ",| \t") || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("config: sandbox.packages.go.upstream %q must be one http(s) GOPROXY URL (no list, no credentials in it, never direct/off)", g.Upstream)
		}
	}
	if f := strings.Fields(g.SumDB); g.SumDB != "" && (len(f) > 2 || f[0] == "off" || strings.HasPrefix(f[0], "off+")) {
		return fmt.Errorf("config: sandbox.packages.go.sumdb %q: checksum verification cannot be turned off — give a name, name+key or \"name+key url\"", g.SumDB)
	}
	if len(g.Private) > 0 && (g.Upstream == "" || strings.TrimRight(g.Upstream, "/") == DefaultGoUpstream) {
		return fmt.Errorf("config: sandbox.packages.go.private needs an upstream of your own — private module paths are never sent to %s", DefaultGoUpstream)
	}
	for _, pat := range g.Private {
		if strings.TrimSpace(pat) == "" || strings.Contains(pat, ",") {
			return fmt.Errorf("config: sandbox.packages.go.private: %q is not one module path pattern", pat)
		}
	}
	return nil
}

// AgentSandboxOn reports whether agent launches are sandboxed (the default).
func (c *Config) AgentSandboxOn() bool {
	return c == nil || c.Sandbox == nil || c.Sandbox.Agents == "" || c.Sandbox.Agents == "on"
}

// PushAtStepEnd reports the end-of-step push (default on).
func (s *SandboxConfig) PushAtStepEnd() bool {
	return s == nil || s.Push == nil || s.Push.AtStepEnd == nil || *s.Push.AtStepEnd
}

// UnlandedFails reports whether committed work conductor did not push
// fails its step (sandbox.push.unlanded, default fail).
func (s *SandboxConfig) UnlandedFails() bool {
	return s == nil || s.Push == nil || s.Push.Unlanded != "warn"
}

// PrefetchGo reports sandbox.prefetch naming go.
func (s *SandboxConfig) PrefetchGo() bool {
	if s == nil {
		return false
	}
	for _, p := range s.Prefetch {
		if p == "go" {
			return true
		}
	}
	return false
}

// PushSign reports commit signing on push (default on).
func (s *SandboxConfig) PushSign() bool {
	return s == nil || s.Push == nil || s.Push.Sign == nil || *s.Push.Sign
}

func (c *Config) validateSandbox() error {
	s := c.Sandbox
	if s == nil {
		return nil
	}
	switch s.Agents {
	case "", "on", "off":
	default:
		return fmt.Errorf("config: sandbox.agents must be on|off, got %q", s.Agents)
	}
	for _, e := range s.Egress {
		if e == "" {
			return fmt.Errorf("config: sandbox.egress: empty pattern")
		}
	}
	for _, p := range s.Prefetch {
		if p != "go" {
			return fmt.Errorf("config: sandbox.prefetch: unknown ecosystem %q (supported: go)", p)
		}
	}
	if err := validatePackages(s.Packages); err != nil {
		return err
	}
	if s.StateDir != "" && !filepath.IsAbs(s.StateDir) {
		return fmt.Errorf("config: sandbox.state_dir %q must be an absolute path", s.StateDir)
	}
	if p := s.Push; p != nil && p.SigningKey != "" && !filepath.IsAbs(p.SigningKey) {
		return fmt.Errorf("config: sandbox.push.signing_key %q must be an absolute path", p.SigningKey)
	}
	if p := s.Push; p != nil {
		switch p.Unlanded {
		case "", "fail", "warn":
		default:
			return fmt.Errorf("config: sandbox.push.unlanded must be fail|warn, got %q", p.Unlanded)
		}
	}
	return nil
}

// SandboxStateDir is the per-identity tool-state root.
func (c *Config) SandboxStateDir() string {
	if c.Sandbox != nil && c.Sandbox.StateDir != "" {
		return c.Sandbox.StateDir
	}
	return filepath.Join(filepath.Dir(c.Store.StateFile), "agent-state")
}

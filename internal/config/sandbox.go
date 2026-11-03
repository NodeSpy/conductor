package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	// StateDir holds the per-identity tool state (each identity's HOME).
	// Default: <state dir>/agent-state.
	StateDir string `yaml:"state_dir,omitempty" path:"local"`
	// Push configures conductor's pushes of agents' work (git.push and the
	// end-of-step push).
	Push *SandboxPush `yaml:"push,omitempty"`
	// Git configures what of a shared git dir an agent may write.
	Git *SandboxGit `yaml:"git,omitempty"`
	// ModelCredential is how an agent reaches its model with the operator's
	// credential: `gateway` (default) — conductor's model gateway holds the
	// real key and the agent gets a per-launch token; a tool or login the
	// gateway cannot serve falls back to `login-copy`. `login-copy`: the
	// identity home gets a copy of the login file without its refresh
	// token, and API keys stay in the agent's environment. `login-share`:
	// today's behavior before this knob — the operator's login file shared
	// read-write and API keys in the environment.
	ModelCredential string `yaml:"model_credential,omitempty"`
	// EgressRequireTLS (default true): every CONNECT tunnel must open with a
	// TLS ClientHello whose SNI names the CONNECT host, checked before the
	// upstream is dialed; false lets any bytes through an allowed tunnel.
	EgressRequireTLS *bool `yaml:"egress_require_tls,omitempty"`
	// EgressLimits bound each launch's egress session.
	EgressLimits *SandboxEgressLimits `yaml:"egress_limits,omitempty"`
	// Detect configures the host-side watcher, tripwires and response.
	Detect *SandboxDetect `yaml:"detect,omitempty"`
	// Allow is the base allowance of every agent launch (see SandboxAllow);
	// an isolation: block's allow: adds to it for its own scope.
	Allow *SandboxAllow `yaml:"allow,omitempty"`
	// Limits are the default resource limits of every agent launch.
	Limits *SandboxLimits `yaml:"limits,omitempty"`
	// Persist is what of an identity home outlives its dispatch: `none`
	// (default) — a fresh home per dispatch or session; `cache` — the
	// identity's XDG/Library cache dir is kept (a run can plant build-cache
	// entries later runs of the identity execute); `home` — the whole home
	// is kept (a run can plant settings, hooks and shell rc files).
	Persist string `yaml:"persist,omitempty"`
	// MacOSPosixIPC (macOS) lets agents use POSIX shared memory and
	// semaphores (Python multiprocessing needs them). Off by default: they
	// are named host-wide, so an agent could open a host process's.
	MacOSPosixIPC bool `yaml:"macos_posix_ipc,omitempty"`
}

// SandboxGit configures shared git dirs.
type SandboxGit struct {
	// Shared is what an agent in a linked worktree may write of the shared
	// common git dir: `protected` (default) — objects, refs, logs and lfs
	// only; `direct` — the whole dir but its config, hooks, info, modules,
	// alternates, replace refs and shallow graft.
	Shared string `yaml:"shared,omitempty"`
}

// SandboxEgressLimits bound one launch's egress. Each field takes "off".
type SandboxEgressLimits struct {
	// Idle closes a tunnel with no bytes either way for this long
	// (default 10m).
	Idle string `yaml:"idle,omitempty"`
	// Upload / Download cap the bytes a launch sends / receives through the
	// egress proxy (defaults 512MiB / 20GiB; model traffic through the
	// gateway is not counted). A launch over its cap has its tunnels closed
	// and further CONNECTs refused.
	Upload   string `yaml:"upload,omitempty"`
	Download string `yaml:"download,omitempty"`
	// ModelUpload / ModelDownload cap the bytes a launch sends / receives
	// through its model gateway (defaults 16GiB each: a model client sends
	// its whole context with every request). A launch over its cap has
	// further model requests refused.
	ModelUpload   string `yaml:"model_upload,omitempty"`
	ModelDownload string `yaml:"model_download,omitempty"`
}

// sizes are the byte-size knobs by name.
func (l *SandboxEgressLimits) sizes() map[string]string {
	return map[string]string{"upload": l.Upload, "download": l.Download, "model_upload": l.ModelUpload, "model_download": l.ModelDownload}
}

// SandboxDetect configures detection.
type SandboxDetect struct {
	// Action on a detection: `kill` (default) — every process of the
	// launch is killed (the step ends as its agent does), its workspace
	// kept, the event audited and raised as a conductor.escalate
	// notification; `alert` — audited and notified only; `off` — no
	// watcher, tripwires or git guard (what a launch leaves running is
	// still killed at its end).
	Action string `yaml:"action,omitempty"`
	// Tripwires plants decoy credentials in each launch's home and treats a
	// read as a detection (default true).
	Tripwires *bool `yaml:"tripwires,omitempty"`
	// Interval is how often the process watcher scans a launch (default 3s).
	Interval string `yaml:"interval,omitempty"`
	// Allow names programs the watcher lets run though they are tunnel
	// tools (ssh, socat, nc, …): an agent whose work needs one.
	Allow []string `yaml:"allow,omitempty"`
}

// SandboxLimits are default resource limits for agent launches.
type SandboxLimits struct {
	// Off disables the defaults (a step's own isolation.limits still apply).
	Off bool `yaml:"off,omitempty"`
	// Memory caps one launch (default 50%; systemd syntax: 4G, 50%).
	Memory string `yaml:"memory,omitempty"`
	// Pids caps one launch's tasks (default 8192).
	Pids int `yaml:"pids,omitempty"`
	// AggregateMemory caps all launches together (default 75%).
	AggregateMemory string `yaml:"aggregate_memory,omitempty"`
	// DiskFreeMin is the free space below which the disk guard refuses a
	// launch and stops a running one on that filesystem (default 5%; or a
	// size: 5GiB; or "off").
	DiskFreeMin string `yaml:"disk_free_min,omitempty"`
	// Required refuses a launch whose limits cannot be applied (no user
	// systemd on Linux) instead of warning once and running without them.
	Required bool `yaml:"required,omitempty"`
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
	// AllowCIChanges lets a push carry commits that change CI definitions
	// (.github/workflows, .gitlab-ci.yml, …). Default false: such a push is
	// refused — a push-triggered pipeline would run the agent's code with
	// the repository's secrets.
	AllowCIChanges bool `yaml:"allow_ci_changes,omitempty"`
	// Unlanded is what happens to a sandboxed step whose agent committed
	// work conductor did not push at the step's end (no branch to push to,
	// a refused push): `fail` (default) fails the step — the work would be
	// lost with its workspace — unless the step is an explicit review (a
	// review trigger, a decide step, `checkout: none`); `warn` only records
	// it (the step's push output, the log, a git_push audit row), which
	// happens either way.
	Unlanded string `yaml:"unlanded,omitempty"`
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

// PushAllowsCIChanges reports sandbox.push.allow_ci_changes (default off).
func (s *SandboxConfig) PushAllowsCIChanges() bool {
	return s != nil && s.Push != nil && s.Push.AllowCIChanges
}

// GitSharedDirect reports sandbox.git.shared: direct.
func (s *SandboxConfig) GitSharedDirect() bool {
	return s != nil && s.Git != nil && s.Git.Shared == "direct"
}

// ModelCredentialMode is sandbox.model_credential (default gateway).
func (s *SandboxConfig) ModelCredentialMode() string {
	if s == nil || s.ModelCredential == "" {
		return "gateway"
	}
	return s.ModelCredential
}

// EgressTLSRequired reports sandbox.egress_require_tls (default true).
func (s *SandboxConfig) EgressTLSRequired() bool {
	return s == nil || s.EgressRequireTLS == nil || *s.EgressRequireTLS
}

// DetectAction is sandbox.detect.action (default kill).
func (s *SandboxConfig) DetectAction() string {
	if s == nil || s.Detect == nil || s.Detect.Action == "" {
		return "kill"
	}
	return s.Detect.Action
}

// TripwiresOn reports sandbox.detect.tripwires (default on; off with
// detection off).
func (s *SandboxConfig) TripwiresOn() bool {
	if s.DetectAction() == "off" {
		return false
	}
	return s == nil || s.Detect == nil || s.Detect.Tripwires == nil || *s.Detect.Tripwires
}

// DetectInterval is sandbox.detect.interval (0: the default).
func (s *SandboxConfig) DetectInterval() time.Duration {
	if s == nil || s.Detect == nil {
		return 0
	}
	d, _ := ParseLimitDuration(s.Detect.Interval)
	if d < 0 {
		return 0
	}
	return d
}

// DetectAllow is sandbox.detect.allow.
func (s *SandboxConfig) DetectAllow() []string {
	if s == nil || s.Detect == nil {
		return nil
	}
	return s.Detect.Allow
}

// PosixIPCAllowed reports sandbox.macos_posix_ipc (default off).
func (s *SandboxConfig) PosixIPCAllowed() bool { return s != nil && s.MacOSPosixIPC }

// PersistMode is sandbox.persist (default none).
func (s *SandboxConfig) PersistMode() string {
	if s == nil || s.Persist == "" {
		return "none"
	}
	return s.Persist
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
	if g := s.Git; g != nil {
		switch g.Shared {
		case "", "protected", "direct":
		default:
			return fmt.Errorf("config: sandbox.git.shared must be protected|direct, got %q", g.Shared)
		}
	}
	switch s.ModelCredential {
	case "", "gateway", "login-copy", "login-share":
	default:
		return fmt.Errorf("config: sandbox.model_credential must be gateway|login-copy|login-share, got %q", s.ModelCredential)
	}
	if err := validateSandboxAllow("sandbox.allow", s.Allow); err != nil {
		return err
	}
	switch s.Persist {
	case "", "none", "cache", "home":
	default:
		return fmt.Errorf("config: sandbox.persist must be none|cache|home, got %q", s.Persist)
	}
	if l := s.EgressLimits; l != nil {
		if _, err := ParseLimitDuration(l.Idle); err != nil {
			return fmt.Errorf("config: sandbox.egress_limits.idle: %w", err)
		}
		for name, v := range l.sizes() {
			if _, err := ParseByteSize(v); err != nil {
				return fmt.Errorf("config: sandbox.egress_limits.%s: %w", name, err)
			}
		}
	}
	if d := s.Detect; d != nil {
		switch d.Action {
		case "", "kill", "alert", "off":
		default:
			return fmt.Errorf("config: sandbox.detect.action must be kill|alert|off, got %q", d.Action)
		}
		if _, err := ParseLimitDuration(d.Interval); err != nil {
			return fmt.Errorf("config: sandbox.detect.interval: %w", err)
		}
	}
	if l := s.Limits; l != nil {
		if l.Pids < 0 {
			return fmt.Errorf("config: sandbox.limits.pids must not be negative")
		}
		for name, v := range map[string]string{"memory": l.Memory, "aggregate_memory": l.AggregateMemory} {
			if v != "" && !limitValue.MatchString(v) {
				return fmt.Errorf("config: sandbox.limits.%s %q: want a percentage (50%%) or a size (4G)", name, v)
			}
		}
		if v := l.DiskFreeMin; v != "" && !strings.HasSuffix(v, "%") && !strings.EqualFold(v, "off") {
			if _, err := ParseByteSize(v); err != nil {
				return fmt.Errorf("config: sandbox.limits.disk_free_min: %w", err)
			}
		}
	}
	return nil
}

// limitValue is a systemd memory value: a percentage or a size.
var limitValue = regexp.MustCompile(`^([0-9]{1,3}%|[0-9]+[KMGT]?)$`)

// ParseByteSize parses a byte size ("512MiB", "20GiB", "1G", "4096"); ""
// is 0 (unset) and "off" is -1 (no cap).
func ParseByteSize(v string) (int64, error) {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "":
		return 0, nil
	case "off":
		return -1, nil
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q is not a size", v)
	}
	n, err := strconv.ParseInt(v[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", v)
	}
	mult := map[string]int64{"": 1, "b": 1, "k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10, "m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
		"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30, "t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40}[strings.ToLower(strings.TrimSpace(v[i:]))]
	if mult == 0 {
		return 0, fmt.Errorf("%q: unknown unit", v)
	}
	return n * mult, nil
}

// DiskFreeMin is sandbox.limits.disk_free_min parsed: a fraction ("5%"),
// a size, or off. All zero: the default.
func (s *SandboxConfig) DiskFreeMin() (frac float64, bytes int64, off bool) {
	if s == nil || s.Limits == nil {
		return 0, 0, false
	}
	v := strings.TrimSpace(s.Limits.DiskFreeMin)
	if strings.EqualFold(v, "off") {
		return 0, 0, true
	}
	if p, ok := strings.CutSuffix(v, "%"); ok {
		f, _ := strconv.ParseFloat(p, 64)
		return f / 100, 0, false
	}
	b, _ := ParseByteSize(v)
	return 0, b, false
}

// AggregateMemory is sandbox.limits.aggregate_memory (default 75%).
func (s *SandboxConfig) AggregateMemory() string {
	if s == nil || s.Limits == nil || s.Limits.AggregateMemory == "" {
		return "75%"
	}
	return s.Limits.AggregateMemory
}

// AgentEgressLimits are sandbox.egress_limits parsed: idle, upload and
// download, each 0 for the default and -1 for off. Validate has checked
// them; a value that does not parse is the default.
func (s *SandboxConfig) AgentEgressLimits() (idle time.Duration, upload, download int64) {
	if s == nil || s.EgressLimits == nil {
		return 0, 0, 0
	}
	idle, _ = ParseLimitDuration(s.EgressLimits.Idle)
	upload, _ = ParseByteSize(s.EgressLimits.Upload)
	download, _ = ParseByteSize(s.EgressLimits.Download)
	return idle, upload, download
}

// AgentModelLimits are sandbox.egress_limits' model caps parsed: 0 for the
// default, -1 for off.
func (s *SandboxConfig) AgentModelLimits() (upload, download int64) {
	if s == nil || s.EgressLimits == nil {
		return 0, 0
	}
	upload, _ = ParseByteSize(s.EgressLimits.ModelUpload)
	download, _ = ParseByteSize(s.EgressLimits.ModelDownload)
	return upload, download
}

// ParseLimitDuration parses a duration knob: "" is 0 (unset), "off" is -1.
func ParseLimitDuration(v string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return 0, nil
	case "off":
		return -1, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration", v)
	}
	return d, nil
}

// SandboxStateDir is the per-identity tool-state root.
func (c *Config) SandboxStateDir() string {
	if c.Sandbox != nil && c.Sandbox.StateDir != "" {
		return c.Sandbox.StateDir
	}
	return filepath.Join(filepath.Dir(c.Store.StateFile), "agent-state")
}

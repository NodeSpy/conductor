package config

import (
	"fmt"
	"path/filepath"
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
	// StateDir holds the per-identity tool state (each identity's HOME).
	// Default: <state dir>/agent-state.
	StateDir string `yaml:"state_dir,omitempty"`
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
	SigningKey string `yaml:"signing_key,omitempty"`
	// Name / Email are the identity signed commits are committed as.
	// Default: git's user.name / user.email.
	Name  string `yaml:"name,omitempty"`
	Email string `yaml:"email,omitempty"`
}

// AgentSandboxOn reports whether agent launches are sandboxed (the default).
func (c *Config) AgentSandboxOn() bool {
	return c == nil || c.Sandbox == nil || c.Sandbox.Agents == "" || c.Sandbox.Agents == "on"
}

// PushAtStepEnd reports the end-of-step push (default on).
func (s *SandboxConfig) PushAtStepEnd() bool {
	return s == nil || s.Push == nil || s.Push.AtStepEnd == nil || *s.Push.AtStepEnd
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
	if s.StateDir != "" && !filepath.IsAbs(expandHome(s.StateDir)) {
		return fmt.Errorf("config: sandbox.state_dir %q must be an absolute path", s.StateDir)
	}
	if p := s.Push; p != nil && p.SigningKey != "" && !filepath.IsAbs(expandHome(p.SigningKey)) {
		return fmt.Errorf("config: sandbox.push.signing_key %q must be an absolute path", p.SigningKey)
	}
	return nil
}

// SandboxStateDir is the per-identity tool-state root.
func (c *Config) SandboxStateDir() string {
	if c.Sandbox != nil && c.Sandbox.StateDir != "" {
		return expandHome(c.Sandbox.StateDir)
	}
	return filepath.Join(filepath.Dir(c.Store.StateFile), "agent-state")
}

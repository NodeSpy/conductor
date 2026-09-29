package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HostCommand is one binary's entry under `isolation.host` (#154 §2). The
// three YAML forms:
//
//	gh: {}                          # a host command, guardrails only
//	aws: {allow: ["s3 ls *"]}       # an allow list ⇒ ONLY these may run
//	docker: false                   # never reachable from the jail
//
// Rules match the PARSED command for a binary with a built-in profile (gh's
// `pr merge`, kubectl's verb + resource, aws's service + operation), so flag
// order and `--flag=value` spellings cannot slip past a rule; a binary with
// no profile matches its argv. The built-in guardrails (#154 §3) apply before
// any of this and cannot be loosened here.
type HostCommand struct {
	// Disabled is the `false` form: the binary is neither a host command nor
	// runnable natively in the jail.
	Disabled bool `yaml:"-"`
	// Allow, if present, is the only set of actions that may run.
	Allow []string `yaml:"allow,omitempty"`
	// Deny refuses matching actions.
	Deny []string `yaml:"deny,omitempty"`
	// Env are optional fix-ups for the host-side process (e.g. KUBECONFIG
	// for a context that suits agents). Never where credentials go, and
	// runtime/global level only — a step cannot set them.
	Env map[string]string `yaml:"env,omitempty"`
	// Network runs this binary's host-side process under the same enforced
	// egress proxy as the jail. Absent = the host's own network.
	Network *IsolationNetwork `yaml:"network,omitempty"`
	// Persist names extra paths under $HOME whose writes survive the
	// copy-on-write home (beyond the built-in profile's own, e.g.
	// ~/.aws/sso/cache). Runtime/global level only.
	Persist []string `yaml:"persist,omitempty"`
}

// UnmarshalYAML accepts `false`/`true` as well as the mapping form.
func (h *HostCommand) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!bool" {
		switch v.Value {
		case "false", "False", "FALSE":
			*h = HostCommand{Disabled: true}
		default:
			*h = HostCommand{}
		}
		return nil
	}
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		*h = HostCommand{}
		return nil
	}
	type plain HostCommand
	var p plain
	if err := strictNodeDecode(v, &p); err != nil {
		return err
	}
	*h = HostCommand(p)
	return nil
}

// MarshalYAML renders the disabled form back as `false`.
func (h HostCommand) MarshalYAML() (any, error) {
	if h.Disabled {
		return false, nil
	}
	type plain HostCommand
	return plain(h), nil
}

// WritesPolicy is what an agent may write outside plain reads (#154 §5).
// Writes are bound to the dispatch's own target by default: a fixer may push
// its target's head branch, comment on it, and reply to and resolve its
// review threads; a reviewer may write nothing. Everything else — opening a
// PR or issue, writing to another PR, pushing another branch, merging or
// closing — is refused unless a field here opens it.
//
// YAML: `writes: read_only`, `writes: target` (the fixer default), or a
// mapping of the fields below. A pack step's widening is capped by what the
// operator's own runtime/global block allows.
type WritesPolicy struct {
	ReadOnly     bool     `yaml:"read_only,omitempty"`
	CreatePR     bool     `yaml:"create_pr,omitempty"`
	CreateIssue  bool     `yaml:"create_issue,omitempty"`
	OtherTargets bool     `yaml:"other_targets,omitempty"`
	Branches     []string `yaml:"branches,omitempty"`
	Merge        bool     `yaml:"merge,omitempty"`
	// Target is the explicit `writes: target` form — the default fixer
	// policy, stated so a step that would default read-only can opt in.
	Target bool `yaml:"-"`
}

// UnmarshalYAML accepts the scalar forms as well as the mapping.
func (w *WritesPolicy) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		switch v.Value {
		case "read_only", "readonly", "none":
			*w = WritesPolicy{ReadOnly: true}
			return nil
		case "target":
			*w = WritesPolicy{Target: true}
			return nil
		}
		return fmt.Errorf("line %d: isolation writes must be read_only | target | {create_pr, create_issue, other_targets, branches, merge}, got %q", v.Line, v.Value)
	}
	type plain WritesPolicy
	var p plain
	if err := strictNodeDecode(v, &p); err != nil {
		return err
	}
	*w = WritesPolicy(p)
	return nil
}

// Widens reports whether the policy opens anything beyond the default
// target-bound fixer policy.
func (w *WritesPolicy) Widens() bool {
	return w != nil && (w.CreatePR || w.CreateIssue || w.OtherTargets || len(w.Branches) > 0 || w.Merge)
}

// IntentRules are optional tool-call rules (#154 §11), evaluated when the
// agent's harness reports a tool call before running it (claude-code
// PreToolUse). They explain a refusal to the model early; they are not the
// security boundary — the jail and host-command policy are.
type IntentRules struct {
	// AllowPaths, if set, confines file edits (Edit/Write/…) to these globs,
	// relative to the workspace (`src/**`).
	AllowPaths []string `yaml:"allow_paths,omitempty"`
	// DenyPaths refuses file edits matching these globs (`migrations/**`).
	DenyPaths []string `yaml:"deny_paths,omitempty"`
	// MaxDeleteLines refuses deleting (or truncating to nothing) a file
	// longer than this many lines. 0 = no limit.
	MaxDeleteLines int `yaml:"max_delete_lines,omitempty"`
	// DenyTools refuses these tool names outright (e.g. WebFetch).
	DenyTools []string `yaml:"deny_tools,omitempty"`
}

// hostCommandNameOK is the shape a host-command key must have: a bare binary
// name, never a path.
func hostCommandNameOK(name string) bool {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "/\\ \t\n") || name == "." || name == ".." {
		return false
	}
	return true
}

// validateAgentJail checks the agent-jail-only parts of an isolation block
// (host rules, writes, intent). step reports a step-level block, where
// operator-only knobs (env fix-ups, persist paths) are refused.
func validateAgentJail(where string, iso *IsolationConfig, step bool) error {
	if iso == nil {
		return nil
	}
	names := make([]string, 0, len(iso.Host))
	for n := range iso.Host {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		h := iso.Host[name]
		if !hostCommandNameOK(name) {
			return fmt.Errorf("config: %s: isolation.host: %q is not a binary name", where, name)
		}
		if h == nil || h.Disabled {
			continue
		}
		if step && len(h.Env) > 0 {
			return fmt.Errorf("config: %s: isolation.host.%s.env: env fix-ups belong on the runtime (or top-level) isolation block — a step can only narrow host commands", where, name)
		}
		if step && len(h.Persist) > 0 {
			return fmt.Errorf("config: %s: isolation.host.%s.persist: persist paths belong on the runtime (or top-level) isolation block — a step can only narrow host commands", where, name)
		}
		for _, r := range append(append([]string(nil), h.Allow...), h.Deny...) {
			if strings.TrimSpace(r) == "" {
				return fmt.Errorf("config: %s: isolation.host.%s: empty rule", where, name)
			}
		}
		for k := range h.Env {
			if k == "" || strings.ContainsAny(k, "= \t") {
				return fmt.Errorf("config: %s: isolation.host.%s.env: bad variable name %q", where, name, k)
			}
		}
		for _, p := range h.Persist {
			if !strings.HasPrefix(p, "~/") {
				return fmt.Errorf("config: %s: isolation.host.%s.persist: %q must be a path under ~/", where, name, p)
			}
			if strings.Contains(p, "..") {
				return fmt.Errorf("config: %s: isolation.host.%s.persist: %q must not contain ..", where, name, p)
			}
		}
		if n := h.Network; n != nil {
			if err := validateNetworkShape(where+": isolation.host."+name, n); err != nil {
				return err
			}
		}
	}
	if in := iso.Intent; in != nil && in.MaxDeleteLines < 0 {
		return fmt.Errorf("config: %s: isolation.intent.max_delete_lines must be >= 0", where)
	}
	if w := iso.Writes; w != nil && w.ReadOnly && w.Widens() {
		return fmt.Errorf("config: %s: isolation.writes: read_only cannot be combined with widening fields", where)
	}
	return nil
}

// validateNetworkShape checks one network block on its own.
func validateNetworkShape(where string, n *IsolationNetwork) error {
	if n.Mode != "" && (n.Deny || len(n.Egress) > 0) {
		return fmt.Errorf("config: %s: network: a scalar mode cannot carry egress/deny fields", where)
	}
	for _, pat := range n.Egress {
		if pat == "" {
			return fmt.Errorf("config: %s: network.egress: empty pattern", where)
		}
	}
	return nil
}

// ExpandHome resolves a leading "~/" against the daemon user's home. Other
// paths pass through unchanged.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			return filepath.Join(h, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// AgentJailEligible reports whether a runtime's launches get the default
// workspace jail: runtimes whose agent process conductor owns end to end —
// cli and acp, launched on this box. paseo and agent-deck (another daemon
// owns the agent process), opencode (reached over an HTTP control channel),
// and `host:` runtimes (the remote box's isolation applies) are out of scope.
func AgentJailEligible(cc ControllerConfig) bool {
	// An external runtime plugin (ScrubEnv) is its own binary under the
	// daemon's state dir with its own lifecycle; it keeps the scrubbed-env
	// launch it has today unless an explicit isolation: block says otherwise.
	if cc.Host != "" || cc.ScrubEnv {
		return false
	}
	switch cc.Type {
	case "cli", "acp", "":
		// "" is an acp agent runtime (use: acp / a plugin) unless its
		// transport says otherwise.
	default:
		return false
	}
	if cc.Type == "" && cc.Agent == "opencode" && cc.EffectiveTransport() == "native" {
		return false
	}
	return true
}

// GlobalIsolation returns the top-level `isolation:` block (nil when unset).
func (c *Config) GlobalIsolation() *IsolationConfig {
	if c == nil {
		return nil
	}
	return c.Isolation
}

// PolicyOnly reports a block that sets only write/tool-call/host-command
// POLICY (`writes:`, `intent:`, `host:`) and nothing about the sandbox
// itself. Such a block narrows what a jailed agent may do without replacing
// the sandbox the layers above chose, and without turning the synthesized
// jail into an explicit (fail-closed) one — a pack that only says its
// reviewers are read-only must not change how the jail degrades.
func (iso *IsolationConfig) PolicyOnly() bool {
	if iso == nil {
		return false
	}
	return iso.Mode == "" && iso.User == "" && iso.Container == nil && iso.Limits == nil &&
		!iso.Privileged && !iso.AllowRoot && iso.Network == nil && len(iso.FS) == 0 && !iso.MacOSKeychain &&
		(len(iso.Host) > 0 || iso.Writes != nil || iso.Intent != nil)
}

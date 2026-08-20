// Package config loads and validates paseo-conductor's YAML configuration.
//
// The top level is integration-agnostic: control/notify/agents/dispatch/store
// plus a list of raw integration entries. Each integration decodes its own
// sub-config (see internal/integrations/*). Action and AgentProfile are shared
// here because both the integration (which maps events→actions) and the
// dispatch package (which executes them) need them.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole config file.
type Config struct {
	Integrations []IntegrationRef        `yaml:"integrations"`
	Control      Control                 `yaml:"control"`
	Notify       Notify                  `yaml:"notify"`
	Agents       map[string]AgentProfile `yaml:"agents"`
	Dispatch     Dispatch                `yaml:"dispatch"`
	Store        Store                   `yaml:"store"`
	Update       Update                  `yaml:"update"`
	DryRun       bool                    `yaml:"dry_run"`
}

// Update configures periodic self-update checks.
type Update struct {
	Auto     bool     `yaml:"auto"`     // check for and install new releases periodically
	Interval Duration `yaml:"interval"` // how often to check (default 8h)
	Apply    *bool    `yaml:"apply"`    // re-exec into the new binary after updating (default true)
}

// ShouldApply reports whether to re-exec after a successful update (default true).
func (u Update) ShouldApply() bool { return u.Apply == nil || *u.Apply }

// IntegrationRef is one entry in the `integrations:` list. It captures the
// common header and retains the raw node so the concrete integration can decode
// its own fields.
type IntegrationRef struct {
	Type    string    `yaml:"type"`
	Name    string    `yaml:"name"`
	Enabled *bool     `yaml:"enabled"`
	raw     yaml.Node `yaml:"-"`
}

// UnmarshalYAML captures the header fields and the raw node.
func (r *IntegrationRef) UnmarshalYAML(n *yaml.Node) error {
	type hdr struct {
		Type    string `yaml:"type"`
		Name    string `yaml:"name"`
		Enabled *bool  `yaml:"enabled"`
	}
	var h hdr
	if err := n.Decode(&h); err != nil {
		return err
	}
	r.Type, r.Name, r.Enabled, r.raw = h.Type, h.Name, h.Enabled, *n
	return nil
}

// Decode unmarshals the raw integration node into a type-specific struct.
func (r IntegrationRef) Decode(v any) error { return r.raw.Decode(v) }

// IsEnabled reports whether the instance is enabled (default true).
func (r IntegrationRef) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Control is the kill switch + shadow settings.
type Control struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PauseLabel string `yaml:"pause_label"` // e.g. "conductor:off"
	Shadow     bool   `yaml:"shadow"`      // global shadow mode
}

// IsEnabled reports the master on/off (default true).
func (c Control) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Notify configures notifications.
type Notify struct {
	Push              bool     `yaml:"push"`
	CommentOnEscalate bool     `yaml:"comment_on_escalate"`
	On                []string `yaml:"on"` // subset of: dispatch, complete, escalate
}

// Wants reports whether the given notify event is enabled.
func (n Notify) Wants(event string) bool {
	for _, e := range n.On {
		if e == event {
			return true
		}
	}
	return false
}

// Dispatch configures backends and identity.
type Dispatch struct {
	DefaultBackends map[string]string        `yaml:"default_backends"` // agent->paseo, command->local
	Backends        map[string]BackendConfig `yaml:"backends"`
	Identity        Identity                 `yaml:"identity"`
}

// BackendConfig holds per-backend settings (e.g. paseo bin path).
type BackendConfig struct {
	Bin string `yaml:"bin"`
}

// Identity controls which credentials read vs write, and commit authorship.
type Identity struct {
	ReadToken    string `yaml:"read_token"`    // "app" | "gh_auth"
	WriteToken   string `yaml:"write_token"`   // "app" | "gh_auth"
	CommitAuthor string `yaml:"commit_author"` // "self"
}

// Store configures the dedup state + audit log.
type Store struct {
	StateFile     string   `yaml:"state_file"`
	AuditLog      string   `yaml:"audit_log"`
	StateTTL      Duration `yaml:"state_ttl"`
	MaxTrackedPRs int      `yaml:"max_tracked_prs"`
	AuditMaxSize  ByteSize `yaml:"audit_max_size"`
}

// AgentProfile is a reusable named agent config referenced by agent actions.
type AgentProfile struct {
	Provider        string            `yaml:"provider"`
	Model           string            `yaml:"model"`
	Thinking        string            `yaml:"thinking"`
	Mode            string            `yaml:"mode"`
	Workspace       string            `yaml:"workspace"` // local | worktree
	WaitTimeout     Duration          `yaml:"wait_timeout"`
	ArchiveWhenDone bool              `yaml:"archive_when_done"`
	Labels          map[string]string `yaml:"labels"`
}

// Action is one (source,kind)→action mapping. Type is "agent" or "command".
type Action struct {
	Type     string            `yaml:"type"`
	Enabled  *bool             `yaml:"enabled"` // default true
	Backend  string            `yaml:"backend"` // override default backend for the type
	Shadow   *bool             `yaml:"shadow"`  // per-action shadow override
	Checkout string            `yaml:"checkout"`
	WorkDir  string            `yaml:"workdir"` // working directory (command: cwd; agent: paseo --cwd)
	Env      map[string]string `yaml:"env"`

	// agent-type fields
	Agent  string `yaml:"agent"` // agent profile name
	Prompt string `yaml:"prompt"`

	// command-type fields
	Command []string `yaml:"command"`

	// workflow (multi-step) fields. When Steps is non-empty the action runs as
	// an ordered workflow; each step is itself an Action plus ID/If/OutputSchema.
	Steps        []Action       `yaml:"steps"`
	ID           string         `yaml:"id"`            // step id (for steps.<id>.outputs.*)
	If           string         `yaml:"if"`            // step condition (see internal/expr)
	OutputSchema map[string]any `yaml:"output_schema"` // agent step: JSON schema for structured output
	Background   bool           `yaml:"background"`    // workflow step: dispatch `paseo run --background` and don't
	//                                                    wait/capture — launch a live agent to drive interactively

	// gating actors (live on the check they gate)
	Reviewer Actors `yaml:"reviewer"` // review_requested: whose requested review triggers it
	Assignee Actors `yaml:"assignee"` // issue_assigned: whose assignment triggers it

	// kind-specific options
	MaxAttemptsPerHead int            `yaml:"max_attempts_per_head"`
	IgnoreChecks       []string       `yaml:"ignore_checks"`
	FlakyRerun         FlakyRerun     `yaml:"flaky_rerun"`
	FromUsers          []string       `yaml:"from_users"`
	LabelsAny          []string       `yaml:"labels_any"`
	RequireLabel       string         `yaml:"require_label"`
	Method             string         `yaml:"method"`
	Gates              map[string]any `yaml:"gates"`
	Project            map[string]any `yaml:"project"`
}

// IsEnabled reports whether the action is enabled (default true).
func (a Action) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

// FlakyRerun controls one-shot re-runs of failed checks before dispatching.
type FlakyRerun struct {
	Enabled bool `yaml:"enabled"`
	Max     int  `yaml:"max"`
}

// Actors is a set of GitHub logins/teams (used for reviewer/assignee matching).
type Actors struct {
	Logins []string `yaml:"logins"`
	Teams  []string `yaml:"teams"`
}

// HasLogin reports whether login is in the set (case-insensitive).
func (a Actors) HasLogin(login string) bool {
	for _, l := range a.Logins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// Load reads, expands, defaults, and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	raw = expandEnv(raw)
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR} (brace form only) with the environment value, so a
// bare "$" in prompts is left untouched.
func expandEnv(b []byte) []byte {
	return envRe.ReplaceAllFunc(b, func(m []byte) []byte {
		name := envRe.FindSubmatch(m)[1]
		return []byte(os.Getenv(string(name)))
	})
}

func (c *Config) applyDefaults() {
	if c.Dispatch.DefaultBackends == nil {
		c.Dispatch.DefaultBackends = map[string]string{"agent": "paseo", "command": "local"}
	}
	if c.Dispatch.Backends == nil {
		c.Dispatch.Backends = map[string]BackendConfig{"paseo": {Bin: "paseo"}, "local": {}}
	}
	if b, ok := c.Dispatch.Backends["paseo"]; ok && b.Bin == "" {
		b.Bin = "paseo"
		c.Dispatch.Backends["paseo"] = b
	}
	if c.Dispatch.Identity.ReadToken == "" {
		c.Dispatch.Identity.ReadToken = "app"
	}
	if c.Dispatch.Identity.WriteToken == "" {
		c.Dispatch.Identity.WriteToken = "gh_auth"
	}
	if c.Dispatch.Identity.CommitAuthor == "" {
		c.Dispatch.Identity.CommitAuthor = "self"
	}
	if c.Store.StateFile == "" {
		c.Store.StateFile = expandHome("~/.local/state/paseo-conductor/state.json")
	}
	if c.Store.AuditLog == "" {
		c.Store.AuditLog = expandHome("~/.local/state/paseo-conductor/audit.jsonl")
	}
	c.Store.StateFile = expandHome(c.Store.StateFile)
	c.Store.AuditLog = expandHome(c.Store.AuditLog)
	if c.Store.StateTTL == 0 {
		c.Store.StateTTL = Duration(30 * 24 * time.Hour)
	}
	if c.Store.MaxTrackedPRs == 0 {
		c.Store.MaxTrackedPRs = 5000
	}
	if c.Store.AuditMaxSize == 0 {
		c.Store.AuditMaxSize = 50 * 1024 * 1024
	}
	if len(c.Notify.On) == 0 {
		c.Notify.On = []string{"escalate"}
	}
	if c.Update.Auto && c.Update.Interval == 0 {
		c.Update.Interval = Duration(8 * time.Hour)
	}
}

// Validate checks required fields and cross-field consistency.
func (c *Config) Validate() error {
	if len(c.Integrations) == 0 {
		return fmt.Errorf("config: no integrations configured")
	}
	names := map[string]bool{}
	for i, ig := range c.Integrations {
		if ig.Type == "" {
			return fmt.Errorf("config: integrations[%d]: missing type", i)
		}
		if ig.Name == "" {
			return fmt.Errorf("config: integrations[%d]: missing name", i)
		}
		if names[ig.Name] {
			return fmt.Errorf("config: duplicate integration name %q", ig.Name)
		}
		names[ig.Name] = true
	}
	for name, p := range c.Agents {
		if p.Workspace != "" && p.Workspace != "local" && p.Workspace != "worktree" {
			return fmt.Errorf("config: agent %q: workspace must be local|worktree, got %q", name, p.Workspace)
		}
	}
	return nil
}

func expandHome(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

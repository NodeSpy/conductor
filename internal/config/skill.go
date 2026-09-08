package config

import (
	"fmt"
	"strings"
)

// SkillConfig is the `skill:` block: the remote HTTP face of the agent skill
// surface (#36 §12). It exists so an agent dispatched to a REMOTE runtime
// (host:) — running on another machine — can still reach conductor's verbs,
// memory, and secret broker, which otherwise live only behind the daemon's
// local unix socket.
//
// It is a control surface an off-box process authenticates into, so it is
// strong-by-default: off unless `listen` is set, and every request carries a
// session token the dispatch path minted (deny-by-default; an unknown token is
// refused). TLS is expected to be terminated upstream by the same reverse proxy
// / tunnel that already exposes conductor's other inbound surfaces — the daemon
// serves plaintext HTTP on `listen`, never facing the public internet directly.
type SkillConfig struct {
	// Listen is the bind address for the remote skill endpoint (e.g.
	// "127.0.0.1:8098"). It mounts "/skill" on the shared inbound listener, so
	// it may reuse a `listen:` a webhook / sentry / callable surface already
	// binds. Empty disables the remote face (local agents are unaffected).
	Listen string `yaml:"listen"`
	// BaseURL is the public origin a remote agent reaches the endpoint at (e.g.
	// "https://conductor.example.com"). The dispatch path hands a remote agent
	// CONDUCTOR_ENDPOINT = BaseURL + "/skill". Required when Listen is set —
	// without it the daemon would serve the endpoint but no agent would be told
	// where to find it.
	BaseURL string `yaml:"base_url"`
}

// RemoteEnabled reports whether the remote HTTP face should be mounted.
func (c *Config) RemoteSkillEnabled() bool {
	return c.Skill != nil && c.Skill.Listen != ""
}

// SkillRemoteListen is the bind address for the remote skill endpoint ("" off).
func (c *Config) SkillRemoteListen() string {
	if c.Skill == nil {
		return ""
	}
	return c.Skill.Listen
}

// SkillRemoteEndpoint is the full URL a remote agent posts ops to, or "" when
// no public base URL is configured (so the dispatch path emits nothing remote).
func (c *Config) SkillRemoteEndpoint() string {
	if c.Skill == nil || c.Skill.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(c.Skill.BaseURL, "/") + "/skill"
}

// validateSkill checks the `skill:` block. A remote face with no way to be
// reached (listen but no base_url), or a base_url that isn't an http(s) origin,
// is a config error rather than a silently useless mount.
func (c *Config) validateSkill() error {
	if c.Skill == nil || (c.Skill.Listen == "" && c.Skill.BaseURL == "") {
		return nil
	}
	if c.Skill.Listen == "" {
		return fmt.Errorf("skill: base_url is set but listen is empty — the remote endpoint is never served")
	}
	if c.Skill.BaseURL == "" {
		return fmt.Errorf("skill: listen is set but base_url is empty — remote agents can't be told where to reach %s", c.Skill.Listen)
	}
	if !strings.HasPrefix(c.Skill.BaseURL, "https://") && !strings.HasPrefix(c.Skill.BaseURL, "http://") {
		return fmt.Errorf("skill: base_url %q must be an http:// or https:// origin", c.Skill.BaseURL)
	}
	if !c.SkillEnabled() {
		return fmt.Errorf("skill: a remote endpoint is configured but no agent has a skill: block — nothing would be served")
	}
	return nil
}

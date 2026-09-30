package controller

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/preflight"
)

func init() { preflight.Register(jailPreflight) }

// jailPreflight reports the agent workspace jail at `conductor validate` and
// boot (#154 §1, §7): whether this box can build it, which runtimes it
// governs and which it cannot (another daemon owns their agents, or they run
// on another box), and the host commands a jailed agent gets.
func jailPreflight(cfg *config.Config, env preflight.Env) []preflight.Finding {
	var out []preflight.Finding
	ccs := cfg.MergedControllers()
	names := make([]string, 0, len(ccs))
	for n := range ccs {
		names = append(names, n)
	}
	sort.Strings(names)
	var jailed []string
	explicit := false
	for _, n := range names {
		cc := ccs[n]
		iso := cc.Isolation
		if iso == nil {
			iso = cfg.Isolation
		}
		switch {
		case !config.AgentJailEligible(cc):
			why := "its agents are another daemon's children"
			if cc.Host != "" {
				why = "it runs on host " + cc.Host + " (that box's own isolation applies)"
			} else if cc.ScrubEnv {
				why = "it is an external runtime plugin (explicit isolation: only)"
			} else if cc.Type == "opencode" || cc.Agent == "opencode" {
				why = "opencode is reached over an HTTP control channel"
			}
			if cc.Type != "paseo" || cc.Isolation != nil {
				out = append(out, preflight.Finding{Level: "info", What: "runtime " + n + ": the agent workspace jail does not apply", Why: why})
			}
		case iso != nil && iso.Mode == "none":
			out = append(out, preflight.Finding{Level: "warn", What: "runtime " + n + ": agents run unconfined (isolation mode: none)", Why: "the explicit opt-out of the workspace jail"})
		case iso != nil && !jailMode(iso):
			// user/container/privileged: the older wrappers, as configured.
		default:
			jailed = append(jailed, n)
			if iso != nil {
				explicit = true
			}
		}
	}
	if len(jailed) == 0 {
		return out
	}
	if why := jailProbe(); why != "" {
		lvl := "warn"
		what := "the agent workspace jail is unavailable here — runtimes " + strings.Join(jailed, ", ") + " run their agents UNCONFINED"
		if explicit {
			lvl = "error"
			what = "an explicit isolation: block requires the workspace jail, which this box cannot build"
		}
		out = append(out, preflight.Finding{Level: lvl, What: what, Why: why})
		return out
	}
	en, dis := hostcmd.HostSet([]*config.IsolationConfig{cfg.Isolation}, false, env.LookPath)
	msg := "none"
	if len(en) > 0 {
		msg = strings.Join(en, ", ")
	}
	if len(dis) > 0 {
		msg += fmt.Sprintf(" (disabled: %s)", strings.Join(dis, ", "))
	}
	out = append(out, preflight.Finding{Level: "info", What: "agent workspace jail: on for runtimes " + strings.Join(jailed, ", "), Why: "host commands: " + msg})
	return out
}

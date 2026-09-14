package flow

import (
	"fmt"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// IsolationWarnings lints `isolation:` blocks for the postures that WORK but
// are weaker than they look (#36 iso-review C2/C4) — `conductor validate`
// prints them so the operator opts into the weakness knowingly instead of
// discovering it in an incident:
//
//   - mode: user + an egress allowlist is ADVISORY-ONLY. There is no network
//     namespace, so only the HTTP(S)_PROXY env steers traffic — a runtime
//     that ignores proxy env reaches the network directly. The enforced
//     allowlist is deny+egress under namespace/container.
//   - mode: user with a SHARED account does not isolate concurrent
//     dispatches from EACH OTHER: same EUID means a sibling can read
//     /proc/<pid>/environ (tokens included) and signal/ptrace its peers. It
//     isolates agents from the daemon, not agents from agents.
func IsolationWarnings(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var warns []string
	seen := map[string]bool{}
	warn := func(w string) {
		if !seen[w] {
			seen[w] = true
			warns = append(warns, w)
		}
	}

	// usersAt collects which scopes run under each mode:user account, so a
	// shared account across scopes gets named explicitly.
	usersAt := map[string][]string{}
	check := func(where string, iso *config.IsolationConfig) {
		if iso == nil || iso.Mode != "user" {
			return
		}
		if iso.User != "" {
			usersAt[iso.User] = append(usersAt[iso.User], where)
		}
		if iso.Network != nil && !iso.Network.Deny {
			warn(fmt.Sprintf("%s: isolation mode user + egress is ADVISORY-ONLY — only HTTP(S)_PROXY env steers traffic; a runtime that ignores it reaches the network directly. For an enforced allowlist use `network: {deny: true, egress: [...]}` under mode namespace or container.", where))
		}
		warn(fmt.Sprintf("%s: isolation mode user isolates the agent from the DAEMON, not concurrent agents from each other — dispatches sharing user %q have the same EUID and can read each other's /proc/<pid>/environ (tokens included). Use a distinct `user:` per concurrent scope, or mode namespace/container, for agent-vs-agent isolation.", where, iso.User))
	}

	cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Isolation != nil {
			check(config.StepLabel(scope, slot, *s), s.Isolation)
		}
	})
	rts := make([]string, 0, len(cfg.Runtimes))
	for n := range cfg.Runtimes {
		rts = append(rts, n)
	}
	sort.Strings(rts)
	for _, n := range rts {
		check("runtime "+n, cfg.Runtimes[n].Isolation)
	}
	hs := make([]string, 0, len(cfg.Hosts))
	for n := range cfg.Hosts {
		hs = append(hs, n)
	}
	sort.Strings(hs)
	for _, n := range hs {
		check("host "+n, cfg.Hosts[n].Isolation)
	}

	users := make([]string, 0, len(usersAt))
	for u := range usersAt {
		users = append(users, u)
	}
	sort.Strings(users)
	for _, u := range users {
		if scopes := usersAt[u]; len(scopes) > 1 {
			warn(fmt.Sprintf("sandbox user %q is SHARED by %d isolation scopes (%v) — their dispatches are not isolated from each other (same EUID). Give each scope its own account, or use mode namespace/container.", u, len(scopes), scopes))
		}
	}
	return warns
}

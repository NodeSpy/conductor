package dispatch

import (
	"os"
	"strconv"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// The conductor tool server (memory + run_step + the skill's verb tools and
// secret broker) is injected per dispatch into the MCP-delivery runtimes
// (ACP/opencode) as an mcpServers entry on session/new. This is the SHARED
// wiring — what to launch, with which provenance flags, and the one-shot skill
// claim (#122 R1) — so both MCP paths assemble the same server instead of
// copy-pasting it. Shell runtimes (paseo/agent-deck) don't use this; they get
// the CLI face via SkillEnv instead. It lives here in dispatch, not controller,
// because the paseo dispatcher can't import controller (controller imports
// dispatch).

// ToolServerSpec is one dispatch's conductor tool server: the subprocess to
// launch, its argv, and the environment to set on it.
type ToolServerSpec struct {
	Command string
	Args    []string
	// Env carries the ONE-SHOT skill claim code (CONDUCTOR_SKILL_CLAIM) for a
	// skill-enabled profile — environment, never argv, which any same-user
	// process can read from a process listing. Empty for profiles without skill:.
	Env map[string]string
}

// BuildToolServer assembles the tool server for one dispatch, or nil when there
// is nothing to inject: no tool command published at boot (no memory: section
// and no skill-enabled profile), or a remote (host:) session — the daemon's
// socket doesn't exist on that box.
//
// When the dispatched profile carries a skill: block (#36 §12), this is where
// the daemon binds identity SERVER-SIDE: it mints a one-shot claim code mapped
// to the real profile and its policy in the skill broker. The subprocess
// exchanges the code (single-use, short TTL) for the session token over the
// socket at startup, and the broker binds the session to that process's kernel
// peer credentials. The --agent/--repo flags remain provenance labels for
// memory writes; the broker never trusts them.
//
// host is the runtime's own host (a hosts: entry); the profile's host wins when
// set. A non-empty effective host means a remote launch → nil (no local socket).
func BuildToolServer(req Request, host string) *ToolServerSpec {
	argv := memory.ToolCommand()
	effHost := host
	if req.Step.Host != "" {
		effHost = req.Step.Host
	}
	if len(argv) == 0 || effHost != "" {
		return nil
	}
	args := append([]string(nil), argv[1:]...)
	if a := req.Action.Agent; a != "" {
		args = append(args, "--agent", a)
	}
	if repo := req.Trigger.Target.Repo; repo != "" {
		args = append(args, "--repo", repo)
	}
	if kind := req.Trigger.Kind; kind != "" {
		args = append(args, "--trigger", kind)
	}
	// The target's PROVENANCE travels with the repo it describes. Without it
	// the memory MCP face would treat every dispatch's repo as its own — the
	// bug — or, if it assumed the safe default, would refuse legitimate
	// trusted dispatches. Neither is a default worth having: the daemon knows
	// the answer, so it says it.
	if req.Trigger.TargetTrusted {
		args = append(args, "--target-trusted")
	}
	// The daemon's id for this dispatch. A live tool (run_step) rebuilds a
	// trigger from this provenance, and when the target is untrusted this is
	// the only thing left that the event's sender did not choose — so it is
	// what the rebuilt trigger's agent-authored steps are confined to.
	if req.DispatchID != "" {
		args = append(args, "--dispatch", req.DispatchID)
	}
	// The secret tools are advertised only when the profile asked for the
	// broker. The credential below is minted for every dispatch; what it
	// GRANTS is the difference.
	if sk := req.Step.Skill; sk != nil && sk.SecretsVia == "broker" {
		args = append(args, "--secrets")
	}
	if n := req.Trigger.Target.Number; n > 0 {
		args = append(args, "--number", strconv.Itoa(n))
	}
	out := &ToolServerSpec{Command: argv[0], Args: args}
	// EVERY tool subprocess gets a per-dispatch credential, not only a step
	// with a skill: block (round-12 #1). The socket resolves a request's
	// provenance from it rather than believing a Source off the wire, so a
	// memory-only dispatch needs one exactly as much as a skill one — the
	// difference is what the identity GRANTS, not whether it exists.
	//
	// !req.AgentAuthored: same refusal as SkillEnv. A grant is minted only
	// for a step the operator authored; an agent-authored step gets no
	// credential and therefore no tool socket identity at all.
	if b := skill.Active(); b != nil && !req.AgentAuthored {
		policy := config.SkillPolicy{} // no skill: block → authentication with an EMPTY grant
		if req.Step.Skill != nil {
			policy = *req.Step.Skill
		}
		claim, err := b.MintClaim(skill.Identity{
			Agent:         req.Action.Agent,
			Repo:          req.Trigger.Target.Repo,
			Trigger:       req.Trigger.Kind,
			Number:        req.Trigger.Target.Number,
			Policy:        policy,
			Context:       req.Trigger.Context,
			TargetTrusted: req.Trigger.TargetTrusted,
			// The daemon's anchor for this dispatch, on the SKILL path too.
			// The argv path passes --dispatch; without the same value here a
			// skill-enabled dispatch with an untrusted target loses the
			// cross-dispatch confinement run_step relies on and falls back to
			// the shared literal namespace (round-12 #4).
			Dispatch: req.DispatchID,
		})
		if err == nil {
			out.Env = map[string]string{"CONDUCTOR_SKILL_CLAIM": claim}
		}
	}
	return out
}

// SkillEnv returns the environment a dispatched agent needs to reach the
// conductor skill surface via the `conductor` CLI: the socket endpoint and a
// session token (broker.MintSession). Delivered through the runtime's env
// (paseo `--env`, etc.), never argv. `endpoint` is a resolved `unix://<socket>`
// — the daemon's own socket for a local launch, or an SSH-reverse-forwarded
// copy of it on the remote box (the caller resolves which). Returns nil when
// there is nothing to offer: no skill: on the profile, or no endpoint. This is
// the paseo/cli counterpart to BuildToolServer's MCP injection (ACP/opencode).
func SkillEnv(req Request, endpoint string) map[string]string {
	if endpoint == "" {
		return nil
	}
	// Defense in depth at the READ point. flow strips skill: from every
	// agent-authored dispatch before building the request, and both plan
	// guards reject it at admission — but this is where a grant actually
	// becomes a token, so it refuses independently. An agent-authored step
	// carrying a skill: block got it from somewhere it shouldn't have; mint
	// nothing rather than trust that the upstream strip ran.
	if req.AgentAuthored {
		return nil
	}
	b := skill.Active()
	if b == nil {
		return nil
	}
	// The uid binding matters on the local socket (kernel peer creds). Over an
	// SSH-forwarded socket the peer is the ssh relay running as the daemon's own
	// uid, so the uid check passes and provenance rests on the token — which is
	// exactly why memory/run_step ops derive Source from the token, not the peer.
	envPolicy := config.SkillPolicy{} // as in BuildToolServer: credential first, grant maybe
	if req.Step.Skill != nil {
		envPolicy = *req.Step.Skill
	}
	tok, err := b.MintSession(skill.Identity{
		Agent:         req.Action.Agent,
		Repo:          req.Trigger.Target.Repo,
		Trigger:       req.Trigger.Kind,
		Number:        req.Trigger.Target.Number,
		Policy:        envPolicy,
		Context:       req.Trigger.Context,
		TargetTrusted: req.Trigger.TargetTrusted,
		Dispatch:      req.DispatchID, // as above: both paths carry the anchor
	}, uint32(os.Getuid()))
	if err != nil {
		return nil
	}
	return map[string]string{
		"CONDUCTOR_ENDPOINT":    endpoint,
		"CONDUCTOR_SKILL_TOKEN": tok,
	}
}

// LocalSkillEndpoint is the `unix://` endpoint for a LOCAL dispatched agent —
// the daemon's own tool socket, or "" when none was published at boot (no
// memory: section and no skill: profile).
func LocalSkillEndpoint() string {
	sock := socketFromToolCommand(memory.ToolCommand())
	if sock == "" {
		return ""
	}
	return "unix://" + sock
}

// socketFromToolCommand pulls the --socket value out of memory.ToolCommand()
// (the daemon publishes `conductor mcp memory --socket <path> …` at boot).
func socketFromToolCommand(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--socket" {
			return argv[i+1]
		}
	}
	return ""
}

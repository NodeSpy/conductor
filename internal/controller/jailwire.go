package controller

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/jail"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The default workspace jail for agent runtimes (#154 §1). A cli or acp
// launch on this box with no isolation: block gets a synthesized
// pivot_root (Linux) / Seatbelt (macOS) jail: the agent sees its workspace,
// the git common dir behind it, its own tool state, and nothing else of the
// operator's home; credentials reach nothing in it, and credentialed work
// goes through the broker (host commands, brokered git, signing).
//
//   - The synthesized default degrades LOUDLY where the OS cannot build it
//     (a warning, an audit row, a watch event) and runs as before.
//   - An explicit isolation: block (namespace mode, or no mode) fails closed.
//   - `mode: none` opts out; `mode: namespace, privileged: true` keeps the
//     full-view behavior; user/container modes are unchanged.

// JailManager is the daemon's jail registry, wired once by cmd/conductor.
// nil → no agent jail at all (tests and builds that do not opt in): every
// launch behaves exactly as before.
var JailManager *jail.Manager

// GlobalIsolation is the config's top-level isolation: block (the fleet-wide
// base under a runtime's and a step's). Wired by cmd/conductor.
var GlobalIsolation *config.IsolationConfig

// JailDegraded reports a synthesized jail that could not be built. Wired to
// the audit trail + watch; nil → the log line alone.
var JailDegraded func(dispatchID, label, reason string)

// EgressProxyUnixLabeled is EgressProxyUnix with the credential labeled by
// dispatch, so `network: audit` and denials are attributed in watch.
var EgressProxyUnixLabeled func(allow []string, label string) (sock, cred string, revoke func(), err error)

// EgressProxyForLabeled is the loopback (TCP) labeled endpoint — the macOS
// jail's route out (Seatbelt allows outbound only to its port).
var EgressProxyForLabeled func(allow []string, label string) (addr, cred string, revoke func(), err error)

// jailProbe reports whether this box can build the jail ("" = yes, else why
// not). A var for tests.
var jailProbe = probeJail

var probeOnce sync.Once
var probeResult string

func probeJail() string {
	probeOnce.Do(func() { probeResult = probeJailNow() })
	return probeResult
}

func probeJailNow() string {
	switch runtime.GOOS {
	case "linux":
		if os.Geteuid() == 0 {
			return "conductor runs as root, where a user namespace is no privilege boundary"
		}
		if _, err := exec.LookPath("unshare"); err != nil {
			return "unshare (util-linux) is not installed"
		}
		if out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "--pid", "--fork", "true").CombinedOutput(); err != nil {
			return fmt.Sprintf("unprivileged user namespaces are unavailable (%v: %s)", err, strings.TrimSpace(string(out)))
		}
		return ""
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return "sandbox-exec is not available"
		}
		return ""
	default:
		return "no jail backend on " + runtime.GOOS
	}
}

// ProbeJail exposes the platform probe (preflight, validate).
func ProbeJail() string { return jailProbe() }

// jailState is shared across one session's turns: the git layout resolved
// on the first (while the worktree was still conductor's own) and the live
// jail of the current turn.
type jailState struct {
	mu  sync.Mutex
	git *jail.GitLayout
}

// modelEndpoints are the hosts each tool's own model traffic needs — always
// allowed, whatever the jail's network mode (#154 §9: `deny` means "nothing
// else", not "the agent cannot think").
var modelEndpoints = map[string][]string{
	"claude-code": {"api.anthropic.com", "statsig.anthropic.com", "console.anthropic.com", "claude.ai", "mcp-proxy.anthropic.com", "platform.claude.com"},
	"codex":       {"api.openai.com", "chatgpt.com", "auth.openai.com", "ab.chatgpt.com"},
	"gemini":      {"generativelanguage.googleapis.com", "oauth2.googleapis.com", "cloudcode-pa.googleapis.com"},
}

// agentLaunchOpts resolves one agent dispatch's isolation, jail included,
// for a runtime that owns its agent process (cli/acp). eligible reports the
// runtime is jail-eligible (config.AgentJailEligible); tool is the recipe's.
func agentLaunchOpts(eligible bool, tool string, runtimeIso *config.IsolationConfig, req dispatch.Request) launchOpts {
	opt := launchOptsFor(runtimeIso, req)
	if !eligible || JailManager == nil {
		return opt
	}
	iso := opt.iso
	if iso == nil {
		iso = GlobalIsolation
	}
	defaulted := false
	if iso == nil {
		iso = &config.IsolationConfig{Defaulted: true}
		defaulted = true
	}
	if !jailMode(iso) {
		if iso.Mode == "none" {
			opt.iso = nil
		} else {
			opt.iso = iso
		}
		return opt
	}
	opt.iso = iso
	opt.jailDefault = defaulted
	spec := jailSpec(tool, runtimeIso, req)
	opt.jail = &spec
	opt.jailState = &jailState{}
	return opt
}

// agentResumeOpts is agentLaunchOpts for a profile-less relaunch (resume):
// the runtime's own isolation, jailed the same way, with no dispatch target
// (the broker then refuses every write).
func agentResumeOpts(eligible bool, tool string, runtimeIso *config.IsolationConfig, agentAuthored bool) launchOpts {
	return agentLaunchOpts(eligible, tool, runtimeIso, dispatch.Request{AgentAuthored: agentAuthored})
}

// jailMode reports an isolation block that means the workspace jail:
// namespace (or no mode) without the privileged opt-out.
func jailMode(iso *config.IsolationConfig) bool {
	return iso != nil && (iso.Mode == "" || iso.Mode == "namespace") && !iso.Privileged
}

// jailSpec fills the per-dispatch jail description from the request.
func jailSpec(tool string, runtimeIso *config.IsolationConfig, req dispatch.Request) jail.LaunchSpec {
	t := req.Trigger.Target
	repo := t.CheckoutRepo()
	num, isPR := t.PR, t.PR > 0
	if !isPR {
		num = t.Issue
		if num == 0 {
			num = t.Number
		}
	}
	head := ""
	if isPR {
		head, _ = req.Trigger.Context["head_ref"].(string)
	}
	step := req.Step
	layers := []*config.IsolationConfig{GlobalIsolation, runtimeIso, step.Isolation}
	readOnly, writes := effectiveWrites(req, GlobalIsolation, runtimeIso, step.Isolation)
	var intent *config.IntentRules
	for _, l := range layers {
		if l != nil && l.Intent != nil {
			intent = l.Intent
		}
	}
	role := "fix"
	if readOnly {
		role = "review"
	}
	if step.DecisionLaunch != nil {
		role = "decide"
	}
	label := role
	if repo != "" {
		label = fmt.Sprintf("%s %s#%d", role, repo, num)
	}
	if step.ID != "" {
		label += " " + step.ID
	}
	return jail.LaunchSpec{
		DispatchID: req.DispatchID, Tool: tool, Repo: repo, Number: num, IsPR: isPR,
		HeadBranch: head, BaseRef: t.BaseRef, Step: firstNonEmpty(step.ID, step.Name), Label: label,
		ReadOnly: readOnly, Writes: writes, Layers: layers, StepLayer: step.Isolation != nil,
		Intent: intent, UserToken: req.Tokens.User,
	}
}

// reviewKinds are trigger kinds whose agents review rather than fix.
var reviewKinds = map[string]bool{"review_requested": true, "self_review": true}

// effectiveWrites decides a dispatch's write policy (#154 §5): review steps
// write nothing by default — a decide step, a step with an output schema, a
// checkout-less step, or a review trigger — unless `expect_push:` or an
// explicit `writes:` says otherwise. A pack step's widening is capped by the
// operator's own runtime/global writes block.
func effectiveWrites(req dispatch.Request, global, runtimeIso, stepIso *config.IsolationConfig) (bool, *config.WritesPolicy) {
	var op *config.WritesPolicy
	for _, l := range []*config.IsolationConfig{global, runtimeIso} {
		if l != nil && l.Writes != nil {
			op = l.Writes
		}
	}
	var st *config.WritesPolicy
	if stepIso != nil {
		st = stepIso.Writes
	}
	eff := op
	if st != nil {
		eff = st
		if req.Step.FromPack && st.Widens() {
			capped := *st
			capped.CreatePR = st.CreatePR && op != nil && op.CreatePR
			capped.CreateIssue = st.CreateIssue && op != nil && op.CreateIssue
			capped.OtherTargets = st.OtherTargets && op != nil && op.OtherTargets
			capped.Merge = st.Merge && op != nil && op.Merge
			if op == nil {
				capped.Branches = nil
			} else {
				var keep []string
				for _, b := range st.Branches {
					for _, o := range op.Branches {
						if b == o {
							keep = append(keep, b)
						}
					}
				}
				capped.Branches = keep
			}
			eff = &capped
		}
	}
	if eff != nil && eff.ReadOnly {
		return true, eff
	}
	if eff != nil && (eff.Target || eff.Widens()) {
		return false, eff
	}
	s := req.Step
	review := s.DecisionLaunch != nil || len(s.OutputSchema) > 0 || s.Checkout == "none" || reviewKinds[req.Trigger.Kind]
	return review && !s.ExpectPush, eff
}

// credentialEnv reports an environment variable that is (or carries) a
// credential and so must not reach a jailed agent (#154 §1). The agent's own
// model credential is the exception (keep).
func credentialEnv(k string, keep map[string]bool) bool {
	if keep[k] {
		return false
	}
	switch k {
	case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"PC_GH_WRITE_TOKEN", "PC_GH_APP_TOKEN",
		"SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO", "KUBECONFIG", "GOOGLE_APPLICATION_CREDENTIALS",
		"NODE_AUTH_TOKEN", "NPM_TOKEN", "DOCKER_AUTH_CONFIG", "DOCKER_HOST", "DOCKER_CONFIG",
		"GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND", "CONDUCTOR_SKILL_TOKEN", "CONDUCTOR_SKILL_CLAIM",
		"CONDUCTOR_VAULT_KEY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS":
		return true
	}
	for _, pre := range []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_", "VAULT_", "OP_", "TF_TOKEN_", "PC_", "HCLOUD_", "DIGITALOCEAN_", "CF_", "HEROKU_"} {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	u := strings.ToUpper(k)
	for _, frag := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE_KEY", "API_KEY", "APIKEY", "ACCESS_KEY", "SESSION_KEY", "AUTH"} {
		if strings.Contains(u, frag) {
			return true
		}
	}
	return false
}

// modelCredentials are the variables each tool authenticates its own model
// traffic with — kept in the jail (the agent must be able to think).
var modelCredentials = map[string][]string{
	"claude-code": {"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"},
	"codex":       {"OPENAI_API_KEY", "CODEX_API_KEY"},
	"gemini":      {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
}

// scrubJailEnv drops credential variables from an environment.
func scrubJailEnv(env []string, tool string) []string {
	keep := map[string]bool{}
	for _, k := range modelCredentials[tool] {
		keep[k] = true
	}
	// claude-code on Bedrock/Vertex authenticates with cloud credentials:
	// those then ARE its model credential.
	for _, kv := range env {
		switch kv {
		case "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_BEDROCK=true":
			for _, e := range env {
				if k, _, _ := strings.Cut(e, "="); strings.HasPrefix(k, "AWS_") {
					keep[k] = true
				}
			}
		case "CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_USE_VERTEX=true":
			for _, e := range env {
				if k, _, _ := strings.Cut(e, "="); strings.HasPrefix(k, "GOOGLE_") || strings.HasPrefix(k, "CLOUDSDK_") || strings.HasPrefix(k, "ANTHROPIC_VERTEX_") {
					keep[k] = true
				}
			}
		}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if credentialEnv(k, keep) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// jailNetwork turns the isolation's network block into the sandbox spec's
// enforced form for the jail: open → host network; audit → everything
// through the proxy, recorded; deny → the model endpoints only; an egress
// list → it plus the model endpoints; the older `{deny: true}` alone → a
// full cut. An agent-authored dispatch with no network block is `deny`.
func jailNetwork(s *sandbox.Spec, n *config.IsolationNetwork, tool string, agentAuthored bool) {
	s.Deny, s.Egress, s.HasEgress = false, nil, false
	if n == nil && agentAuthored {
		n = &config.IsolationNetwork{Mode: config.NetDeny}
	}
	if n == nil || n.Mode == config.NetOpen {
		return
	}
	model := modelEndpoints[tool]
	switch {
	case n.Mode == config.NetAudit:
		s.Deny, s.Egress, s.HasEgress = true, []string{"*"}, true
	case n.Mode == config.NetDeny:
		s.Deny, s.Egress, s.HasEgress = true, append([]string(nil), model...), true
	case n.Deny && len(n.Egress) == 0:
		s.Deny = true // the legacy full cut
	default:
		s.Deny, s.Egress, s.HasEgress = true, append(append([]string(nil), n.Egress...), model...), true
	}
}

// prepareJail builds this launch's jail and wraps argv in it. It returns
// handled=false when the synthesized default could not be built (degraded
// loudly): the caller then launches exactly as before.
func prepareJail(dir string, env, argv []string, opt launchOpts) (wrapped []string, outEnv []string, cleanup func(), handled bool, err error) {
	noop := func() {}
	spec := *opt.jail
	degrade := func(why string) ([]string, []string, func(), bool, error) {
		if !opt.jailDefault {
			return nil, nil, noop, false, fmt.Errorf("isolation: the workspace jail cannot be built here (%s) — an explicit isolation: block fails closed; set `mode: none` to run unconfined", why)
		}
		log.Printf("WARNING: conductor: the default workspace jail is unavailable for %s (%s) — running WITHOUT confinement; set isolation explicitly to require it, or `mode: none` to silence this", firstNonEmpty(spec.Label, "an agent launch"), why)
		if JailDegraded != nil {
			JailDegraded(spec.DispatchID, spec.Label, why)
		}
		return nil, nil, noop, false, nil
	}
	if why := jailProbe(); why != "" {
		return degrade(why)
	}
	spec.Workspace = dir
	if len(argv) > 0 {
		spec.Argv0 = argv[0]
	}
	st := opt.jailState
	if st != nil {
		st.mu.Lock()
		spec.Git = st.git
		st.mu.Unlock()
	}
	l, perr := JailManager.Prepare(context.Background(), spec)
	if perr != nil {
		return degrade(perr.Error())
	}
	if st != nil {
		st.mu.Lock()
		if st.git == nil {
			st.git = l.Dispatch.Git
		}
		st.mu.Unlock()
	}
	if opt.claudeHooks && l.ClaudeSettings != "" {
		argv = append(append([]string(nil), argv...), "--settings", l.ClaudeSettings)
	}
	ss := sandbox.FromConfig(opt.iso)
	if ss == nil {
		ss = &sandbox.Spec{Mode: "namespace"}
	}
	ss.Mode = "namespace"
	var n *config.IsolationNetwork
	if opt.iso != nil {
		n = opt.iso.Network
	}
	jailNetwork(ss, n, spec.Tool, opt.agentAuthored)
	label := spec.DispatchID
	deps := sandbox.LocalWrapDeps{
		SelfExe:    launchSelfExe,
		Confine:    true,
		ExtraBinds: l.Binds,
		Agent:      l.Seatbelt,
	}
	if EgressProxyUnixLabeled != nil {
		deps.EgressUnix = func(allow []string) (string, string, func(), error) { return EgressProxyUnixLabeled(allow, label) }
	} else {
		deps.EgressUnix = EgressProxyUnix
	}
	if EgressProxyForLabeled != nil {
		deps.EgressAddr = func(allow []string) (string, string, func(), error) { return EgressProxyForLabeled(allow, label) }
	} else {
		deps.EgressAddr = EgressProxyFor
	}
	base := append(scrubJailEnv(env, spec.Tool), l.Env...)
	w, wenv, wclean, werr := sandbox.WrapLocalCommand(ss, argv, dir, base, deps)
	if werr != nil {
		l.Close()
		if opt.jailDefault {
			return degrade(werr.Error())
		}
		return nil, nil, noop, false, werr
	}
	return w, wenv, func() { wclean(); l.Close() }, true, nil
}

// JailBaseEnv is the inherited environment a jailed process starts from: the
// daemon's own, minus every credential variable.
func JailBaseEnv(tool string) []string { return scrubJailEnv(os.Environ(), tool) }

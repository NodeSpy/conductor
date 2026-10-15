package agentsandbox

import (
	"strings"
)

// envOps are the operational variables a sandboxed agent inherits — no
// credentials, nothing that locates the operator's home (HOME is the
// identity's) or the host's temp dir (the sandbox has its own).
var envOps = map[string]bool{
	"PATH": true, "USER": true, "LOGNAME": true, "SHELL": true, "TZ": true, "LANG": true,
	"TERM": true, "COLORTERM": true, "NO_COLOR": true, "FORCE_COLOR": true,
}

// envModelKnobs are model-selection and behavior switches an operator sets
// for an agent CLI. None is a credential; each is passed through when set.
var envModelKnobs = map[string]bool{
	"ANTHROPIC_MODEL": true, "ANTHROPIC_SMALL_FAST_MODEL": true,
	"ANTHROPIC_DEFAULT_OPUS_MODEL": true, "ANTHROPIC_DEFAULT_SONNET_MODEL": true, "ANTHROPIC_DEFAULT_HAIKU_MODEL": true,
	"CLAUDE_CODE_SUBAGENT_MODEL": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_MAX_OUTPUT_TOKENS": true,
	"MAX_THINKING_TOKENS": true, "OPENAI_MODEL": true,
}

// envKnobPrefixes are switch families passed through (DISABLE_TELEMETRY,
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, …).
var envKnobPrefixes = []string{"DISABLE_", "CLAUDE_CODE_DISABLE_", "LC_"}

// AgentEnv builds a sandboxed agent's environment from scratch out of
// inherited (the launching process's own): the operational basics, the
// tool's model credentials and endpoint overrides, model-selection knobs,
// then HOME (the identity's), the policy's own variables, and the conductor
// skill endpoint + this launch's token. Everything else is dropped — a push
// or GitHub token, SSH_AUTH_SOCK, cloud credentials, a launcher's own
// messaging token, the daemon's skill token, other CLAUDE_CODE_* state — so
// none of it reaches the agent even when the launcher carried it.
func AgentEnv(inherited []string, p Policy) []string {
	keep := map[string]bool{}
	if t, ok := tools[p.Tool]; ok {
		for _, k := range t.creds {
			keep[k] = true
		}
		for _, k := range t.baseEnv {
			keep[k] = true
		}
	}
	out := make([]string, 0, 16)
	for _, kv := range inherited {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if envOps[k] || keep[k] || envModelKnobs[k] || hasAnyPrefix(k, envKnobPrefixes) {
			out = append(out, kv)
		}
	}
	out = append(out, "HOME="+p.Home)
	out = append(out, p.Env...)
	if p.Socket != "" && p.Token != "" {
		out = append(out, "CONDUCTOR_ENDPOINT=unix://"+p.Socket, "CONDUCTOR_SKILL_TOKEN="+p.Token)
	}
	return dedupEnv(out)
}

// dedupEnv keeps the LAST value of each key, in first-seen order.
func dedupEnv(env []string) []string {
	idx := map[string]int{}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

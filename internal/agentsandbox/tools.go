// Package agentsandbox is conductor's slim agent sandbox
// (docs/design/agent-sandbox.md). Agents are read-only and conductor performs
// every write: an agent conductor launches — a cli or acp runtime's worker,
// a paseo session through `conductor sandbox exec`, a member worker — runs
// under the built-in internal/sandbox (namespace + pivot_root on Linux,
// Seatbelt on macOS) with
//
//   - its workspace read-write;
//   - HOME set to a per-identity tool-state directory conductor pre-creates
//     (seeded once from the operator's settings, never their history);
//   - the conductor skill socket bound (the socket file, never its dir);
//   - enforced egress: no network of its own, only conductor's allowlist
//     proxy (the model endpoints, plus what the operator adds), and the
//     host-loopback model endpoint relayed in where there is one;
//   - a minimal environment: no push, cloud or GitHub credential;
//   - nothing else of the operator's home or conductor's state: what is not
//     bound is absent, so there is no deny-list to keep complete.
//
// It fails closed: a launch whose sandbox cannot be built does not run.
// Policy is what one launch is allowed; Wrap realizes it around an argv.
package agentsandbox

import (
	"path/filepath"
	"strings"
)

// toolInfo is what the sandbox needs to know about one agent CLI.
type toolInfo struct {
	// state are the home-relative paths the tool keeps its state in; each
	// exists in the identity home (a dir stays a dir).
	state []string
	// login are home-relative files holding the tool's sign-in, shared into
	// the identity home from the operator's real home, so the agent is signed
	// in as the operator and a token refresh is the operator's.
	login []string
	// seed are home-relative files copied once into a fresh identity home:
	// the operator's settings for the tool (seedFilter drops history).
	seed []string
	// endpoints are the hosts the tool's own model traffic needs — always
	// allowed: a sandbox with no network is not one where the agent cannot
	// think.
	endpoints []string
	// baseEnv name the tool's model-endpoint override variables.
	baseEnv []string
	// creds are the variables the tool authenticates its model traffic with,
	// kept in the sandbox's environment.
	creds []string
}

var (
	anthropicHosts = []string{"api.anthropic.com", "statsig.anthropic.com", "console.anthropic.com", "claude.ai", "mcp-proxy.anthropic.com", "platform.claude.com"}
	openaiHosts    = []string{"api.openai.com", "chatgpt.com", "auth.openai.com", "ab.chatgpt.com"}
	googleHosts    = []string{"generativelanguage.googleapis.com", "oauth2.googleapis.com", "cloudcode-pa.googleapis.com"}
)

var tools = map[string]toolInfo{
	"claude-code": {
		state:     []string{".claude", ".claude.json", ".config/claude"},
		login:     []string{".claude/.credentials.json"},
		seed:      []string{".claude.json", ".claude/settings.json"},
		endpoints: anthropicHosts,
		baseEnv:   []string{"ANTHROPIC_BASE_URL"},
		creds:     []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"},
	},
	"codex": {
		state:     []string{".codex"},
		login:     []string{".codex/auth.json"},
		seed:      []string{".codex/config.toml"},
		endpoints: openaiHosts,
		baseEnv:   []string{"OPENAI_BASE_URL"},
		creds:     []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
	},
	"gemini": {
		state:     []string{".gemini"},
		login:     []string{".gemini/oauth_creds.json", ".gemini/google_accounts.json"},
		seed:      []string{".gemini/settings.json"},
		endpoints: googleHosts,
		baseEnv:   []string{"GOOGLE_GEMINI_BASE_URL"},
		creds:     []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	},
	// pi (pi-coding-agent) talks to whichever provider it is configured
	// for; the common ones are allowed.
	"pi": {
		state:     []string{".pi"},
		login:     []string{".pi/agent/auth.json"},
		seed:      []string{".pi/agent/settings.json", ".pi/agent/models.json"},
		endpoints: append(append([]string(nil), anthropicHosts...), openaiHosts...),
		baseEnv:   []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"},
		creds:     []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY"},
	},
	"qwen": {
		state:     []string{".qwen"},
		login:     []string{".qwen/oauth_creds.json"},
		seed:      []string{".qwen/settings.json"},
		baseEnv:   []string{"OPENAI_BASE_URL"},
		creds:     []string{"OPENAI_API_KEY", "DASHSCOPE_API_KEY"},
		endpoints: []string{"dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "chat.qwen.ai"},
	},
}

// CanonicalTool maps a recipe tool, a provider name or a launch binary to the
// tool table's key: "claude" and a wrapper script named claude-something are
// claude-code. "" when nothing matches (a tool the sandbox knows nothing
// about still runs sandboxed, with no model endpoint allowed unless the
// operator adds one).
func CanonicalTool(name string) string {
	n := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if _, ok := tools[n]; ok {
		return n
	}
	switch {
	case n == "claude" || strings.HasPrefix(n, "claude-") || strings.HasPrefix(n, "claude_") || n == "claude-agent-acp":
		return "claude-code"
	case strings.HasPrefix(n, "codex"):
		return "codex"
	case strings.HasPrefix(n, "gemini"):
		return "gemini"
	case n == "pi" || strings.HasPrefix(n, "pi-"):
		return "pi"
	case strings.HasPrefix(n, "qwen"):
		return "qwen"
	}
	return ""
}

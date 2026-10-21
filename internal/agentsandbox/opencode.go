package agentsandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// opencode talks to whichever provider its model names — it is
// multi-provider, so its allowlist is not a fixed list like claude-code's.
// opencodeRoute derives it from what is configured: the dispatch's model
// ("anthropic/claude-sonnet-4-5"), the operator's opencode config (its
// `model` / `small_model`, and a provider's own `options.baseURL` — a local
// Ollama or LM Studio is a loopback endpoint, relayed in like a local model
// router), and, when nothing names a provider, the providers the operator has
// signed in to (auth.json) or holds a key for in the environment: the ones
// opencode would pick from.

// opencodeProviderHosts are the model endpoints of opencode's built-in
// providers, by provider id. A provider not listed here (or a custom one with
// no baseURL) gets none: the operator adds its host to sandbox.egress.
var opencodeProviderHosts = map[string][]string{
	"anthropic":    anthropicHosts,
	"openai":       openaiHosts,
	"google":       {"generativelanguage.googleapis.com"},
	"openrouter":   {"openrouter.ai"},
	"groq":         {"api.groq.com"},
	"mistral":      {"api.mistral.ai"},
	"deepseek":     {"api.deepseek.com"},
	"xai":          {"api.x.ai"},
	"opencode":     {"opencode.ai"},
	"togetherai":   {"api.together.xyz"},
	"fireworks-ai": {"api.fireworks.ai"},
	"cerebras":     {"api.cerebras.ai"},
	"moonshotai":   {"api.moonshot.ai"},
	"zai":          {"api.z.ai"},
}

// opencodeProviderKeys are the variables opencode reads a provider's key
// from — model credentials, kept in the agent's environment like
// claude-code's ANTHROPIC_API_KEY.
var opencodeProviderKeys = []string{
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY",
	"OPENROUTER_API_KEY", "GROQ_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY",
	"OPENCODE_API_KEY", "TOGETHER_AI_API_KEY", "FIREWORKS_API_KEY", "CEREBRAS_API_KEY",
	"MOONSHOT_API_KEY", "ZHIPU_API_KEY",
}

// opencodeKeyProvider maps a key variable to the provider it signs in to.
var opencodeKeyProvider = map[string]string{
	"ANTHROPIC_API_KEY": "anthropic", "OPENAI_API_KEY": "openai",
	"GOOGLE_GENERATIVE_AI_API_KEY": "google", "GEMINI_API_KEY": "google",
	"OPENROUTER_API_KEY": "openrouter", "GROQ_API_KEY": "groq", "MISTRAL_API_KEY": "mistral",
	"DEEPSEEK_API_KEY": "deepseek", "XAI_API_KEY": "xai", "OPENCODE_API_KEY": "opencode",
	"TOGETHER_AI_API_KEY": "togetherai", "FIREWORKS_API_KEY": "fireworks-ai",
	"CEREBRAS_API_KEY": "cerebras", "MOONSHOT_API_KEY": "moonshotai", "ZHIPU_API_KEY": "zai",
}

// opencodeCatalog is the model catalog opencode reads its providers' models
// from at start (models.opencode.ai since 1.x, models.dev before): data,
// not a model endpoint, and read-only.
var opencodeCatalog = []string{"models.dev", "models.opencode.ai"}

// opencodeConfig is the part of an opencode config the route reads.
type opencodeConfig struct {
	Model      string `json:"model"`
	SmallModel string `json:"small_model"`
	Provider   map[string]struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
	} `json:"provider"`
}

// opencodeRoute is opencode's model route for model ("" = whatever its
// config or sign-ins pick): the base URLs it reaches (endpoint URLs, split
// into allowlist hosts and relays by ModelRouteFor) and the provider hosts.
func opencodeRoute(model, home string, env []string) (urls, hosts []string) {
	cfg := readOpencodeConfig(home)
	var named []string
	for _, m := range []string{model, cfg.Model, cfg.SmallModel} {
		if p, _, ok := strings.Cut(m, "/"); ok && p != "" {
			named = appendUnique(named, p)
		}
	}
	if len(named) == 0 {
		// Nothing names a provider: opencode picks among those it can sign in
		// to, so each is allowed — and every provider the config defines.
		named = append(named, opencodeSignedIn(home, env)...)
		for id := range cfg.Provider {
			named = appendUnique(named, id)
		}
		sort.Strings(named)
	}
	hosts = append([]string(nil), opencodeCatalog...)
	for _, p := range named {
		if pc, ok := cfg.Provider[p]; ok && pc.Options.BaseURL != "" {
			// The operator pointed this provider somewhere: that endpoint,
			// not the provider's public one.
			urls = appendUnique(urls, pc.Options.BaseURL)
			continue
		}
		for _, h := range opencodeProviderHosts[p] {
			hosts = appendUnique(hosts, h)
		}
	}
	return urls, hosts
}

// opencodeSignedIn are the providers the operator holds a login (auth.json)
// or a key variable for.
func opencodeSignedIn(home string, env []string) []string {
	var out []string
	if home != "" {
		if b, err := os.ReadFile(filepath.Join(home, ".local", "share", "opencode", "auth.json")); err == nil {
			var m map[string]json.RawMessage
			if json.Unmarshal(b, &m) == nil {
				for id := range m {
					out = appendUnique(out, id)
				}
			}
		}
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if p := opencodeKeyProvider[k]; p != "" && v != "" {
			out = appendUnique(out, p)
		}
	}
	return out
}

// readOpencodeConfig reads the operator's global opencode config (JSON or
// JSONC, under any of the names opencode looks for, merged in its order).
func readOpencodeConfig(home string) opencodeConfig {
	var cfg opencodeConfig
	if home == "" {
		return cfg
	}
	dir := filepath.Join(home, ".config", "opencode")
	for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var c opencodeConfig
		if json.Unmarshal(stripJSONC(b), &c) != nil {
			continue
		}
		if c.Model != "" {
			cfg.Model = c.Model
		}
		if c.SmallModel != "" {
			cfg.SmallModel = c.SmallModel
		}
		for id, p := range c.Provider {
			if cfg.Provider == nil {
				cfg.Provider = map[string]struct {
					Options struct {
						BaseURL string `json:"baseURL"`
					} `json:"options"`
				}{}
			}
			cfg.Provider[id] = p
		}
	}
	return cfg
}

// stripJSONC drops // and /* */ comments (outside strings) and trailing
// commas, turning opencode's JSONC into JSON.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			// A trailing comma before the close.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

package agentsandbox

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// lookupIP resolves an operator-configured model host; a var for tests.
var lookupIP = net.LookupIP

// ModelRoute splits tool's model endpoints into allowlist entries for the
// egress proxy (hosts) and host-loopback addresses that must be relayed into
// the sandbox (loopback: "127.0.0.1:3456" — a local model router).
//
// The endpoints are the tool's own defaults plus any override the operator
// configured: a *_BASE_URL in env (the launching process's environment), else
// claude-code's `env` in <home>/.claude/settings.json, where an operator
// routes claude-code through a local router.
func ModelRoute(tool, home string, env []string) (hosts, loopback []string) {
	t := tools[tool]
	hosts = append(hosts, t.endpoints...)
	for _, o := range modelOverrides(tool, home, env) {
		u, err := url.Parse(o)
		if err != nil || u.Hostname() == "" {
			continue
		}
		h, port := u.Hostname(), u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		if isLoopbackHost(h) {
			if h == "localhost" {
				h = "127.0.0.1"
			}
			loopback = appendUnique(loopback, net.JoinHostPort(h, port))
			continue
		}
		hosts = appendUnique(hosts, h+":"+port)
		// The operator configured this endpoint explicitly: if it is a
		// private-network host (a router on a tailnet), list its addresses
		// literally — the proxy's SSRF guard lets an internal address
		// through only when it is named as an IP.
		if ips, err := lookupIP(h); err == nil {
			for _, ip := range ips {
				hosts = appendUnique(hosts, net.JoinHostPort(ip.String(), port))
			}
		}
	}
	return hosts, loopback
}

// modelOverrides are the tool's configured model-endpoint overrides: its
// base-URL variables in env, then (claude-code) settings.json's env block.
func modelOverrides(tool, home string, env []string) []string {
	var out []string
	vars := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	for _, k := range tools[tool].baseEnv {
		if v := vars[k]; v != "" {
			out = append(out, v)
		}
	}
	if tool == "claude-code" && home != "" {
		if v := claudeSettingsEnv(filepath.Join(home, ".claude", "settings.json"))["ANTHROPIC_BASE_URL"]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// claudeSettingsEnv reads the env block of a claude-code settings.json.
func claudeSettingsEnv(p string) map[string]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.Env
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
)

// Section reads (design §5.4): one derived shape per screen, read from the live
// config file (so they reflect exactly what is on disk, the same bytes the
// editor stages against). Secret VALUES are never read or returned.

// builtinPlugins are the connector/runtime names conductor ships in the binary —
// not fetched, so the version policy does not apply to them.
var builtinPlugins = map[string]bool{
	"github": true, "rss": true, "webhook": true, "cron": true,
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	var resp api.SecretsResponse

	if vaults, ok := tree["vaults"].(map[string]any); ok {
		names := sortedKeys(vaults)
		for _, name := range names {
			vi := api.VaultInfo{Name: name}
			if m, ok := vaults[name].(map[string]any); ok {
				if t, ok := m["type"].(string); ok {
					vi.Type = t
				}
			}
			resp.Vaults = append(resp.Vaults, vi)
		}
	}

	if secrets, ok := tree["secrets"].(map[string]any); ok {
		names := sortedKeys(secrets)
		for _, name := range names {
			ref, _ := secrets[name].(string)
			si := api.SecretInfo{Name: name, Ref: ref}
			// Where is ${settings.<name>}-style or the secret name referenced?
			si.UsedBy = scanRefs(tree, "${secrets."+name+"}")
			resp.Secrets = append(resp.Secrets, si)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePacks(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	var resp api.PacksResponse
	if packs, ok := tree["packs"].(map[string]any); ok {
		for _, name := range sortedKeys(packs) {
			pi := api.PackInfo{Name: name}
			if m, ok := packs[name].(map[string]any); ok {
				pi.Use, _ = m["use"].(string)
				pi.Version, _ = m["version"].(string)
				pi.Hold, _ = m["hold"].(bool)
				if pi.Version == "" {
					pi.Version = versionFromUse(pi.Use)
				}
			}
			resp.Packs = append(resp.Packs, pi)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	// name → {version, pinned, usedBy}
	type agg struct {
		version string
		pinned  bool
		usedBy  []string
	}
	plugins := map[string]*agg{}

	note := func(use, by string) {
		if use == "" {
			return
		}
		name, ver := splitUse(use)
		a := plugins[name]
		if a == nil {
			a = &agg{}
			plugins[name] = a
		}
		if ver != "" {
			a.version = ver
			a.pinned = true
		}
		a.usedBy = append(a.usedBy, by)
	}

	for _, section := range []string{"connectors", "runtimes"} {
		if m, ok := tree[section].(map[string]any); ok {
			for _, name := range sortedKeys(m) {
				if entry, ok := m[name].(map[string]any); ok {
					if use, ok := entry["use"].(string); ok {
						note(use, section+"."+name)
					}
				}
			}
		}
	}

	var resp api.PluginsResponse
	for _, name := range sortedKeys(mapKeysToAny(plugins)) {
		a := plugins[name]
		sort.Strings(a.usedBy)
		resp.Plugins = append(resp.Plugins, api.PluginInfo{
			Name: name, Version: a.version, Pinned: a.pinned,
			Builtin: builtinPlugins[name], UsedBy: a.usedBy,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	var resp api.ConnectionsResponse

	if m, ok := tree["connectors"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			use := ""
			if entry, ok := m[name].(map[string]any); ok {
				use, _ = entry["use"].(string)
			}
			resp.Connectors = append(resp.Connectors, api.NamedUse{Name: name, Use: use})
		}
	}
	if m, ok := tree["runtimes"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			use := ""
			switch entry := m[name].(type) {
			case map[string]any:
				use, _ = entry["use"].(string)
			case string:
				use = entry
			}
			resp.Runtimes = append(resp.Runtimes, api.NamedUse{Name: name, Use: use})
		}
	}
	if m, ok := tree["hosts"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			hi := api.HostInfo{Name: name}
			if entry, ok := m[name].(map[string]any); ok {
				hi.Host, _ = entry["host"].(string)
				hi.User, _ = entry["user"].(string)
			}
			resp.Hosts = append(resp.Hosts, hi)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	resp := api.SystemResponse{
		Version:   s.deps.Version,
		ConfigSHA: s.deps.ConfigSHA,
		Settings:  map[string]string{},
	}
	if m, ok := tree["settings"].(map[string]any); ok {
		for k, v := range m {
			resp.Settings[k] = fmt.Sprintf("%v", v)
		}
	}

	// Safety posture.
	var safety api.SafetyInfo
	if sb, ok := tree["sandbox"].(map[string]any); ok {
		safety.SandboxAgents, _ = sb["agents"].(string)
		if eg, ok := sb["egress"].([]any); ok {
			for _, h := range eg {
				safety.Egress = append(safety.Egress, fmt.Sprintf("%v", h))
			}
		}
	}
	if db, ok := tree["dashboard"].(map[string]any); ok {
		if l, ok := db["listen"].([]any); ok {
			for _, x := range l {
				safety.DashboardListen = append(safety.DashboardListen, fmt.Sprintf("%v", x))
			}
		}
	}
	if safety.SandboxAgents == "off" {
		safety.Warnings = append(safety.Warnings, "agents run unsandboxed (sandbox.agents: off)")
	}
	for _, h := range safety.Egress {
		if h == "*" || strings.HasPrefix(h, "*.") {
			safety.Warnings = append(safety.Warnings, "egress allows a wildcard host ("+h+")")
		}
	}
	for _, l := range safety.DashboardListen {
		if l == "tailscale" {
			safety.Warnings = append(safety.Warnings, "the dashboard is reachable over the tailnet (dashboard.listen: tailscale)")
		}
	}
	resp.Safety = safety

	// Update block.
	if up, ok := tree["update"].(map[string]any); ok {
		resp.Update.Auto, _ = up["auto"].(bool)
		resp.Update.Interval, _ = up["interval"].(string)
		resp.Update.Apply = fmt.Sprintf("%v", up["apply"])
		if _, ok := up["apply"]; !ok {
			resp.Update.Apply = ""
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries := readAuditTail(s.deps.AuditPath, 200)
	writeJSON(w, http.StatusOK, api.AuditResponse{Entries: entries})
}

// --- helpers ---

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mapKeysToAny[T any](m map[string]T) map[string]any {
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// splitUse splits a `use:` reference into its plugin name and pinned version.
// "github@1.3.0" → ("github", "1.3.0"); "acme/thing@2" → ("acme/thing", "2").
func splitUse(use string) (name, version string) {
	if i := strings.LastIndex(use, "@"); i > 0 {
		return use[:i], use[i+1:]
	}
	return use, ""
}

func versionFromUse(use string) string {
	_, v := splitUse(use)
	return v
}

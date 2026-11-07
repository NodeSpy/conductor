package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/secrets"
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

	if secs, ok := tree["secrets"].(map[string]any); ok {
		names := sortedKeys(secs)
		for _, name := range names {
			ref, _ := secs[name].(string)
			si := api.SecretInfo{Name: name, Ref: ref}
			// Where is ${settings.<name>}-style or the secret name referenced?
			si.UsedBy = scanRefs(tree, "${secrets."+name+"}")
			resp.Secrets = append(resp.Secrets, si)
		}
	}

	// Real configs mostly skip the legacy secrets: map and reference a vault
	// entry directly wherever a credential is needed: client_secret: '{{ vault
	// "house" "key" }}'. Walk every string leaf for that syntax (the one
	// internal/secrets.ParseVaultRef recognizes — a whole-field reference, the
	// documented form for credentials) so those show here too, under the vault
	// entry's own key name, with every path that names it.
	for _, vr := range scanVaultRefs(tree) {
		resp.Secrets = append(resp.Secrets, vr)
	}
	sort.Slice(resp.Secrets, func(i, j int) bool {
		if resp.Secrets[i].Name != resp.Secrets[j].Name {
			return resp.Secrets[i].Name < resp.Secrets[j].Name
		}
		return resp.Secrets[i].Ref < resp.Secrets[j].Ref
	})
	writeJSON(w, http.StatusOK, resp)
}

// scanVaultRefs walks the config tree for {{ vault "<name>" "<key>" }} /
// {{ .vaults.<name>.<key> }} whole-field references (internal/secrets.
// ParseVaultRef — the documented way credentials are wired to a vault) and
// returns one SecretInfo per distinct vault/key pair, named by its key, with
// every dotted path that references it. The vault's actual value is never
// read: this only recognizes the reference syntax in the config text itself.
func scanVaultRefs(tree any) []api.SecretInfo {
	type key struct{ vault, name string }
	usedBy := map[key][]string{}
	var order []key
	var walk func(prefix []string, v any)
	walk = func(prefix []string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range sortedKeys(x) {
				walk(append(append([]string{}, prefix...), k), x[k])
			}
		case []any:
			for i, item := range x {
				walk(append(append([]string{}, prefix...), fmt.Sprintf("%d", i)), item)
			}
		case string:
			if vault, name, ok := secrets.ParseVaultRef(x); ok {
				k := key{vault, name}
				if _, seen := usedBy[k]; !seen {
					order = append(order, k)
				}
				usedBy[k] = append(usedBy[k], strings.Join(prefix, "."))
			}
		}
	}
	walk(nil, tree)
	out := make([]api.SecretInfo, 0, len(order))
	for _, k := range order {
		by := usedBy[k]
		sort.Strings(by)
		out = append(out, api.SecretInfo{Name: k.name, Ref: k.vault + "/" + k.name, UsedBy: by})
	}
	return out
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
			typ := ""
			if s.deps.Cfg != nil {
				if rt, ok := s.deps.Cfg.Runtimes[name]; ok {
					typ = rt.BuiltinType()
				}
			}
			resp.Runtimes = append(resp.Runtimes, api.NamedUse{Name: name, Use: use, Type: typ})
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
	for k := range resp.Settings {
		if by := scanRefs(tree, "${settings."+k+"}"); len(by) > 0 {
			if resp.SettingsUsedBy == nil {
				resp.SettingsUsedBy = map[string][]string{}
			}
			resp.SettingsUsedBy[k] = by
		}
	}
	resp.EnvVars = envVars(tree)

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
	safety.Outside = outsideSandboxItems(s.deps.Cfg)
	resp.Safety = safety

	// Update block: the configured policy, this process's own last check (if
	// any — Check now runs it; nothing is checked on page load), and the
	// releases this platform has published.
	resp.Update.Version = s.deps.Version
	if up, ok := tree["update"].(map[string]any); ok {
		resp.Update.Auto, _ = up["auto"].(bool)
		resp.Update.Interval, _ = up["interval"].(string)
		if d, ok := up["deps"].(bool); ok {
			resp.Update.Deps = d
		} else {
			resp.Update.Deps = resp.Update.Auto // Update.Deps follows auto when unset (config.Update.DepsEnabled)
		}
		if rv, ok := up["reload"].(string); ok {
			resp.Update.ReloadInPlace = rv == "in place"
		}
	}
	s.updateMu.Lock()
	if s.updated {
		resp.Update.Checked = true
		resp.Update.LastCheck = s.lastChk.UTC().Format(time.RFC3339)
		resp.Update.Available = s.updInfo.Available
		resp.Update.Checks = s.updInfo.Checks
		resp.Update.CheckError = s.updInfo.CheckError
	}
	s.updateMu.Unlock()
	if s.deps.ListReleases != nil {
		if tags, err := s.deps.ListReleases(); err == nil {
			resp.Update.Releases = buildReleaseRows(tags, s.deps.Version, resp.Update.Available)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// releaseRowCap is how many rows the Releases list shows: the running
// release plus a handful of recent ones either side of it, matching the
// design's short list rather than every tag the platform has ever
// published.
const releaseRowCap = 5

// buildReleaseRows turns every published tag into the Releases list's rows:
// pre-releases (-rc.N) dropped unless running is itself one, newest first,
// capped to releaseRowCap. "Go back to…" only ever makes sense for a release
// OLDER than the one running — a newer, unverified tag is never offered as a
// rollback target; the one Check now verified is "ready" instead.
func buildReleaseRows(tags []string, running, available string) []api.ReleaseInfo {
	runningIsRC := strings.Contains(running, "-rc")
	kept := make([]string, 0, len(tags))
	for _, t := range tags {
		if strings.Contains(t, "-rc") && !runningIsRC && t != running {
			continue
		}
		kept = append(kept, t)
	}
	sort.Slice(kept, func(i, j int) bool { return config.CompareVersions(kept[i], kept[j]) > 0 })
	if len(kept) > releaseRowCap {
		kept = kept[:releaseRowCap]
	}
	out := make([]api.ReleaseInfo, 0, len(kept))
	for _, t := range kept {
		out = append(out, api.ReleaseInfo{
			Version: t,
			Current: t == running,
			Ready:   t == available && available != "",
			Older:   t != running && config.CompareVersions(t, running) < 0,
		})
	}
	return out
}

// handleUpdateCheck is Updates' "Check now": looks up the newest published
// release and, if newer, fetches and preflights it (never installs) — real
// pass/fail checks, not developer copy. The result is kept in memory for
// subsequent GET /api/v1/system reads, until the next check or a restart.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.deps.CheckUpdate == nil {
		writeError(w, http.StatusNotFound, "no_updater", "this daemon has no updater wired", "")
		return
	}
	info, err := s.deps.CheckUpdate()
	if err != nil {
		writeError(w, http.StatusBadGateway, "check_failed", err.Error(), "")
		return
	}
	s.updateMu.Lock()
	s.updated = true
	s.lastChk = time.Now()
	s.updInfo = info
	s.updateMu.Unlock()
	s.handleSystem(w, r)
}

// handleUpdateInstall is Updates' "Install now" (tag "") and "Go back to
// vX…" (tag set, force implied) — both run deps.InstallUpdate, which is
// cmd/conductor's own doUpdate: fetch, verify, preflight, atomic swap,
// restart. A session is required once sign-in is enforced, matching config
// apply's own gating (this changes what code the daemon runs, same weight).
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if s.deps.InstallUpdate == nil {
		writeError(w, http.StatusNotFound, "no_updater", "this daemon has no updater wired", "")
		return
	}
	// force is NOT an accepted field here, on purpose: this endpoint never
	// skips the preflight, and the only installs it allows are (a) the
	// release Check now already verified newer, or (b) an older, already
	// published release (a rollback) — both validated below against
	// ListReleases, never trusted from the request body.
	var req struct {
		Version string `json:"version"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	tags, lerr := s.listReleaseTags()
	if lerr != nil {
		writeError(w, http.StatusBadGateway, "list_failed", lerr.Error(), "")
		return
	}
	if req.Version == "" || !tags[req.Version] {
		writeError(w, http.StatusBadRequest, "unknown_version", "that version isn't one of this platform's published releases", "")
		return
	}
	// Installing (or rolling back) the running binary changes what code the
	// daemon runs for every bot and every workflow — the same weight as a
	// config apply, so it needs the same step-up (passkey/SSH), bound to the
	// exact version so a stolen token can't be replayed against a different
	// one, and it's audited like every other privileged action: who, which
	// version, when.
	digest := updateInstallDigest(req.Version)
	if s.authEnforced() {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		if _, err := s.auth.RequireStepup(sess, auth.PurposeUpdateNow, digest, r.Header.Get(auth.StepupHeader)); err != nil {
			desc := fmt.Sprintf("install %s (the version running now is kept, so you can go back)", req.Version)
			s.labels.note(r, auth.PurposeUpdateNow, digest, desc)
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":  map[string]string{"code": "stepup_required", "message": err.Error(), "hint": "confirm it's you to install an update"},
				"stepup": map[string]string{"purpose": string(auth.PurposeUpdateNow), "digest": digest, "description": desc},
			})
			return
		}
	}
	installed, err := s.deps.InstallUpdate(req.Version)
	if err != nil {
		writeError(w, http.StatusBadGateway, "install_failed", err.Error(), "")
		return
	}
	s.audit(r, "dashboard.update.install", digest, "version", req.Version)
	writeJSON(w, http.StatusOK, map[string]any{"installed": installed})
}

// updateInstallDigest binds an update install's step-up to the exact version
// requested, the same way a forced retry's step-up is bound to its run and
// starting step (retryDigest) — a token confirmed for one version can't be
// replayed to install a different one.
func updateInstallDigest(version string) string {
	b, _ := json.Marshal(map[string]any{"v": "1", "ns": "conductor-dashboard-update", "version": version})
	return auth.DigestOf(b)
}

// listReleaseTags is the set of versions this endpoint will ever install —
// exactly what deps.ListReleases reports, so a request can never name a
// version that was never actually published for this platform.
func (s *Server) listReleaseTags() (map[string]bool, error) {
	out := map[string]bool{}
	if s.deps.ListReleases == nil {
		return out, nil
	}
	tags, err := s.deps.ListReleases()
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		out[t] = true
	}
	return out, nil
}

// handleDaemonLog is System → Logs & audit's "Daemon log" tab: the process's
// own recent log lines (internal/logring), already redacted the same way
// every other logged line is.
func (s *Server) handleDaemonLog(w http.ResponseWriter, r *http.Request) {
	var lines []api.LogLine
	if s.deps.DaemonLog != nil {
		lines = s.deps.DaemonLog()
	}
	writeJSON(w, http.StatusOK, api.LogResponse{Lines: lines})
}

// handleSetPaused is System → Safety's "Pause all automation" / "Resume
// automation" — an emergency stop, not a config widening: the prototype asks
// for a plain confirmation, never a passkey (CONFORMANCE: turning the sandbox
// off asks for a passkey because it loosens safety; pausing only narrows what
// runs, same as it already does for the CLI's `conductor pause`/`resume` and
// the conductor.pause/resume verbs — deps.SetPaused is literally that same
// function). A session is required once sign-in is enforced, so the action is
// attributed; an open dashboard (no operators, e.g. every e2e fixture) may
// call it directly, matching config apply's own gating.
func (s *Server) handleSetPaused(w http.ResponseWriter, r *http.Request) {
	if s.deps.SetPaused == nil {
		writeError(w, http.StatusNotFound, "no_pause", "this daemon has no pause control wired", "")
		return
	}
	var req struct {
		Paused bool `json:"paused"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	who := "an open dashboard"
	if s.authEnforced() {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		who = sess.Operator
	}
	if err := s.deps.SetPaused(req.Paused); err != nil {
		writeError(w, http.StatusBadRequest, "pause_failed", err.Error(), "")
		return
	}
	event := "dashboard.resume"
	if req.Paused {
		event = "dashboard.pause"
	}
	s.audit(r, event, "", "operator", who)
	writeJSON(w, http.StatusOK, map[string]any{"paused": req.Paused})
}

// outsideSandboxItems lists every step and runtime that opted OUT of the
// confined-by-default posture — `trust: full` or `isolation: {mode: none}` —
// the same set `conductor validate` warns about (codeIsolationOptOutWarnings,
// internal/flow/isolationwarn.go), reshaped into what/why/who rows for the
// Safety screen's "Running outside the sandbox" card instead of one long
// sentence. Quiet when the whole default is off (code.isolation_default:
// none): every step runs bare by that global choice, and naming each one
// would be noise, not signal — mirrors the validate-time behavior exactly.
func outsideSandboxItems(cfg *config.Config) []api.OutsideSandboxItem {
	if cfg == nil || !cfg.CodeIsolationDefaultOn() {
		return nil
	}
	var out []api.OutsideSandboxItem
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, st *config.Step) {
		if _, class := st.StepEngine(); class != config.EngineCLI && class != config.EngineHost {
			return
		}
		if st.Host != "" || st.SSH != nil {
			return
		}
		where := config.StepLabel(scope, slot, *st)
		switch {
		case st.Trust == "full":
			out = append(out, api.OutsideSandboxItem{
				What: where,
				Why:  "trust: full opts it out of the sandbox. It runs unconfined, with this daemon's view of the machine.",
				Who:  "your config",
			})
		case st.Isolation.OptsOut():
			out = append(out, api.OutsideSandboxItem{
				What: where,
				Why:  "isolation: { mode: none } opts it out of the sandbox. It runs unconfined, with this daemon's view of the machine.",
				Who:  "your config",
			})
		}
	})
	names := make([]string, 0, len(cfg.Runtimes))
	for n := range cfg.Runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rt := cfg.Runtimes[n]
		if strings.EqualFold(strings.TrimSpace(rt.Trust), "full") {
			out = append(out, api.OutsideSandboxItem{
				What: "the runtime " + n,
				Why:  "trust: full opts it out of its jail. Every agent it starts runs as you.",
				Who:  "your config",
			})
		}
	}
	return out
}

// envVars collects hosts.*.env — the one place config declares named,
// scoped env vars outside a step (HostConfig.Env; runtimes: has no env: field
// — a config with one fails to load, config.RuntimeConfig). Values are shown
// only when a value is itself a reference (where it lives), never a literal.
func envVars(tree any) []api.EnvVarInfo {
	t, _ := tree.(map[string]any)
	var out []api.EnvVarInfo
	m, ok := t["hosts"].(map[string]any)
	if !ok {
		return nil
	}
	for _, name := range sortedKeys(m) {
		entry, ok := m[name].(map[string]any)
		if !ok {
			continue
		}
		env, ok := entry["env"].(map[string]any)
		if !ok {
			continue
		}
		scope := "host:" + name
		for _, k := range sortedKeys(env) {
			v := fmt.Sprintf("%v", env[k])
			ev := api.EnvVarInfo{Name: k, Scope: scope}
			if isRefLiteral(v) {
				ev.Ref = v
			} else {
				ev.Literal = true
			}
			out = append(out, ev)
		}
	}
	return out
}

// isRefLiteral reports whether a value is itself a secret/setting reference
// (safe to echo verbatim: it names where a value lives, not the value) rather
// than a plain literal.
func isRefLiteral(v string) bool {
	return strings.HasPrefix(v, "env:") || strings.Contains(v, "${") || (strings.HasPrefix(v, "{{") && strings.HasSuffix(v, "}}"))
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

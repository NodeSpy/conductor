package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// The Plugins screen's read model: what each configured connection, agent
// runtime, code language and machine is, where its plugin comes from, and
// what can be added. Everything a plugin contributes (its kind, its
// connection fields, its permissions) is read from the plugin's own
// declaration or install record at runtime — nothing here names a plugin.

// loadPluginState reads the plugin install state (what `conductor plugin
// list` reads). A var so tests can hand in a synthetic state.
var loadPluginState = func() *plugin.InstallState { return plugin.LoadInstallState(plugin.InstallDir()) }

// pluginRefOf describes the plugin behind one `use:` reference.
func (s *Server) pluginRefOf(kind config.UseKind, use string, st *plugin.InstallState) api.PluginRef {
	name, constraint := splitUse(use)
	ref := api.PluginRef{Use: use, Plugin: name, Constraint: constraint}
	u, err := config.ParseUse(kind, use)
	if err != nil {
		ref.Origin = "custom"
		return ref
	}
	ref.Plugin = u.Name
	ref.Constraint = u.Version
	switch {
	case u.IsBuiltin():
		ref.Origin = "builtin"
		ref.Version = s.deps.Version
		ref.Constraint = ""
		return ref
	case u.Origin == config.OriginLocal:
		ref.Origin = "local"
		ref.Source = use
	default:
		ref.Source = u.Source()
		ref.Origin = "custom"
		if config.IsOfficialSource(ref.Source) {
			ref.Origin = "official"
		}
	}
	switch c := ref.Constraint; {
	case c == "":
		ref.Policy = "current"
	case strings.HasPrefix(c, "^") || strings.HasPrefix(c, "~"):
		ref.Policy = "range"
	default:
		ref.Policy = "pinned"
	}
	if st != nil && u.Origin != config.OriginLocal {
		if inst, ok := st.GetForConstraint(u.InstallKey(), u); ok && inst.Path != "" {
			ref.Version = inst.Resolved
			ref.Permissions = plugin.Agreement(u.Name, inst.Manifest)
		}
		for _, p := range st.PendingUpdates() {
			if p.Key == u.InstallKey() && !p.Approved {
				ref.Waiting = p.Resolved
			}
		}
	}
	return ref
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	st := loadPluginState()
	resp := api.ConnectionsResponse{Version: s.deps.Version, Connectors: []api.ConnectorRow{}, Runtimes: []api.RuntimeRow{}, Hosts: []api.HostInfo{}}
	cfg := s.deps.Cfg

	if m, ok := tree["connectors"].(map[string]any); ok {
		today := s.runsTodayByConnector()
		for _, name := range sortedKeys(m) {
			use := ""
			row := api.ConnectorRow{Name: name}
			if entry, ok := m[name].(map[string]any); ok {
				use, _ = entry["use"].(string)
				if en, ok := entry["enabled"].(bool); ok && !en {
					row.Disabled = true
				}
			}
			row.PluginRef = s.pluginRefOf(config.UseKindConnector, use, st)
			if d, ok := connector.TypeDeclFor(row.Plugin); ok && d.Unavailable != "" {
				row.Unavailable = d.Unavailable
			} else if !ok && row.Origin != "builtin" {
				row.Unavailable = "its plugin isn't installed yet"
			}
			row.RunsToday = today[name]
			row.Bots = botsUsingConnector(cfg, name)
			resp.Connectors = append(resp.Connectors, row)
		}
	}
	if m, ok := tree["runtimes"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			use := ""
			row := api.RuntimeRow{Name: name}
			switch entry := m[name].(type) {
			case map[string]any:
				use, _ = entry["use"].(string)
				row.Tool, _ = entry["tool"].(string)
				row.Default, _ = entry["default"].(bool)
				row.Host, _ = entry["host"].(string)
				if iso, ok := entry["isolation"].(map[string]any); ok {
					row.Isolation, _ = iso["mode"].(string)
				}
			case string:
				use = entry
			}
			row.PluginRef = s.pluginRefOf(config.UseKindRuntime, use, st)
			if cfg != nil {
				if rt, ok := cfg.Runtimes[name]; ok {
					row.Type = rt.BuiltinType()
				}
			}
			row.Bots = botsOnRuntime(cfg, name)
			resp.Runtimes = append(resp.Runtimes, row)
		}
	}
	if m, ok := tree["hosts"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			hi := api.HostInfo{Name: name}
			if entry, ok := m[name].(map[string]any); ok {
				hi.Host, _ = entry["host"].(string)
				hi.User, _ = entry["user"].(string)
				switch p := entry["port"].(type) {
				case int:
					hi.Port = p
				case float64:
					hi.Port = int(p)
				}
				if env, ok := entry["env"].(map[string]any); ok {
					hi.Env = len(env)
				}
			}
			for _, rt := range resp.Runtimes {
				if rt.Host == name {
					hi.Runtimes = append(hi.Runtimes, rt.Name)
				}
			}
			resp.Hosts = append(resp.Hosts, hi)
		}
	}
	resp.Languages = s.languageInfo(tree, st)
	writeJSON(w, http.StatusOK, resp)
}

// runsTodayByConnector counts today's runs per connector: the runs whose
// trigger fired on one of its events (`on: <connector>.<event>`).
func (s *Server) runsTodayByConnector() map[string]int {
	out := map[string]int{}
	if s.deps.Store == nil {
		return out
	}
	now := time.Now()
	if s.deps.Now != nil {
		now = s.deps.Now()
	}
	y, mo, d := now.Date()
	start := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
	for _, rec := range s.deps.Store.ListHistory(0) {
		if rec.Started.Before(start) {
			continue
		}
		if c := connectorOf(rec); c != "" {
			out[c]++
		}
	}
	return out
}

// botsUsingConnector names the bots granted one of a connection's verbs.
func botsUsingConnector(cfg *config.Config, name string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for bot, bc := range cfg.Bots {
		if bc.Skill == nil {
			continue
		}
		for _, v := range bc.Skill.Verbs {
			if strings.HasPrefix(v, name+".") {
				out = append(out, bot)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// botsOnRuntime names the bots whose worker runs on a runtime.
func botsOnRuntime(cfg *config.Config, name string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for bot, bc := range cfg.Bots {
		if bc.Worker.Runtime == name {
			out = append(out, bot)
		}
	}
	sort.Strings(out)
	return out
}

// languageInfo reads the Code languages tab: conductor's built-in engines,
// then every engine a workflow or trigger step names (a program on this
// machine, or an engine plugin), then any `engines:` hardening entry. An
// engine has no block of its own, so this walks the steps — the same
// Step.StepEngine resolution the dispatcher uses.
func (s *Server) languageInfo(tree map[string]any, st *plugin.InstallState) []api.LanguageInfo {
	type agg struct {
		class string
		steps int
		wfs   map[string]bool
	}
	seen := map[string]*agg{}
	note := func(name, class, where string) *agg {
		a := seen[name]
		if a == nil {
			a = &agg{class: class, wfs: map[string]bool{}}
			seen[name] = a
		}
		if where != "" {
			a.steps++
			a.wfs[where] = true
		}
		return a
	}
	for _, n := range config.BuiltinNames(config.UseKindEngine) {
		note(n, "builtin", "")
	}
	var walk func(where string, steps []config.Step)
	walk = func(where string, steps []config.Step) {
		for _, step := range steps {
			switch name, class := step.StepEngine(); class {
			case config.EngineCLI:
				note(name, "builtin", where)
			case config.EngineHost:
				note(name, "host", where)
			case config.EnginePlugin:
				note(name, "plugin", where)
			}
			if step.Parallel != nil {
				for _, b := range step.Parallel.Branches {
					walk(where, b)
				}
			}
		}
	}
	hardened := map[string]bool{}
	if cfg := s.deps.Cfg; cfg != nil {
		for name, wf := range cfg.Workflows {
			walk(name, wf.Steps)
		}
		for i, t := range cfg.Triggers {
			name := t.Name
			if name == "" {
				name = t.On
			}
			if name == "" {
				name = fmt.Sprintf("trigger %d", i+1)
			}
			walk(name, t.Steps)
		}
		for name := range cfg.Engines {
			hardened[name] = true
			if seen[name] == nil {
				note(name, "plugin", "")
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		// built in first, then by name
		bi, bj := seen[names[i]].class == "builtin", seen[names[j]].class == "builtin"
		if bi != bj {
			return bi
		}
		return names[i] < names[j]
	})
	out := make([]api.LanguageInfo, 0, len(names))
	for _, n := range names {
		a := seen[n]
		li := api.LanguageInfo{Name: n, Class: a.class, Steps: a.steps, Hardened: hardened[n], Workflows: []string{}}
		for wf := range a.wfs {
			li.Workflows = append(li.Workflows, wf)
		}
		sort.Strings(li.Workflows)
		switch a.class {
		case "builtin":
			li.PluginRef = api.PluginRef{Use: n, Plugin: n, Origin: "builtin", Version: s.deps.Version}
		case "host":
			li.PluginRef = api.PluginRef{Use: n, Plugin: n, Origin: "local"}
		default:
			li.PluginRef = s.pluginRefOf(config.UseKindEngine, n, st)
		}
		out = append(out, li)
	}
	return out
}

// handlePluginCatalog answers GET /api/v1/plugins/catalog?kind=: the kinds
// the Plugins page's "Add a …" picker offers. A kind is listed when
// conductor has it built in, when its plugin is in install state, or when
// the config already names it; its fields come from its own declaration.
func (s *Server) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	kind, ok := useKindOf(r.URL.Query().Get("kind"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_kind", "kind is connector, runtime or engine", "")
		return
	}
	st := loadPluginState()
	tree := s.liveTree()
	items := map[string]*api.CatalogItem{}
	add := func(name string) *api.CatalogItem {
		if it := items[name]; it != nil {
			return it
		}
		it := &api.CatalogItem{Name: name}
		ref := s.pluginRefOf(kind, name, st)
		it.Origin, it.Source, it.Installed = ref.Origin, ref.Source, ref.Version
		items[name] = it
		return it
	}
	switch kind {
	case config.UseKindConnector:
		for _, t := range connector.Types() {
			add(t)
		}
	default:
		for _, n := range config.BuiltinNames(kind) {
			add(n)
		}
	}
	for _, key := range st.Keys() {
		dir, name, ok := strings.Cut(key, "/")
		if ok && dir == kind.Dir() {
			add(name)
		}
	}
	if block, ok := tree[kind.Block()].(map[string]any); ok && kind != config.UseKindEngine {
		for _, inst := range sortedKeys(block) {
			use := ""
			switch e := block[inst].(type) {
			case map[string]any:
				use, _ = e["use"].(string)
			case string:
				use = e
			}
			if use == "" {
				continue
			}
			ref := s.pluginRefOf(kind, use, st)
			add(ref.Plugin).Configured++
		}
	}
	if kind == config.UseKindEngine {
		for _, li := range s.languageInfo(tree, st) {
			if li.Class != "host" {
				add(li.Name).Configured = li.Steps
			}
		}
	}
	resp := api.PluginCatalogResponse{Kind: string(kind), Items: []api.CatalogItem{}}
	for _, it := range items {
		if kind == config.UseKindConnector {
			fillDecl(it)
		}
		resp.Items = append(resp.Items, *it)
	}
	sort.Slice(resp.Items, func(i, j int) bool { return resp.Items[i].Name < resp.Items[j].Name })
	writeJSON(w, http.StatusOK, resp)
}

// fillDecl copies a connector kind's own declaration into its catalog item.
func fillDecl(it *api.CatalogItem) {
	d, ok := connector.TypeDeclFor(it.Name)
	if !ok {
		return
	}
	if d.Unavailable != "" {
		it.Unavailable = d.Unavailable
		return
	}
	it.HasSchema = true
	it.Desc = d.Desc
	it.Fields = catalogFields(d.Connection)
}

// handlePluginReview answers GET /api/v1/plugins/review?kind=&use=&as=:
// plugin.ReviewAdd's answer (the review `conductor plugin add` prints), read
// only — nothing is fetched or installed.
func (s *Server) handlePluginReview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, ok := useKindOf(q.Get("kind"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_kind", "kind is connector, runtime or engine", "")
		return
	}
	var trust *config.PackTrustConfig
	if s.deps.Cfg != nil {
		trust = s.deps.Cfg.PluginTrust
	}
	rev, err := plugin.ReviewAdd(kind, q.Get("use"), q.Get("as"), trust, loadPluginState())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_use", err.Error(), "")
		return
	}
	out := api.AddReview{
		Kind: string(kind), Use: strings.TrimSpace(q.Get("use")), Plugin: rev.Use.Name, Instance: rev.Instance,
		Source: rev.Source, Builtin: rev.Builtin, Official: rev.Official, Trusted: rev.Trusted,
		Permissions: rev.Permissions, Snippet: rev.Snippet,
	}
	switch {
	case rev.Builtin:
		out.Origin = "builtin"
	case rev.Use.Origin == config.OriginLocal:
		out.Origin = "local"
	case rev.Official:
		out.Origin = "official"
	default:
		out.Origin = "custom"
	}
	if !rev.Trusted {
		out.TrustEntry = rev.Source
	}
	if rev.Installed != nil {
		out.Installed = rev.Installed.Resolved
	}
	if kind == config.UseKindConnector {
		it := &api.CatalogItem{Name: rev.Use.Name}
		fillDecl(it)
		out.HasSchema, out.Fields = it.HasSchema, it.Fields
	}
	writeJSON(w, http.StatusOK, out)
}

func useKindOf(s string) (config.UseKind, bool) {
	switch s {
	case "connector", "":
		return config.UseKindConnector, true
	case "runtime":
		return config.UseKindRuntime, true
	case "engine":
		return config.UseKindEngine, true
	}
	return "", false
}

func catalogFields(sch connector.Schema) []api.CatalogField {
	if len(sch) == 0 {
		return nil
	}
	keys := make([]string, 0, len(sch))
	for k := range sch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]api.CatalogField, 0, len(keys))
	for _, k := range keys {
		f := sch[k]
		out = append(out, api.CatalogField{
			Key: k, Type: string(f.Type), Required: f.Required,
			Secret: f.Secret, Enum: f.Enum, Desc: f.Desc,
		})
	}
	return out
}

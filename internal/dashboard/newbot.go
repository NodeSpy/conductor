package dashboard

import (
	"net/http"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// New bot's read model (the guided wizard): what each configured connection
// can deliver to a bot and do for it, and the bot templates packs ship.
// Everything comes from the plugin's own declaration at runtime and the
// pack's own manifest; nothing here names a plugin, an event or a verb.

// newbotEvent is one event a connection delivers.
type newbotEvent struct {
	Name string `json:"name"` // "" for a dynamic event (its names come from the connection's config)
	Desc string `json:"desc,omitempty"`
	// Dynamic: the event's names are the connection's own (configured
	// sources, schedules); Seen are the ones this config already routes.
	Dynamic bool     `json:"dynamic,omitempty"`
	Seen    []string `json:"seen,omitempty"`
	Filters []string `json:"filters,omitempty"` // keys its `filter:` accepts
	Context []string `json:"context,omitempty"` // values a step may template from it
}

// newbotVerb is one verb a connection offers.
type newbotVerb struct {
	Name    string   `json:"name"`
	Desc    string   `json:"desc,omitempty"`
	Posts   bool     `json:"posts,omitempty"` // declares conversation_post: it posts into a conversation
	Options []string `json:"options,omitempty"`
}

type newbotConnection struct {
	Name        string        `json:"name"`
	Plugin      string        `json:"plugin"`
	Unavailable string        `json:"unavailable,omitempty"`
	Events      []newbotEvent `json:"events"`
	Verbs       []newbotVerb  `json:"verbs"`
}

// handleNewbotConnections is GET /api/v1/newbot/connections.
func (s *Server) handleNewbotConnections(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	st := loadPluginState()
	out := []newbotConnection{}
	m, _ := tree["connectors"].(map[string]any)
	seen := routedEvents(tree)
	for _, name := range sortedKeys(m) {
		use := ""
		if entry, ok := m[name].(map[string]any); ok {
			use, _ = entry["use"].(string)
		}
		ref := s.pluginRefOf(config.UseKindConnector, use, st)
		c := newbotConnection{Name: name, Plugin: ref.Plugin, Events: []newbotEvent{}, Verbs: []newbotVerb{}}
		d, ok := connector.TypeDeclFor(ref.Plugin)
		if s.deps.ConnectorDecl != nil {
			// The live instance's own declaration: a connection whose events
			// its config names (configured sources, schedules) lists those.
			if ld, lok := s.deps.ConnectorDecl(name); lok && ld != nil {
				d, ok = ld, true
			}
		}
		switch {
		case !ok || d == nil:
			c.Unavailable = "its plugin isn't installed yet"
		case d.Unavailable != "":
			c.Unavailable = d.Unavailable
		default:
			for _, e := range d.Events {
				ev := newbotEvent{Desc: e.Desc, Dynamic: e.Dynamic, Filters: schemaKeys(e.FilterKeys()), Context: schemaKeys(e.Context)}
				if e.Dynamic {
					ev.Seen = seen[name]
				} else {
					ev.Name = e.Name
				}
				c.Events = append(c.Events, ev)
			}
			for _, v := range d.Verbs {
				c.Verbs = append(c.Verbs, newbotVerb{Name: v.Name, Desc: v.Desc, Posts: v.Semantics != nil && v.Semantics.ConversationPost, Options: schemaKeys(v.Options)})
			}
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// routedEvents is, per connection, the event names this config already
// routes: its triggers' `on: <connection>.<event>` and its teams' routing
// rules' `match: { on: … }`.
func routedEvents(tree map[string]any) map[string][]string {
	set := map[string]map[string]bool{}
	add := func(on string) {
		conn, ev, ok := strings.Cut(on, ".")
		if !ok || ev == "" || strings.Contains(on, "{{") {
			return
		}
		if set[conn] == nil {
			set[conn] = map[string]bool{}
		}
		set[conn][ev] = true
	}
	ts, _ := tree["triggers"].([]any)
	for _, t := range ts {
		tm, _ := t.(map[string]any)
		on, _ := tm["on"].(string)
		add(on)
	}
	teams, _ := tree["teams"].(map[string]any)
	for _, t := range teams {
		tm, _ := t.(map[string]any)
		rt, _ := tm["routing"].(map[string]any)
		rules, _ := rt["rules"].([]any)
		for _, r := range rules {
			rm, _ := r.(map[string]any)
			match, _ := rm["match"].(map[string]any)
			on, _ := match["on"].(string)
			add(on)
		}
	}
	out := map[string][]string{}
	for conn, evs := range set {
		for ev := range evs {
			out[conn] = append(out[conn], ev)
		}
		sort.Strings(out[conn])
	}
	return out
}

func schemaKeys(s connector.Schema) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// newbotTemplate is one bot template a pack ships.
type newbotTemplate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Pack        string `json:"pack"`
	Version     string `json:"version,omitempty"`
	Official    bool   `json:"official"`
	// Instance is the installed pack instance (`use: <instance>/<name>`);
	// empty when the pack isn't installed: InstallKey/InstallRef are then
	// the packs: entry that installs it.
	Instance   string `json:"instance,omitempty"`
	InstallKey string `json:"install_key,omitempty"`
	InstallRef string `json:"install_ref,omitempty"`
}

// handleNewbotTemplates is GET /api/v1/newbot/templates: the bot templates
// of installed packs and of the registry's, read from each pack's manifest
// through the same catalog `conductor pack search` serves.
func (s *Server) handleNewbotTemplates(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"templates": []newbotTemplate{}})
		return
	}
	result, ok := withFetchTimeout(r.Context(), func() config.CatalogResult {
		return config.PackCatalog(s.deps.ConfigPath, "", s.deps.PackRegistry, false, true)
	})
	registryErr := ""
	if !ok {
		result = config.PackCatalog(s.deps.ConfigPath, "", s.deps.PackRegistry, true, true)
		registryErr = "the pack registry took too long to answer"
	} else if result.RegistryError != "" {
		registryErr = "the pack registry couldn't be read"
	}
	out := []newbotTemplate{}
	for _, p := range result.Packs {
		if p.Manifest == nil || len(p.Manifest.Bots) == 0 {
			continue
		}
		t := newbotTemplate{Pack: p.Name, Version: p.Version}
		if ref, err := config.ParsePackRef(p.Source); err == nil {
			t.Official = ref.Official
			if !p.Installed {
				t.InstallKey, t.InstallRef = ref.Key, ref.Value
			}
		}
		if p.Installed {
			t.Instance = p.Instance
		}
		names := make([]string, 0, len(p.Manifest.Bots))
		for n := range p.Manifest.Bots {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			tt := t
			tt.Name, tt.Description = n, p.Manifest.Bots[n].Description
			out = append(out, tt)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out, "registry_error": registryErr})
}

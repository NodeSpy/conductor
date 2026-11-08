package dashboard

import (
	"net/http"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// The verbs a bot may be given, in words (a bot's "What it can do" tab): every
// configured connection's verbs, each with the description its plugin
// declares. Conductor learns the verbs from the plugin at runtime; nothing here
// names a plugin or a verb.

// botVerb is one verb a bot may be granted.
type botVerb struct {
	Verb       string `json:"verb"`       // <connection>.<verb>
	Desc       string `json:"desc"`       // the plugin's own description, may be empty
	Connection string `json:"connection"` // the connection instance
	Kind       string `json:"kind"`       // the plugin behind it
}

// handleBotVerbs is GET /api/v1/connections/verbs.
func (s *Server) handleBotVerbs(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	st := loadPluginState()
	out := []botVerb{}
	if m, ok := tree["connectors"].(map[string]any); ok {
		for _, name := range sortedKeys(m) {
			use := ""
			if entry, ok := m[name].(map[string]any); ok {
				use, _ = entry["use"].(string)
			}
			ref := s.pluginRefOf(config.UseKindConnector, use, st)
			d, ok := connector.TypeDeclFor(ref.Plugin)
			if !ok || d == nil || d.Unavailable != "" {
				continue
			}
			for _, v := range d.Verbs {
				out = append(out, botVerb{Verb: name + "." + v.Name, Desc: v.Desc, Connection: name, Kind: ref.Plugin})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"verbs": out})
}

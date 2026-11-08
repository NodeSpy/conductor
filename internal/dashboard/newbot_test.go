package dashboard

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// New bot learns a connection's events and verbs from its plugin's own
// declaration: a synthetic kind's events (fixed and dynamic) and verbs come
// back as declared, with the post semantic and the filter keys.
func TestNewbotConnectionsFromThePluginDeclaration(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	d, _ := connector.TypeDeclFor(synthKind)
	d.Events = []connector.EventDecl{
		{Name: "changed", Desc: "a widget changed", Filters: connector.Schema{"shelf": {Type: connector.TString}}, Context: connector.Schema{"title": {Type: connector.TString}}},
		{Name: "<feed>", Desc: "a feed delivered", Dynamic: true},
	}
	d.Verbs = []connector.VerbDecl{
		{Name: "read", Desc: "Read a widget"},
		{Name: "note", Desc: "Leave a note", Options: connector.Schema{"room": {Type: connector.TString}}, Semantics: &sdk.VerbSemantics{ConversationPost: true}},
	}
	var resp struct {
		Connections []newbotConnection `json:"connections"`
	}
	getJSONInto(t, s, "/api/v1/newbot/connections", &resp)
	var w1 *newbotConnection
	for i := range resp.Connections {
		if resp.Connections[i].Name == "w1" {
			w1 = &resp.Connections[i]
		}
	}
	if w1 == nil || len(w1.Events) != 2 || len(w1.Verbs) != 2 {
		t.Fatalf("connections: %+v", resp.Connections)
	}
	if e := w1.Events[0]; e.Name != "changed" || e.Dynamic || len(e.Filters) != 1 || e.Filters[0] != "shelf" || len(e.Context) != 1 {
		t.Errorf("fixed event: %+v", e)
	}
	if e := w1.Events[1]; e.Name != "" || !e.Dynamic {
		t.Errorf("dynamic event: %+v", e)
	}
	if v := w1.Verbs[1]; v.Name != "note" || !v.Posts || len(v.Options) != 1 || v.Options[0] != "room" {
		t.Errorf("post verb: %+v", v)
	}
	if w1.Verbs[0].Posts {
		t.Errorf("read verb marked as posting: %+v", w1.Verbs[0])
	}
}

// A connection's live declaration wins over its plugin's type-level one: the
// events its own config names are the ones listed.
func TestNewbotConnectionsPreferTheLiveDeclaration(t *testing.T) {
	s, _, _ := newPluginCatalogServer(t)
	s.deps.ConnectorDecl = func(name string) (*connector.TypeDecl, bool) {
		if name != "hooks" {
			return nil, false
		}
		return &connector.TypeDecl{Type: "webhook", Events: []connector.EventDecl{{Name: "deploy", Desc: "a configured source received a delivery"}}}, true
	}
	var resp struct {
		Connections []newbotConnection `json:"connections"`
	}
	getJSONInto(t, s, "/api/v1/newbot/connections", &resp)
	for _, c := range resp.Connections {
		if c.Name == "hooks" {
			if len(c.Events) != 1 || c.Events[0].Name != "deploy" || c.Events[0].Dynamic {
				t.Fatalf("hooks events: %+v", c.Events)
			}
			return
		}
	}
	t.Fatalf("no hooks connection: %+v", resp.Connections)
}

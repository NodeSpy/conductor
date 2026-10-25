package dashboard

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/store"
)

// Connector-published filters (design §4.2): the filter bar's MENUS are
// conductor's own concepts — status, workflow, bot. What a connector's events
// carry — a GitHub repo, an issue number — is the connector's concept, so it is
// offered as a SEARCH QUALIFIER (github.repo:owner/name), never its own menu.
// These endpoints publish those qualifiers and their autocomplete values,
// derived from what the run history actually carries (so the bar never offers a
// qualifier with nothing behind it).

// connectorOf is the connector namespace a run came through: the part of its
// trigger `on:` before the dot (github.pull_request → github). The built-in
// manual source has none.
func connectorOf(rec store.RunHistory) string {
	on := rec.On
	if on == "" || on == "manual" {
		return ""
	}
	if i := strings.IndexByte(on, '.'); i > 0 {
		return on[:i]
	}
	return on
}

// handleConnectorAttrs publishes the connector search qualifiers present in the
// run history: for each connector whose runs carry a repo (or number), a
// `<connector>.repo` (or `.number`) qualifier.
func (s *Server) handleConnectorAttrs(w http.ResponseWriter, r *http.Request) {
	hasRepo := map[string]bool{}
	hasNum := map[string]bool{}
	for _, rec := range s.deps.Store.ListHistory(0) {
		conn := connectorOf(rec)
		if conn == "" {
			continue
		}
		if rec.Repo != "" {
			hasRepo[conn] = true
		}
		if rec.Number > 0 {
			hasNum[conn] = true
		}
	}
	var attrs []api.AttrQualifier
	for conn := range hasRepo {
		attrs = append(attrs, api.AttrQualifier{Key: conn + ".repo", Connector: conn, Field: "repo", Label: "Repo"})
	}
	for conn := range hasNum {
		attrs = append(attrs, api.AttrQualifier{Key: conn + ".number", Connector: conn, Field: "number", Label: "Number"})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })
	writeJSON(w, http.StatusOK, api.AttrsResponse{Attrs: attrs})
}

// handleAttrValues serves autocomplete values for one qualifier: the distinct
// values present in the run history for ?attr=<connector>.<field>.
func (s *Server) handleAttrValues(w http.ResponseWriter, r *http.Request) {
	attr := r.URL.Query().Get("attr")
	conn, field, ok := strings.Cut(attr, ".")
	if !ok || (field != "repo" && field != "number") {
		writeJSON(w, http.StatusOK, api.AttrValuesResponse{Values: []string{}})
		return
	}
	seen := map[string]bool{}
	var vals []string
	for _, rec := range s.deps.Store.ListHistory(0) {
		if connectorOf(rec) != conn {
			continue
		}
		v := attrValue(rec, field)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		vals = append(vals, v)
	}
	sort.Strings(vals)
	if vals == nil {
		vals = []string{}
	}
	writeJSON(w, http.StatusOK, api.AttrValuesResponse{Values: vals})
}

// attrValue reads one qualifier field off a run record.
func attrValue(rec store.RunHistory, field string) string {
	switch field {
	case "repo":
		return rec.Repo
	case "number":
		if rec.Number > 0 {
			return strconv.Itoa(rec.Number)
		}
	}
	return ""
}

// matchesAttrs reports whether a run satisfies every connector-qualifier filter
// present in the query (keys like "github.repo"). A qualifier matches only runs
// from its own connector with the given field value; an unknown qualifier
// excludes nothing (forward-compatible with qualifiers a newer client sends).
func matchesAttrs(rec store.RunHistory, q url.Values) bool {
	for key, vals := range q {
		conn, field, ok := strings.Cut(key, ".")
		if !ok || (field != "repo" && field != "number") {
			continue
		}
		if connectorOf(rec) != conn {
			return false
		}
		got := attrValue(rec, field)
		matched := false
		for _, want := range vals {
			if want == got {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

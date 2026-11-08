package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// The workflow builder's read models (design: js/screens/builder.js): the
// reusable workflows: entries beside the triggers, what each configured
// connection can do (from its plugin's own declaration, read at runtime), the
// past events a test can be pointed at, and a manual run. Edits still go
// through the config staging pipeline; nothing here writes the config.

// reusableSummaries lists the workflows: map's entries (run with call:) as
// Workflows rows. A pack's own reusable workflows are listed under the pack.
func (s *Server) reusableSummaries(hist map[string][]store.RunHistory, now time.Time) []api.WorkflowSummary {
	out := make([]api.WorkflowSummary, 0, len(s.deps.Cfg.Workflows))
	for name, def := range s.deps.Cfg.Workflows {
		ws := api.WorkflowSummary{
			Name:        name,
			Steps:       len(def.Steps),
			Index:       -1,
			When:        "When another workflow calls it",
			Does:        stepsDoes(def.Steps),
			Kind:        "workflow",
			Description: def.Description,
		}
		if def.PackInstance != "" {
			ws.Source = "pack:" + def.PackInstance
		}
		if def.Description != "" {
			ws.Does = def.Description
		}
		if h := hist[name]; len(h) > 0 {
			ws.Runs = len(h)
			ws.RunsWeek, ws.FailedWeek, ws.Health, ws.Last20 = wfHealth(h, now)
		} else {
			ws.Health = "none"
		}
		out = append(out, ws)
	}
	return out
}

// liveWorkflowsMap is the live config FILE's own workflows: map (not the
// loaded one, which also carries pack and file-imported workflows).
func (s *Server) liveWorkflowsMap() map[string]any {
	m, _ := s.liveTree()["workflows"].(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// writeReusableDetail serves one workflows: entry as a workflow page.
func (s *Server) writeReusableDetail(w http.ResponseWriter, name string, def config.WorkflowDef) {
	b, err := yaml.Marshal(def)
	yamlText := string(b)
	if err != nil {
		yamlText = "# could not render: " + err.Error()
	}
	resp := api.WorkflowDetailResponse{
		Name:        name,
		YAML:        yamlText,
		Nodes:       graphOf(def.Steps, yamlText),
		Runs:        []api.RunSummary{},
		Index:       -1,
		When:        "When another workflow calls it",
		Does:        stepsDoes(def.Steps),
		Kind:        "workflow",
		Description: def.Description,
	}
	if def.PackInstance != "" {
		resp.Source = "pack:" + def.PackInstance
	}
	if def.Description != "" {
		resp.Does = def.Description
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleWorkflowCatalog serves every configured connection's verbs and events,
// as its plugin declares them. Core copies the declaration; it never lists a
// plugin's verbs itself.
func (s *Server) handleWorkflowCatalog(w http.ResponseWriter, r *http.Request) {
	tree := s.liveTree()
	st := loadPluginState()
	resp := api.WorkflowCatalogResponse{Connections: []api.CatalogConnection{}}
	m, _ := tree["connectors"].(map[string]any)
	for _, name := range sortedKeys(m) {
		use := ""
		if entry, ok := m[name].(map[string]any); ok {
			use, _ = entry["use"].(string)
		}
		ref := s.pluginRefOf(config.UseKindConnector, use, st)
		c := api.CatalogConnection{Name: name, Use: ref.Plugin, Verbs: []api.CatalogVerb{}, Events: []api.CatalogEvent{}}
		d, ok := connector.TypeDeclFor(ref.Plugin)
		switch {
		case !ok:
			c.Unavailable = "its plugin isn't installed yet"
		case d.Unavailable != "":
			c.Unavailable = d.Unavailable
		default:
			c.Desc = d.Desc
			for _, v := range d.Verbs {
				c.Verbs = append(c.Verbs, api.CatalogVerb{
					Name: v.Name, Desc: v.Desc, Ask: v.Ask, Open: v.Open,
					Options: catalogFields(v.Options), Outputs: catalogFields(v.Outputs),
				})
			}
			for _, e := range d.Events {
				ev := api.CatalogEvent{Name: e.Name, Desc: e.Desc, Dynamic: e.Dynamic, Fields: catalogFields(e.Context)}
				keys := e.MatchKeys
				if len(keys) == 0 {
					keys = e.Filters
				}
				for k := range keys {
					ev.Filters = append(ev.Filters, k)
				}
				sort.Strings(ev.Filters)
				c.Events = append(c.Events, ev)
			}
		}
		resp.Connections = append(resp.Connections, c)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleWorkflowBuilder serves where a workflow lives in the config file and
// the past events its test can be pointed at.
func (s *Server) handleWorkflowBuilder(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	info := api.WorkflowBuilderInfo{Kind: "trigger", File: s.deps.ConfigPath, Events: []api.WorkflowEvent{}}
	var ons []string
	if t, ok := s.findWorkflow(name); ok {
		if t.PackInstance == "" && t.Bot == "" {
			if i := s.rawTriggerIndex(t.Name); i >= 0 {
				info.Path = []string{"triggers", strconv.Itoa(i)}
			}
		}
		for _, x := range s.deps.Cfg.Triggers {
			if wfName(x) == wfName(t) && x.On != "" {
				ons = append(ons, x.On)
			}
		}
	} else if def, ok := s.deps.Cfg.Workflows[name]; ok {
		info.Kind = "workflow"
		if _, live := s.liveWorkflowsMap()[name]; live && def.PackInstance == "" {
			info.Path = []string{"workflows", name}
		}
	} else {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no workflow %q", name), "")
		return
	}
	info.Events = s.pastEvents(ons, 8)
	writeJSON(w, http.StatusOK, info)
}

// pastEvents returns up to n recent runs (the last 14 days) that started from
// one of the given sources, newest first, one per distinct event, with the
// fields each carried.
func (s *Server) pastEvents(ons []string, n int) []api.WorkflowEvent {
	out := []api.WorkflowEvent{}
	if len(ons) == 0 {
		return out
	}
	want := map[string]bool{}
	for _, o := range ons {
		want[o] = true
	}
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	seen := map[string]bool{}
	for _, rec := range s.deps.Store.ListHistory(0) {
		if len(out) >= n {
			break
		}
		if !want[rec.On] || rec.Started.Before(cutoff) || len(rec.Trigger) == 0 {
			continue
		}
		var t struct {
			Title   string
			Context map[string]any
		}
		if json.Unmarshal(rec.Trigger, &t) != nil {
			continue
		}
		key := rec.On + "|" + t.Title
		if rec.Repo != "" {
			key += "|" + rec.Repo + "#" + strconv.Itoa(rec.Number)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		label := strings.TrimSpace(t.Title)
		if label == "" {
			label = rec.On
		}
		out = append(out, api.WorkflowEvent{ID: rec.ID, On: rec.On, At: rec.Started.UTC().Format(time.RFC3339), Label: label, Fields: t.Context})
	}
	return out
}

// handleWorkflowRun starts a manual run of a workflow that starts by hand
// (on: manual), the same dispatch `conductor run <name>` makes.
func (s *Server) handleWorkflowRun(w http.ResponseWriter, r *http.Request) {
	if s.deps.RunManual == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "starting a run is not available on this daemon", "")
		return
	}
	name := r.PathValue("name")
	t, ok := s.findWorkflow(name)
	if !ok || t.Name == "" {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no workflow %q that can be started by name", name), "")
		return
	}
	if !t.Manual() {
		writeError(w, http.StatusBadRequest, "not_manual", fmt.Sprintf("%s starts on %s, not by hand", name, t.On), "add manual to its on: to run it from here")
		return
	}
	var req api.WorkflowRunRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	id, err := s.deps.RunManual(r.Context(), t.Name, req.Inputs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "run_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, api.ActionResponse{Message: fmt.Sprintf("started %s", t.Name), RunID: id})
}

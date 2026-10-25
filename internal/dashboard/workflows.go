package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// Workflows (design §4.4): every configured trigger is a workflow. The list and
// the one-workflow page read entirely from the live *config.Config and the run
// history — no engine call, no socket.

// wfName is a workflow's display name: its trigger name, or its `on:` when it
// has none.
func wfName(t config.TriggerSpec) string {
	if t.Name != "" {
		return t.Name
	}
	return t.On
}

// wfManual reports whether a workflow can be started by hand (a named trigger,
// or the built-in `manual` source) — which is what the dry-run Test offers.
func wfManual(t config.TriggerSpec) bool {
	return t.Name != "" || t.On == "manual"
}

// stepNodeID mirrors flow.stepID: a step's id is its own `id:`, else "stepN"
// (1-based). Matching that rule is what lets a graph node line up with a run's
// recorded steps (node→record) and the Activity run page.
func stepNodeID(s config.Step, i int) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("step%d", i+1)
}

// stepKind maps a step's form (config.Step.Form) onto the graph's node kinds,
// pulling `handoff:` out of the agent form (the graph draws it apart).
func stepKind(s config.Step) string {
	if s.Handoff != "" {
		return "handoff"
	}
	switch f := s.Form(); f {
	case "agent", "command", "code", "verb", "decide", "team", "parallel":
		return f
	case "workflow":
		return "call"
	case "":
		if s.Prompt != "" {
			return "agent"
		}
		return "step"
	default:
		return f // the helper forms (sleep, log, set, assert, fail, wait_for, bot)
	}
}

// stepLabel is a short human label for a node: the agent/verb/call target.
func stepLabel(s config.Step) string {
	switch {
	case s.Handoff != "":
		return s.Handoff
	case s.Agent != "":
		return s.Agent
	case s.Uses != "":
		return s.Uses
	case s.Workflow != "":
		return s.Workflow
	case s.Use != "":
		return s.Use
	case s.Run != "":
		return s.Run
	}
	return ""
}

// graphOf builds a workflow's nodes: one per top-level step, each depending on
// the one before it (conductor runs top-level steps in order). yamlText is
// scanned so each node carries the YAML line its list item starts on (node→YAML
// reveal), which works whether or not the step has an explicit `id:`.
func graphOf(steps []config.Step, yamlText string) []api.GraphNode {
	lines := stepItemLines(yamlText)
	out := make([]api.GraphNode, 0, len(steps))
	var prev string
	for i, s := range steps {
		id := stepNodeID(s, i)
		n := api.GraphNode{ID: id, Kind: stepKind(s), Label: stepLabel(s)}
		if i < len(lines) {
			n.Line = lines[i]
		}
		if prev != "" {
			n.Needs = []string{prev}
		}
		out = append(out, n)
		prev = id
	}
	return out
}

// stepItemLines returns the 1-based YAML line each top-level step list item
// starts on, in order, by scanning the first `steps:` block. Indentation-aware
// so nested list items (a parallel branch's steps) are not counted as top-level.
func stepItemLines(yamlText string) []int {
	lines := strings.Split(yamlText, "\n")
	var out []int
	inSteps := false
	stepsIndent := -1
	itemIndent := -1
	for i, raw := range lines {
		trimmed := strings.TrimRight(raw, "\r")
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		content := strings.TrimSpace(trimmed)
		if content == "" {
			continue
		}
		if !inSteps {
			if indent == 0 && (content == "steps:" || strings.HasPrefix(content, "steps:")) {
				inSteps = true
				stepsIndent = indent
			}
			continue
		}
		// A key at or below the steps indent that is not a list item ends the block.
		if indent <= stepsIndent && !strings.HasPrefix(content, "-") {
			break
		}
		if strings.HasPrefix(content, "-") {
			if itemIndent == -1 {
				itemIndent = indent
			}
			if indent == itemIndent {
				out = append(out, i+1)
			}
		}
	}
	return out
}

// handleWorkflows serves the Workflows list.
func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	recs := s.deps.Store.ListHistory(0)
	// Tally runs per workflow name. A run's workflow is its Variant, falling
	// back to Kind (the same projection RunSummary uses).
	type tally struct {
		runs, failed int
		last         time.Time
		lastID       string
	}
	byWF := map[string]*tally{}
	for _, rec := range recs {
		name := rec.Variant
		if name == "" {
			name = rec.Kind
		}
		t := byWF[name]
		if t == nil {
			t = &tally{}
			byWF[name] = t
		}
		t.runs++
		if rec.Status == "failed" {
			t.failed++
		}
		if rec.Started.After(t.last) {
			t.last = rec.Started
			t.lastID = rec.ID
		}
	}

	out := make([]api.WorkflowSummary, 0, len(s.deps.Cfg.Triggers))
	for _, t := range s.deps.Cfg.Triggers {
		name := wfName(t)
		ws := api.WorkflowSummary{
			Name:     name,
			On:       t.On,
			Steps:    len(t.Steps),
			Disabled: t.Enabled != nil && !*t.Enabled,
			Manual:   wfManual(t),
		}
		if tl := byWF[name]; tl != nil {
			ws.Runs = tl.runs
			ws.Failed = tl.failed
			ws.LastKey = tl.lastID
			if !tl.last.IsZero() {
				ws.LastRun = tl.last.UTC().Format(time.RFC3339Nano)
			}
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, api.WorkflowsResponse{Workflows: out})
}

// findWorkflow resolves a workflow name (trigger name first, then `on:`) to its
// spec.
func (s *Server) findWorkflow(name string) (config.TriggerSpec, bool) {
	for _, t := range s.deps.Cfg.Triggers {
		if t.Name == name {
			return t, true
		}
	}
	for _, t := range s.deps.Cfg.Triggers {
		if t.On == name {
			return t, true
		}
	}
	return config.TriggerSpec{}, false
}

// handleWorkflow serves one workflow: its YAML, its graph, and its recent runs.
func (s *Server) handleWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t, ok := s.findWorkflow(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no workflow %q", name), "")
		return
	}
	yamlText := marshalWorkflow(t)

	var runs []api.RunSummary
	for _, rec := range s.deps.Store.ListHistory(0) {
		rn := rec.Variant
		if rn == "" {
			rn = rec.Kind
		}
		if rn != wfName(t) {
			continue
		}
		runs = append(runs, toSummary(rec))
		if len(runs) >= 20 {
			break
		}
	}

	writeJSON(w, http.StatusOK, api.WorkflowDetailResponse{
		Name:     wfName(t),
		On:       t.On,
		Manual:   wfManual(t),
		Disabled: t.Enabled != nil && !*t.Enabled,
		YAML:     yamlText,
		Nodes:    graphOf(t.Steps, yamlText),
		Runs:     runs,
	})
}

// marshalWorkflow re-marshals a trigger spec to YAML for the workflow's YAML
// pane. It is the config as loaded (anchors already expanded) — a faithful
// read-only view, not the original source bytes.
func marshalWorkflow(t config.TriggerSpec) string {
	b, err := yaml.Marshal(t)
	if err != nil {
		return "# could not render: " + err.Error()
	}
	return string(b)
}

// handleWorkflowTest is the dry-run Test: it checks the workflow resolves (it is
// configured, enabled and has steps) without dispatching anything. A full
// template/verb replay belongs to the engine's dry-run; this is the safe,
// no-side-effect confidence check the Workflows page offers.
func (s *Server) handleWorkflowTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t, ok := s.findWorkflow(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no workflow %q", name), "")
		return
	}
	resp := api.TestResponse{Ok: true, Nodes: graphOf(t.Steps, marshalWorkflow(t))}
	switch {
	case t.Enabled != nil && !*t.Enabled:
		resp.Ok = false
		resp.Message = "This workflow is disabled (enabled: false); it will not run until re-enabled."
	case len(t.Steps) == 0:
		resp.Ok = false
		resp.Message = "This workflow has no steps, so a run would do nothing."
	default:
		plural := "steps"
		if len(t.Steps) == 1 {
			plural = "step"
		}
		resp.Message = fmt.Sprintf("Resolves: %d %s would run, in order. No agent or outbound verb was dispatched.", len(t.Steps), plural)
	}
	writeJSON(w, http.StatusOK, resp)
}

// workflowOfRun returns the workflow name a run belongs to (Variant, else Kind).
func workflowOfRun(rec store.RunHistory) string {
	if rec.Variant != "" {
		return rec.Variant
	}
	return rec.Kind
}

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
		n := api.GraphNode{ID: id, Kind: stepKind(s), Label: stepLabel(s), If: s.If}
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

// rawTriggerIndex finds a named trigger's position in the LIVE CONFIG FILE's
// own `triggers:` list — as opposed to its position in s.deps.Cfg.Triggers,
// which is the runtime-expanded list (a multi-source `on: [a, b]` trigger
// becomes one TriggerSpec per source, and pack/bot triggers are appended to
// it). A config-stage edit targets the raw document, so it must address the
// raw list; this is what the Workflows list and builder use for the on/off
// switch and a config-file save. Only a named trigger can be found this way
// (an unnamed one has no unambiguous raw identity); callers fall back to -1
// (not addressable) when a trigger has no name, which the UI treats as
// read-only for anything that stages a path.
func (s *Server) rawTriggerIndex(name string) int {
	if name == "" {
		return -1
	}
	seq, _ := s.liveTree()["triggers"].([]any)
	for i, item := range seq {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if n, _ := m["name"].(string); n == name {
			return i
		}
	}
	return -1
}

// wfSource is a workflow's ownership, as the Workflows list groups it: "" for
// one in the operator's own config, "pack:<instance>" for one a pack
// installed. A bot's own synthesized workflow has no row here at all — it
// lives on the bot's page (wfManual callers must check t.Bot first).
func wfSource(t config.TriggerSpec) string {
	if t.PackInstance != "" {
		return "pack:" + t.PackInstance
	}
	return ""
}

// wfWhen is what starts a workflow, in one short phrase. There is no
// connector event catalog on this side of the API, so it reads the trigger's
// own `on:`/`manual` mechanically rather than a curated English sentence per
// event (that catalog is the connector plugins', not core's to copy).
func wfWhen(t config.TriggerSpec) string {
	if t.On == "manual" {
		return "You start it"
	}
	conn, event, ok := strings.Cut(t.On, ".")
	event = strings.ReplaceAll(event, "_", " ")
	if !ok || event == "" {
		return "When " + t.On
	}
	return fmt.Sprintf("When %s: %s", conn, event)
}

// wfDoes is what a workflow does, in one short phrase, from its own steps:
// the prototype's wfDoes without a description field to prefer (TriggerSpec
// has none) or a connector verb catalog to pull an imperative label from.
func wfDoes(t config.TriggerSpec) string {
	if len(t.Steps) == 0 {
		return "has no steps yet"
	}
	seen := map[string]bool{}
	var did []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			did = append(did, s)
		}
	}
	for _, s := range t.Steps {
		switch stepKind(s) {
		case "agent":
			add("an agent works on it")
		case "code", "command":
			add("runs a command")
		case "handoff":
			add("hands it to a bot")
		case "verb":
			add("calls " + s.Uses)
		case "decide":
			add("decides what happens next")
		case "parallel":
			add("runs steps in parallel")
		case "call":
			add("calls " + s.Workflow)
		}
		if len(did) >= 3 {
			break
		}
	}
	if len(did) == 0 {
		return "runs its steps"
	}
	if len(did) == 1 {
		return did[0]
	}
	return strings.Join(did[:len(did)-1], ", ") + " and " + did[len(did)-1]
}

// FAIL_AT is the failure share at or above which a workflow's week counts as
// "failing" (58b: a workflow's recent health, not whether it's running now).
const failAt = 0.2

// wfHealth summarizes a workflow's last 7 days of runs: how many, the share
// that failed, a health key the Health filter uses, and its last 20 runs'
// status (most recent first) for the row's strip.
func wfHealth(recs []store.RunHistory, now time.Time) (runsWeek, failedWeek int, health string, last20 []string) {
	cutoff := now.Add(-7 * 24 * time.Hour)
	for i, rec := range recs {
		if i < 20 {
			last20 = append(last20, rec.Status)
		}
		if rec.Started.Before(cutoff) {
			continue
		}
		runsWeek++
		if rec.Status == "failed" {
			failedWeek++
		}
	}
	switch {
	case runsWeek == 0:
		health = "none"
	case float64(failedWeek)/float64(runsWeek) >= failAt:
		health = "failing"
	case failedWeek > 0:
		health = "some"
	default:
		health = "healthy"
	}
	return
}

// handleWorkflows serves the Workflows list.
func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	recs := s.deps.Store.ListHistory(0)
	// Tally runs and recent history per workflow name. A run's workflow is
	// its Variant, falling back to Kind (the same projection RunSummary
	// uses). Runs are kept most-recent-first for the health strip.
	type tally struct {
		runs, failed int
		last         time.Time
		lastID       string
		history      []store.RunHistory
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
		t.history = append(t.history, rec)
	}
	now := time.Now()

	out := make([]api.WorkflowSummary, 0, len(s.deps.Cfg.Triggers))
	for _, t := range s.deps.Cfg.Triggers {
		if t.Bot != "" {
			continue // a bot's own workflow lives on the bot's page, not here
		}
		name := wfName(t)
		ws := api.WorkflowSummary{
			Name:     name,
			On:       t.On,
			Steps:    len(t.Steps),
			Disabled: t.Enabled != nil && !*t.Enabled,
			Manual:   wfManual(t),
			Index:    s.rawTriggerIndex(t.Name),
			Source:   wfSource(t),
			When:     wfWhen(t),
			Does:     wfDoes(t),
		}
		if tl := byWF[name]; tl != nil {
			ws.Runs = tl.runs
			ws.Failed = tl.failed
			ws.LastKey = tl.lastID
			if !tl.last.IsZero() {
				ws.LastRun = tl.last.UTC().Format(time.RFC3339Nano)
			}
			ws.RunsWeek, ws.FailedWeek, ws.Health, ws.Last20 = wfHealth(tl.history, now)
		} else {
			ws.Health = "none"
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, api.WorkflowsResponse{Workflows: out})
}

// findWorkflow resolves a workflow name (trigger name first, then `on:`) to
// its spec.
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
		Index:    s.rawTriggerIndex(t.Name),
		Source:   wfSource(t),
		When:     wfWhen(t),
		Does:     wfDoes(t),
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

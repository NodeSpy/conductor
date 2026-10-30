package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// Run detail (design §4.3): one execution in full — the step tree, each step's
// recorded inputs/outputs/diff, and the retry/stop controls. Everything is read
// from the signed history record; `live` comes from the engine (the record
// lags a still-running run).

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok := s.deps.Store.GetHistory(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no recorded run %q", id), "It may be older than the 14 days runs are kept.")
		return
	}
	live := false
	if s.deps.RunActive != nil {
		live = s.deps.RunActive(id)
	}
	resp := api.RunDetailResponse{
		Run:     toSummary(rec),
		Live:    live,
		Trigger: triggerWords(rec),
		Steps:   s.runSteps(rec),
	}
	writeJSON(w, http.StatusOK, resp)
}

// runSteps projects a run's steps onto the detail shape, merging the workflow's
// configured step list (so steps never reached show as idle) with the record's
// executed steps, and classifying each by kind from the config.
func (s *Server) runSteps(rec store.RunHistory) []api.RunStep {
	kinds := map[string]string{}
	var specSteps []config.Step
	if t, ok := s.findWorkflow(workflowOfRun(rec)); ok {
		specSteps = t.Steps
		for i, st := range t.Steps {
			kinds[stepNodeID(st, i)] = stepKind(st)
		}
	}

	byID := map[string]store.StepRecord{}
	for _, sr := range rec.Steps {
		byID[sr.ID] = sr
	}

	project := func(sr store.StepRecord, kind string, idx int) api.RunStep {
		status := sr.Status
		switch {
		case status == "" && !sr.Started.IsZero():
			status = "running"
		case status == "":
			status = "idle"
		}
		rs := api.RunStep{
			ID: sr.ID, Index: idx, Kind: kind, Status: status,
			DurationMS: sr.DurationMS, Error: sr.Error,
			ContinuedOnError: sr.ContinuedOnError,
			Tokens:           sr.Tokens, CostUSD: sr.CostUSD,
			Runtime: sr.Runtime, Model: sr.Model,
		}
		if !sr.Started.IsZero() {
			rs.Started = sr.Started.UTC().Format(time.RFC3339Nano)
		}
		if len(sr.Inputs) > 0 {
			rs.InputsYAML = toYAML(sr.Inputs)
		}
		if len(sr.Outputs) > 0 {
			rs.OutputsYAML = toYAML(sr.Outputs)
			rs.Diff = diffFromOutputs(sr.Outputs)
		}
		if kind == "" {
			rs.Kind = "step"
		}
		return rs
	}

	// When we have the workflow spec, iterate it in order so unreached steps
	// show; otherwise fall back to the recorded steps alone.
	out := make([]api.RunStep, 0, max(len(specSteps), len(rec.Steps)))
	if len(specSteps) > 0 {
		for i, st := range specSteps {
			id := stepNodeID(st, i)
			if sr, ok := byID[id]; ok {
				out = append(out, project(sr, kinds[id], i))
				delete(byID, id)
			} else {
				out = append(out, api.RunStep{ID: id, Index: i, Kind: stepKind(st), Status: "idle"})
			}
		}
		// Any recorded step not in the spec (config changed since the run) is
		// appended so nothing recorded is lost.
		for i, sr := range rec.Steps {
			if _, dropped := byID[sr.ID]; dropped {
				out = append(out, project(sr, "step", len(specSteps)+i))
			}
		}
		return out
	}
	for i, sr := range rec.Steps {
		out = append(out, project(sr, "step", i))
	}
	return out
}

// toYAML renders a scrubbed I/O map as YAML text (the run page's Inputs/Outputs
// panes). Marshal errors degrade to a comment rather than failing the response.
func toYAML(m map[string]any) string {
	b, err := yaml.Marshal(m)
	if err != nil {
		return "# could not render: " + err.Error()
	}
	return strings.TrimRight(string(b), "\n")
}

// diffFromOutputs pulls a unified diff out of a step's outputs when it recorded
// one, under any of the conventional keys. Empty when the step produced none.
func diffFromOutputs(out map[string]any) string {
	for _, k := range []string{"diff", "patch", "unified_diff"} {
		if v, ok := out[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// triggerWords is a short human line for what started a run, from its pinned
// trigger (manual, a schedule, or a connector event on a target).
func triggerWords(rec store.RunHistory) string {
	// A manual run has no real event target — conductor keys it internally as
	// "manual:<name>#<hash>", which is not a repo to show. Only surface a target
	// that is a genuine connector target.
	target := ""
	if rec.Repo != "" && !strings.HasPrefix(rec.Repo, "manual:") {
		target = rec.Repo
		if rec.Number > 0 {
			target = fmt.Sprintf("%s#%d", rec.Repo, rec.Number)
		}
	}
	on := rec.On
	switch {
	case on == "manual" || on == "":
		if target != "" {
			return "Started by hand on " + target
		}
		return "Started by hand"
	case strings.Contains(on, "schedule") || strings.Contains(on, "cron"):
		return "On its schedule"
	default:
		if target != "" {
			return fmt.Sprintf("%s on %s", on, target)
		}
		return on
	}
}

// handleRetry re-runs a recorded execution from a step (reuses the engine's
// RetryRunByID — the crash-resume machinery invoked deliberately).
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	if s.deps.Retry == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "retry is not available on this daemon", "")
		return
	}
	id := r.PathValue("id")
	var req api.RetryRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	// A plain retry is the session's: it never repeats what a step already
	// did. A forced one re-runs steps that succeeded, replaying their side
	// effects (a posted comment, a push), so with sign-in on it needs a
	// step-up bound to the run and the starting step, described with the
	// steps it replays.
	if req.Force && s.authEnforced() {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		digest := retryDigest(id, req.From)
		if _, err := s.auth.RequireStepup(sess, auth.PurposeRunReplay, digest, r.Header.Get(auth.StepupHeader)); err != nil {
			desc := s.retryDescription(id, req.From)
			s.labels.note(r, auth.PurposeRunReplay, digest, desc)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":  map[string]string{"code": "stepup_required", "message": err.Error(), "hint": "confirm it's you to re-run what already ran"},
				"stepup": map[string]string{"purpose": string(auth.PurposeRunReplay), "digest": digest, "description": desc},
			})
			return
		}
	}
	msg, err := s.deps.Retry(r.Context(), id, req.From, req.Force)
	if err != nil {
		writeError(w, http.StatusBadRequest, "retry_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, api.ActionResponse{Message: msg, RunID: retryRunID(msg)})
}

// retryDigest binds a forced retry's step-up to the run and the step it
// starts from.
func retryDigest(id, from string) string {
	b, _ := json.Marshal(map[string]any{"v": "1", "ns": "conductor-dashboard-retry", "run": id, "from": from, "force": true})
	return auth.DigestOf(b)
}

// retryDescription says what a forced retry of run id from step from does:
// the run, where it starts, and each step that already succeeded and runs
// again. The start is resolved as the engine resolves it (the step named,
// else where the run failed, else the beginning).
func (s *Server) retryDescription(id, from string) string {
	rec, ok := s.deps.Store.GetHistory(id)
	if !ok {
		return auth.SafeText("re-run run " + id + " with --force-replay (no record of it here)")
	}
	start, startIdx := from, 0
	if start == "" {
		start = rec.FailedStep
	}
	where := "the beginning"
	if st, ok := rec.Step(start); ok {
		startIdx, where = st.Index, "step "+start
	}
	kinds := map[string]string{}
	for _, st := range s.runSteps(rec) {
		kinds[st.ID] = st.Kind
	}
	var replay []string
	for _, st := range rec.Steps {
		if st.Index >= startIdx && st.Status == "ok" {
			if k := kinds[st.ID]; k != "" && k != "step" {
				replay = append(replay, st.ID+" ("+k+")")
			} else {
				replay = append(replay, st.ID)
			}
		}
	}
	lines := []string{"re-run run " + id + " (" + triggerWords(rec) + ") from " + where + ", forcing a replay"}
	if len(replay) == 0 {
		lines = append(lines, "no step that already succeeded is in range; any that did would run again")
	} else {
		lines = append(lines, "these steps already succeeded and run again, repeating what they did (a posted comment, a push, a command):")
		for _, st := range replay {
			lines = append(lines, "  - "+st)
		}
	}
	return strings.Join(auth.SafeLines(strings.Join(lines, "\n")), "\n")
}

// retryRunID best-effort extracts a new run id (r-xxxx / a flow id) from the
// engine's acknowledgment so the client can navigate to the retry.
func retryRunID(msg string) string {
	for _, tok := range strings.Fields(msg) {
		tok = strings.Trim(tok, ".,:;()[]")
		if strings.HasPrefix(tok, "r-") || strings.HasPrefix(tok, "flow:") {
			return tok
		}
	}
	return ""
}

// handleStop cancels a live run (design §4.3). It stops mid-step; anything
// already committed stays, and the run stays retryable from any step.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stop == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "stop is not available on this daemon", "")
		return
	}
	id := r.PathValue("id")
	if err := s.deps.Stop(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, "stop_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, api.ActionResponse{Message: fmt.Sprintf("Stopping %s — it will stop at its next checkpoint.", id)})
}

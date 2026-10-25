package dashboard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/configedit"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"gopkg.in/yaml.v3"
)

// This file implements the config-edit pipeline endpoints (design §5.3) and the
// per-screen section reads (§5.4). Every mutation goes through one
// configedit.Pipeline: the Config, Secrets, Packs, Plugins and System screens
// all stage path edits and apply them through the identical
// backup→validate-on-copy→diff→apply→reload gate.

// editAvailable writes a 409 when the dashboard is read-only (no ConfigPath).
func (s *Server) editAvailable(w http.ResponseWriter) bool {
	if s.edit == nil {
		writeError(w, http.StatusConflict, "read_only", "this dashboard was started without a config path; editing is unavailable", "")
		return false
	}
	return true
}

// handleConfig serves the live config file: text, sha, a generic tree, the
// flattened scalar leaves and the staged edits.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	path := s.deps.ConfigPath
	if path == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_config", err.Error(), "")
		return
	}
	var tree any
	_ = yaml.Unmarshal(b, &tree)
	tree = normalizeYAML(tree)

	resp := api.ConfigResponse{
		Path:   path,
		SHA:    s.deps.ConfigSHA,
		Text:   string(b),
		Tree:   tree,
		Leaves: flattenLeaves(tree),
		Staged: stagedDTO(s.edit),
	}
	writeJSON(w, http.StatusOK, resp)
}

func stagedDTO(p *configedit.Pipeline) []api.ConfigChange {
	if p == nil {
		return nil
	}
	var out []api.ConfigChange
	for _, c := range p.Staged() {
		out = append(out, api.ConfigChange{
			ID: c.ID, Path: c.Path, Value: c.Value, Delete: c.Delete,
			Summary: c.Summary, Section: c.Section,
		})
	}
	return out
}

// handleStage stages one edit and returns the updated staged set.
func (s *Server) handleStage(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	var req api.StageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Path) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "a change needs a path", "")
		return
	}
	if _, err := s.edit.Stage(configedit.Change{
		Path: req.Path, Value: req.Value, Delete: req.Delete, Summary: req.Summary,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "stage", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"staged": stagedDTO(s.edit)})
}

func (s *Server) handleUnstage(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	s.edit.Unstage(req.ID)
	writeJSON(w, http.StatusOK, map[string]any{"staged": stagedDTO(s.edit)})
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	s.edit.Clear()
	writeJSON(w, http.StatusOK, map[string]any{"staged": []api.ConfigChange{}})
}

// handleReview renders the diff, effects and widening list for the staged set
// and mints a confirm token.
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	rev, err := s.edit.Review()
	if err != nil {
		writeError(w, http.StatusBadRequest, "review", err.Error(), "the staged edits do not apply cleanly to the current config")
		return
	}
	writeJSON(w, http.StatusOK, reviewDTO(rev))
}

func reviewDTO(rev configedit.Review) api.ReviewResponse {
	out := api.ReviewResponse{
		Changed: rev.Diff.Changed, Added: rev.Diff.Added, Removed: rev.Diff.Removed,
		Unified: rev.Diff.Unified, SHA: rev.SHA, Token: rev.Token,
	}
	for _, l := range rev.Diff.Lines {
		out.Lines = append(out.Lines, api.DiffLineDTO{Op: string(l.Op), Text: l.Text, OldLine: l.OldLine, NewLine: l.NewLine})
	}
	for _, e := range rev.Effects {
		out.Effects = append(out.Effects, api.EffectDTO{Path: e.Path, Text: e.Text, Widens: e.Widens})
	}
	for _, e := range rev.Widening {
		out.Widening = append(out.Widening, api.EffectDTO{Path: e.Path, Text: e.Text, Widens: e.Widens})
	}
	return out
}

// handleValidate streams the validate-on-copy result as SSE: a `start` event,
// then an `ok` or `error` event. The dashboard shows this as the candidate runs
// through the daemon's own load+semantic pass.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if s.edit == nil {
		writeError(w, http.StatusConflict, "read_only", "editing is unavailable", "")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "streaming is not supported", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(ev map[string]any) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	emit(map[string]any{"type": "start", "ts": time.Now().UTC().Format(time.RFC3339)})

	done := make(chan error, 1)
	go func() { done <- s.edit.Validate() }()

	select {
	case <-r.Context().Done():
		return
	case err := <-done:
		if err != nil {
			emit(map[string]any{"type": "error", "message": err.Error()})
		} else {
			emit(map[string]any{"type": "ok", "message": "the candidate config validates"})
		}
	}
}

// handleApply applies the staged set after a step-up confirm (the token from the
// matching review).
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	var req api.ApplyRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "no_confirm", "apply needs the confirm token from the review (step-up)", "call GET /api/v1/config/review first")
		return
	}
	applied, err := s.edit.Apply(req.Token)
	if err != nil {
		if errors.Is(err, configedit.ErrStale) {
			writeError(w, http.StatusConflict, "stale", err.Error(), "review the change again")
			return
		}
		writeError(w, http.StatusBadRequest, "apply", err.Error(), "")
		return
	}
	s.deps.Store.Audit(map[string]any{"event": "dashboard.config.apply", "sha": applied.SHA, "backup": applied.BackupID})
	writeJSON(w, http.StatusOK, appliedDTO(applied))
}

func appliedDTO(a configedit.Applied) api.AppliedResponse {
	return api.AppliedResponse{SHA: a.SHA, BackupID: a.BackupID, ReloadError: a.ReloadError, Unchanged: a.Unchanged}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	hist, err := s.edit.History()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history", err.Error(), "")
		return
	}
	out := make([]api.HistoryItem, 0, len(hist))
	for _, h := range hist {
		out = append(out, api.HistoryItem{ID: h.ID, TS: h.TS, SHA: h.SHA, Bytes: h.Bytes})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

func (s *Server) handleRestoreReview(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	var req api.RestoreRequest
	if !decodeBody(w, r, &req) {
		return
	}
	rev, _, err := s.edit.RestoreReview(req.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "restore_review", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, reviewDTO(rev))
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if !s.editAvailable(w) {
		return
	}
	var req api.RestoreRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "no_confirm", "restore needs the confirm token from the restore review (step-up)", "")
		return
	}
	applied, err := s.edit.Restore(req.ID, req.Token)
	if err != nil {
		if errors.Is(err, configedit.ErrStale) {
			writeError(w, http.StatusConflict, "stale", err.Error(), "")
			return
		}
		writeError(w, http.StatusBadRequest, "restore", err.Error(), "")
		return
	}
	s.deps.Store.Audit(map[string]any{"event": "dashboard.config.restore", "from": req.ID, "sha": applied.SHA})
	writeJSON(w, http.StatusOK, appliedDTO(applied))
}

// --- helpers ---

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "could not parse the request body: "+err.Error(), "")
		return false
	}
	return true
}

// normalizeYAML converts yaml's map[string]interface{} / map[interface{}]... and
// nested values into JSON-encodable maps (map[string]any) so the tree serializes.
func normalizeYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normalizeYAML(val)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprintf("%v", k)] = normalizeYAML(val)
		}
		return m
	case []any:
		for i := range x {
			x[i] = normalizeYAML(x[i])
		}
		return x
	default:
		return v
	}
}

// flattenLeaves walks the tree and returns every scalar value with its path.
func flattenLeaves(tree any) []api.ConfigLeaf {
	var out []api.ConfigLeaf
	var walk func(prefix []string, v any)
	walk = func(prefix []string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(append(append([]string{}, prefix...), k), x[k])
			}
		case []any:
			for i, item := range x {
				walk(append(append([]string{}, prefix...), fmt.Sprintf("%d", i)), item)
			}
		default:
			if len(prefix) == 0 {
				return
			}
			out = append(out, api.ConfigLeaf{
				Path: prefix, Section: prefix[0], Kind: kindOf(v), Value: v,
			})
		}
	}
	walk(nil, tree)
	return out
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int, int64:
		return "int"
	case float64, float32:
		return "float"
	default:
		return "string"
	}
}

// liveTree decodes the live config into a generic tree for the section reads.
func (s *Server) liveTree() map[string]any {
	b, err := os.ReadFile(s.deps.ConfigPath)
	if err != nil {
		return map[string]any{}
	}
	var tree any
	_ = yaml.Unmarshal(b, &tree)
	m, _ := normalizeYAML(tree).(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// scanRefs collects, for a secret ref name, the dotted config paths that contain
// "${settings.<name>}" or a "<vault>/<key>" style reference. Used for "used by".
func scanRefs(tree any, needle string) []string {
	var out []string
	var walk func(prefix []string, v any)
	walk = func(prefix []string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				walk(append(append([]string{}, prefix...), k), val)
			}
		case []any:
			for i, item := range x {
				walk(append(append([]string{}, prefix...), fmt.Sprintf("%d", i)), item)
			}
		case string:
			if needle != "" && strings.Contains(x, needle) {
				out = append(out, strings.Join(prefix, "."))
			}
		}
	}
	walk(nil, tree)
	sort.Strings(out)
	return out
}

// readAuditTail returns the last n entries of the audit JSONL file, newest first.
func readAuditTail(path string, n int) []map[string]any {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			all = append(all, e)
		}
	}
	// newest first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

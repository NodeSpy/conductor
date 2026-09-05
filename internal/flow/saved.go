package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// The saved-workflow registry: agent-promoted reusable workflows
// (workflow.save, #36 §11). A saved workflow is a durable §4 workflow —
// self-describing (name + description + steps), versioned with provenance,
// resolvable by dynamic `workflow:` names and workflow.run alongside the
// config's `workflows:` section (config names win on collision, and saving
// over one is rejected).
//
// Trust is earned, not assumed: a newly saved (or newly revised) workflow is
// UNREVIEWED — it dry-runs freely but refuses a real run until reviewed
// (`conductor workflows review <name>`, or trust: full). Conductor tracks
// each saved workflow's success rate; a rotting one (≥3 runs, <50% success)
// is flagged and deprioritized in the catalog so Choose stops picking it.

// SavedWorkflow is one promoted workflow with its lifecycle metadata.
type SavedWorkflow struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Steps       []config.Step `json:"steps"`
	Version     int           `json:"version"`
	// Provenance: who promoted it, from which run/trigger/repo.
	Source  memory.Source `json:"source,omitempty"`
	Created time.Time     `json:"created"`
	Updated time.Time     `json:"updated"`
	// Reviewed: a human (or trust: full) has cleared it for unattended
	// reuse. Reset on every new version.
	Reviewed bool `json:"reviewed"`
	// Health counters (real, non-shadow runs).
	Successes int `json:"successes"`
	Failures  int `json:"failures"`
}

// Runs returns the total recorded outcomes.
func (w *SavedWorkflow) Runs() int { return w.Successes + w.Failures }

// Rotting reports a workflow whose track record says stop choosing it:
// at least 3 recorded runs with under 50% success.
func (w *SavedWorkflow) Rotting() bool {
	return w.Runs() >= 3 && w.Successes*2 < w.Runs()
}

// Def renders the saved workflow as a §4 WorkflowDef for the runner.
func (w *SavedWorkflow) Def() config.WorkflowDef {
	return config.WorkflowDef{Description: w.Description, Steps: w.Steps}
}

// SavedStore persists the registry as one JSON file in conductor's own data
// dir (beside affinity.json/runs.json). It reloads lazily when the file
// changes on disk, so `conductor workflows review` takes effect on a running
// daemon without a restart.
type SavedStore struct {
	path string

	mu      sync.Mutex
	m       map[string]*SavedWorkflow
	modTime time.Time
	now     func() time.Time
}

// OpenSavedStore loads (or creates) the registry file. An empty path keeps
// it in-memory (tests).
func OpenSavedStore(path string) (*SavedStore, error) {
	s := &SavedStore{path: path, m: map[string]*SavedWorkflow{}, now: time.Now}
	if path == "" {
		return s, nil
	}
	if err := s.reload(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("saved workflows: %s: %w", path, err)
	}
	return s, nil
}

// reload reads the file when it changed on disk. Caller need not hold mu.
func (s *SavedStore) reload() error {
	if s.path == "" {
		return nil
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi.ModTime().Equal(s.modTime) {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	m := map[string]*SavedWorkflow{}
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	s.m, s.modTime = m, fi.ModTime()
	return nil
}

// save persists (caller holds mu).
func (s *SavedStore) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}

// Get resolves one saved workflow (a copy).
func (s *SavedStore) Get(name string) (SavedWorkflow, bool) {
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[name]
	if !ok {
		return SavedWorkflow{}, false
	}
	return *w, true
}

// All returns every saved workflow, sorted by name.
func (s *SavedStore) All() []SavedWorkflow {
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SavedWorkflow, 0, len(s.m))
	for _, w := range s.m {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Save upserts a workflow: a new name starts at version 1; saving over an
// existing one bumps the version and RESETS review (a revised workflow is
// re-earned trust). Provenance records who promoted it.
func (s *SavedStore) Save(name, description string, steps []config.Step, src memory.Source) (SavedWorkflow, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SavedWorkflow{}, fmt.Errorf("workflow.save: name is required")
	}
	if strings.ContainsAny(name, " \t\n{}") {
		return SavedWorkflow{}, fmt.Errorf("workflow.save: bad name %q", name)
	}
	if len(steps) == 0 {
		return SavedWorkflow{}, fmt.Errorf("workflow.save: steps are required")
	}
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	w, exists := s.m[name]
	if !exists {
		w = &SavedWorkflow{Name: name, Created: now}
		s.m[name] = w
	}
	w.Description = description
	w.Steps = steps
	w.Version++
	w.Source = src
	w.Updated = now
	w.Reviewed = false
	w.Successes, w.Failures = 0, 0 // a new version starts a fresh track record
	if err := s.save(); err != nil {
		return SavedWorkflow{}, err
	}
	return *w, nil
}

// Review marks a workflow trusted for unattended reuse.
func (s *SavedStore) Review(name string) error {
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[name]
	if !ok {
		return fmt.Errorf("no saved workflow %q", name)
	}
	w.Reviewed = true
	return s.save()
}

// Delete removes a saved workflow.
func (s *SavedStore) Delete(name string) error {
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[name]; !ok {
		return fmt.Errorf("no saved workflow %q", name)
	}
	delete(s.m, name)
	return s.save()
}

// RecordOutcome tracks a real (non-shadow) run's result for rot detection.
func (s *SavedStore) RecordOutcome(name string, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.m[name]
	if !ok {
		return
	}
	if success {
		w.Successes++
	} else {
		w.Failures++
	}
	_ = s.save()
}

// ---------------------------------------------------------------------------
// The process-wide registry handle (like kv/memory): the daemon configures
// it at boot; the runner, the workflow verbs, and the CLI all read it here.
// ---------------------------------------------------------------------------

var (
	savedMu  sync.RWMutex
	savedReg *SavedStore
)

// ConfigureSavedWorkflows installs the registry (boot, tests).
func ConfigureSavedWorkflows(s *SavedStore) {
	savedMu.Lock()
	defer savedMu.Unlock()
	savedReg = s
}

// SavedWorkflows returns the configured registry (nil when none).
func SavedWorkflows() *SavedStore {
	savedMu.RLock()
	defer savedMu.RUnlock()
	return savedReg
}

// savedWorkflowDef resolves a saved workflow by name for the runner.
func savedWorkflowDef(name string) (config.WorkflowDef, bool) {
	s := SavedWorkflows()
	if s == nil {
		return config.WorkflowDef{}, false
	}
	w, ok := s.Get(name)
	if !ok {
		return config.WorkflowDef{}, false
	}
	return w.Def(), true
}

// savedWorkflowNames lists saved workflow names (sorted).
func savedWorkflowNames() []string {
	s := SavedWorkflows()
	if s == nil {
		return nil
	}
	all := s.All()
	out := make([]string, 0, len(all))
	for _, w := range all {
		out = append(out, w.Name)
	}
	return out
}

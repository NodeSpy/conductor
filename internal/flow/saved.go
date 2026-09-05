package flow

import (
	"sort"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
)

// The saved-workflow registry: agent-promoted reusable workflows
// (workflow.save, #36 §11) live here — durable in conductor's own state,
// versioned with provenance, and resolvable by every runtime workflow lookup
// (dynamic `workflow:` names, workflow.run) alongside the config's
// `workflows:` section. Config-declared names always win on collision.
//
// The registry is a process-wide singleton like the kv/memory registries;
// the daemon configures it at boot (see SavedWorkflows in this package's
// promote step) and tests install an in-memory one.

var (
	savedMu  sync.RWMutex
	savedReg map[string]config.WorkflowDef
)

// setSavedWorkflows installs the resolvable saved-workflow set (boot,
// workflow.save, tests).
func setSavedWorkflows(defs map[string]config.WorkflowDef) {
	savedMu.Lock()
	defer savedMu.Unlock()
	savedReg = defs
}

// savedWorkflowDef resolves a saved workflow by name.
func savedWorkflowDef(name string) (config.WorkflowDef, bool) {
	savedMu.RLock()
	defer savedMu.RUnlock()
	wf, ok := savedReg[name]
	return wf, ok
}

// savedWorkflowNames lists saved workflow names (sorted).
func savedWorkflowNames() []string {
	savedMu.RLock()
	defer savedMu.RUnlock()
	out := make([]string, 0, len(savedReg))
	for n := range savedReg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

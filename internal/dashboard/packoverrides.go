package dashboard

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
)

// Pack overrides (CONFORMANCE "Pack overrides": roles → compatible
// connections, triggers with consent, steps via the builder, YAML second).
// Packs already accept overrides in config — settings, steps, triggers and
// the bind-only roles (connectors/stores/secrets/handoffs) — through the same
// stage/review/apply pipeline every other guided edit uses
// (internal/configedit); this is a read model naming what a pack NEEDS (its
// requires:, read from the vendored manifest) and what it's already bound to
// (the live packs.<name> instance), so the dashboard can offer a picker
// instead of a YAML path.
//
// Left out (named, not silently dropped): Policy, Decide and Models overrides
// (no mock asks for them here); a step's full builder (phase 5's step editor)
// is not wired in — only the two override fields the brief names (runtime,
// model) are guided, anything else needs the YAML-second fallback this
// renders as staged JSON (no raw-YAML editor exists anywhere in this port,
// Config included; see the Config screen's own gap note).

// connectorKindMap groups a config tree's connector instance names by their
// `use:` kind (its base name, before any `@version`) — what a pack's
// requires.connectors role names, and what the dashboard offers as
// candidates to bind a role to, or checks to tell a dormant pack from a
// working one.
func connectorKindMap(tree map[string]any) map[string][]string {
	connByKind := map[string][]string{}
	if m, ok := tree["connectors"].(map[string]any); ok {
		for _, cname := range sortedKeys(m) {
			use := ""
			if e, ok := m[cname].(map[string]any); ok {
				use, _ = e["use"].(string)
			}
			kind, _ := splitUse(use)
			connByKind[kind] = append(connByKind[kind], cname)
		}
	}
	return connByKind
}

func (s *Server) handlePackManifest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tree := s.liveTree()
	instRaw, _ := tree["packs"].(map[string]any)
	inst, _ := instRaw[name].(map[string]any)
	if inst == nil {
		writeError(w, http.StatusNotFound, "not_found", "no pack instance named "+name, "")
		return
	}
	if s.deps.ConfigPath == "" {
		writeError(w, http.StatusConflict, "read_only", "no config path is configured", "")
		return
	}
	dir := filepath.Join(config.PackVendorDir(filepath.Dir(s.deps.ConfigPath)), name)
	man, err := config.LoadPackManifestForLint(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "manifest", "couldn't read this pack's manifest: "+err.Error(), "run conductor init if it hasn't been fetched yet")
		return
	}

	resp := api.PackManifestResponse{Name: name}

	// Roles: requires.connectors/stores/secrets/handoffs, each with what it is
	// currently bound to and the consumer's own connections of a compatible
	// kind (a connector's "kind" is its `use:` base name — splitUse — since a
	// pack's connector requirement names that same base).
	boundConn, _ := inst["connectors"].(map[string]any)
	boundStore, _ := inst["stores"].(map[string]any)
	boundSecret, _ := inst["secrets"].(map[string]any)
	boundHandoff, _ := inst["handoffs"].(map[string]any)

	connByKind := connectorKindMap(tree)
	for _, role := range man.Pack.Requires.ConnectorNames() {
		role := role
		ri := api.PackRoleInfo{Name: role, Kind: "connector"}
		if v, ok := boundConn[role]; ok {
			ri.Bound, _ = v.(string)
		}
		ri.Candidates = connByKind[role]
		resp.Connectors = append(resp.Connectors, ri)
	}
	var storeNames []string
	if m, ok := tree["stores"].(map[string]any); ok {
		storeNames = sortedKeys(m)
	}
	for _, role := range man.Pack.Requires.Stores {
		ri := api.PackRoleInfo{Name: role, Kind: "store", Candidates: storeNames}
		if v, ok := boundStore[role]; ok {
			ri.Bound, _ = v.(string)
		}
		resp.Stores = append(resp.Stores, ri)
	}
	for _, role := range sortedKeys(mapKeysToAny(man.Pack.Requires.Secrets)) {
		ri := api.PackRoleInfo{Name: role, Kind: "secret"}
		if v, ok := boundSecret[role]; ok {
			ri.Bound, _ = v.(string)
		}
		resp.Secrets = append(resp.Secrets, ri)
	}
	for _, role := range man.Pack.Requires.Handoffs {
		ri := api.PackRoleInfo{Name: role, Kind: "handoff"}
		if v, ok := boundHandoff[role]; ok {
			ri.Bound, _ = v.(string)
		}
		resp.Handoffs = append(resp.Handoffs, ri)
	}

	// Steps: every step the pack's own workflows define, as <workflow>/<id>
	// refs, with its current override (if any) deep-merged value.
	boundSteps, _ := inst["steps"].(map[string]any)
	for _, wfName := range sortedKeys(mapKeysToAny(man.Workflows)) {
		wf := man.Workflows[wfName]
		for _, st := range wf.Steps {
			if st.ID == "" {
				continue
			}
			ref := wfName + "/" + st.ID
			si := api.PackStepInfo{Ref: ref}
			if v, ok := boundSteps[ref]; ok {
				if m, ok := v.(map[string]any); ok {
					si.Runtime, _ = m["runtime"].(string)
					si.Model, _ = m["model"].(string)
					si.Overridden = true
				}
			}
			resp.Steps = append(resp.Steps, si)
		}
	}

	// Triggers: every trigger the pack ships disarmed, with whether this
	// instance has armed it (enabled: true with at least one repo — the
	// consent, see config.TriggerArm) and the repos it's scoped to.
	boundTriggers, _ := inst["triggers"].(map[string]any)
	for _, t := range man.Triggers {
		if t.Name == "" {
			continue
		}
		ti := api.PackTriggerInfo{Name: t.Name, On: t.On}
		if v, ok := boundTriggers[t.Name]; ok {
			if m, ok := v.(map[string]any); ok {
				if en, ok := m["enabled"].(bool); ok {
					ti.Armed = en
				}
				if repos, ok := m["repos"].([]any); ok {
					for _, rp := range repos {
						ti.Repos = append(ti.Repos, fmt.Sprintf("%v", rp))
					}
				}
			}
		}
		resp.Triggers = append(resp.Triggers, ti)
	}

	writeJSON(w, http.StatusOK, resp)
}

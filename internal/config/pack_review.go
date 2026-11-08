package config

import "sort"

// PackReview is what adding a pack shows before it's staged: what the pack
// is, what it needs bound (connectors/stores/secrets, by role name — bind-only,
// never shipped resolved), what its steps may do (a skill's verbs and the
// secrets it may ask the broker for), and what it ships disarmed (triggers).
// `conductor pack add` and the dashboard's Packs → Add flow both render this
// same review from the same fetched manifest; neither computes it twice.
type PackReview struct {
	Name        string
	Version     string
	Description string
	// Connectors, Stores and Secrets are the role names requires: declares —
	// bind-only: nothing here is a resolved value, only what has to be
	// pointed somewhere before the pack can run.
	Connectors []string
	Stores     []string
	Secrets    []string
	// Grants are the pack's own steps that carry a skill: policy — "what it
	// may do" — one entry per step that requests verbs or secrets.
	Grants []PackReviewGrant
	// Triggers are what the pack ships, always disarmed until the operator
	// arms one (a widening that needs its own consent, separate from install).
	Triggers []PackReviewTrigger
}

// PackReviewGrant is one step's skill request: a pack step may ask for verbs
// (what it can call) and secrets (what the broker may hand it).
type PackReviewGrant struct {
	Where   string   // workflow/step id
	Verbs   []string
	Secrets []string
}

// PackReviewTrigger is one trigger the pack ships, disarmed, with what it
// fires on.
type PackReviewTrigger struct {
	Name string
	On   string
}

// BuildPackReview derives the install review from a fetched pack manifest —
// the one source of truth for "what does installing this pack add" that
// `conductor pack add`'s text report and the dashboard's review dialog both
// render.
func BuildPackReview(man *PackManifest) PackReview {
	m := man.Pack
	rv := PackReview{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Connectors:  m.Requires.ConnectorNames(),
		Stores:      append([]string(nil), m.Requires.Stores...),
	}
	for name := range m.Requires.Secrets {
		rv.Secrets = append(rv.Secrets, name)
	}
	sort.Strings(rv.Secrets)
	man.WalkPackSteps(func(where string, st *Step) {
		if s := st.Skill; s != nil && (len(s.Verbs) > 0 || len(s.AllowSecrets) > 0) {
			rv.Grants = append(rv.Grants, PackReviewGrant{Where: where, Verbs: s.Verbs, Secrets: s.AllowSecrets})
		}
	})
	for _, tr := range man.Triggers {
		if tr.Name == "" {
			continue
		}
		rv.Triggers = append(rv.Triggers, PackReviewTrigger{Name: tr.Name, On: tr.On})
	}
	return rv
}

// PackConfigBlock is the packs:<instance> value to stage for installing this
// pack at source — just where it comes from and (when the manifest names
// one) the version to pin, the same minimal block `conductor pack add`
// prints ready to paste. Bind-only roles (connectors/stores/secrets) and
// arming a trigger are deliberately left for afterwards, on the pack's own
// page (Pack overrides), through the same stage/review/apply pipeline —
// never written sight-unseen here.
func PackConfigBlock(source string, man *PackManifest) map[string]any {
	block := map[string]any{"source": source}
	if man.Pack.Version != "" {
		block["version"] = man.Pack.Version
	}
	return block
}

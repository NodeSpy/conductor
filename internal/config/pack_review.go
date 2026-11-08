package config

import (
	"fmt"
	"sort"
	"strings"
)

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
	// Workflows and Bots (bot template names) are what it adds, by name;
	// AgentSteps counts the steps that run an agent.
	Workflows  []string
	Bots       []string
	AgentSteps int
}

// PackReviewGrant is one step's skill request: a pack step may ask for verbs
// (what it can call) and secrets (what the broker may hand it).
type PackReviewGrant struct {
	Where   string // workflow/step id
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
	rv.Workflows = sortedNames(man.Workflows)
	rv.Bots = sortedNames(man.Bots)
	man.WalkPackSteps(func(where string, st *Step) {
		if st.Form() == "agent" {
			rv.AgentSteps++
		}
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

// PackRef is how a pack reference an operator typed (or a catalog entry
// installs from) is written into a packs: entry and fetched for review.
type PackRef struct {
	// Key is the packs:<instance> field it is written under: "use" (an
	// official conductor-packs/<name>, an owner/repo/<path> or a local path)
	// or "source" (an explicit git source: git::…, https://…, host.tld/…).
	Key   string
	Value string
	// Fetch is the resolver's source form, what FetchPackForReview takes.
	Fetch string
	// Local is a path on this machine (no version to choose).
	Local bool
	// Official is the official registry (trusted by default).
	Official bool
}

// ParsePackRef classifies a pack reference the way a packs: entry reads it.
func ParsePackRef(ref string) (PackRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PackRef{}, fmt.Errorf("empty pack source")
	}
	if isLocalUsePath(ref) || strings.HasPrefix(ref, "file://") || strings.HasPrefix(ref, "~") {
		return PackRef{Key: "use", Value: ref, Fetch: ref, Local: true}, nil
	}
	first := strings.SplitN(ref, "/", 2)[0]
	if strings.HasPrefix(ref, "git::") || strings.Contains(ref, "://") || strings.Contains(first, ":") || strings.Contains(first, ".") {
		return PackRef{Key: "source", Value: ref, Fetch: ref}, nil
	}
	src, err := packUseSource(ref)
	if err != nil {
		return PackRef{}, err
	}
	return PackRef{Key: "use", Value: ref, Fetch: src, Official: strings.HasPrefix(ref, PacksNamespaceAlias+"/")}, nil
}

// PackConfigBlock is the packs:<instance> value that installs this pack
// from ref, tracking its releases: just where it comes from, the same
// minimal block `conductor pack add` prints ready to paste. Binding its roles
// and arming a trigger are the operator's own additions (the dashboard's Add
// review, or the pack's own page afterwards), through the same
// stage/review/apply pipeline — never written sight-unseen here.
func PackConfigBlock(ref PackRef) map[string]any {
	return map[string]any{ref.Key: ref.Value}
}

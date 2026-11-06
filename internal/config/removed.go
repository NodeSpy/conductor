package config

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// REMOVED FIELDS. Conductor auto-updates, and a config the new binary
// rejects would crash-loop every box that still has it. So a field that is
// removed stays DECODABLE: it is accepted with any value, ignored, never
// written back, and named in a warning. It never fails validation.
//
// `folder:` is one: folders (display-only filing of bots, teams, workflows
// and triggers) were removed. It was accepted on bots, teams, workflows,
// triggers, a packs: entry, a pack instance's workflows.<name> and
// triggers.<name> (object or array form) — and, in a pack manifest, on its
// workflows, triggers and bot templates.

// FolderRemoved is the warning for a `folder:` still in a config.
const FolderRemoved = "folders were removed; this field is ignored and can be deleted"

// `sandbox.prefetch:` is another: conductor no longer downloads packages for
// agents. An agent fetches them itself through the egress proxy.

// PrefetchRemoved is the warning for a `sandbox.prefetch:` still in a config.
const PrefetchRemoved = "prefetch was removed; this field is ignored and can be deleted (agents download packages themselves; public hosts are reachable by default)"

// `sandbox.push.rebase:` is another: conductor's push no longer replays an
// agent's commits onto a branch that moved under it. The push is refused,
// the commits kept, and the step's push output says why — a workflow (a
// pack) decides whether to run the job again on the new head.

// PushRebaseRemoved is the warning for a `sandbox.push.rebase:` still in a
// config.
const PushRebaseRemoved = "rebase was removed; this field is ignored and can be deleted (conductor pushes fast-forward only: a push to a branch that moved is refused, the step's push output reports the commits left unpushed, and the workflow decides what happens next)"

// RemovedField is a removed config key: it decodes from any value and is
// never marshaled (IsZero is always true, and every use is omitempty).
type RemovedField struct{}

// UnmarshalYAML accepts any value and keeps nothing.
func (RemovedField) UnmarshalYAML(*yaml.Node) error { return nil }

// IsZero keeps a removed field out of everything conductor writes.
func (RemovedField) IsZero() bool { return true }

// MarshalYAML writes nothing.
func (RemovedField) MarshalYAML() (any, error) { return nil, nil }

// RemovedFieldWarnings are the removed keys found in the config and its
// packs, one warning each.
func (c *Config) RemovedFieldWarnings() []string { return c.removedWarnings }

// removedFolderWarnings finds every `folder:` key in a config document,
// where conductor used to read one.
func removedFolderWarnings(doc []byte) []string {
	var n yaml.Node
	if yaml.Unmarshal(doc, &n) != nil || len(n.Content) == 0 {
		return nil
	}
	var out []string
	walkConfigFolders(n.Content[0], &out)
	if mapValue(mapValue(n.Content[0], "sandbox"), "prefetch") != nil {
		out = append(out, "sandbox.prefetch: "+PrefetchRemoved)
	}
	if mapValue(mapValue(mapValue(n.Content[0], "sandbox"), "push"), "rebase") != nil {
		out = append(out, "sandbox.push.rebase: "+PushRebaseRemoved)
	}
	sort.Strings(out)
	return out
}

func walkConfigFolders(root *yaml.Node, out *[]string) {
	for _, sec := range []string{"bots", "teams", "workflows"} {
		eachEntry(mapValue(root, sec), func(name string, e *yaml.Node) {
			hasFolder(e, sec+"."+name, out)
		})
	}
	eachItem(mapValue(root, "triggers"), func(label string, e *yaml.Node) {
		hasFolder(e, "triggers"+label, out)
	})
	walkPackFolders(mapValue(root, "packs"), "packs", out)
}

// walkPackFolders checks packs: entries, recursively (a dependency's
// packs:), with their workflows.<name> and triggers.<name> overlays.
func walkPackFolders(packs *yaml.Node, where string, out *[]string) {
	eachEntry(packs, func(name string, p *yaml.Node) {
		at := where + "." + name
		hasFolder(p, at, out)
		eachEntry(mapValue(p, "workflows"), func(w string, e *yaml.Node) {
			hasFolder(e, at+".workflows."+w, out)
		})
		eachEntry(mapValue(p, "triggers"), func(t string, e *yaml.Node) {
			if e.Kind == yaml.SequenceNode {
				for i, it := range e.Content {
					hasFolder(it, fmt.Sprintf("%s.triggers.%s[%d]", at, t, i), out)
				}
				return
			}
			hasFolder(e, at+".triggers."+t, out)
		})
		walkPackFolders(mapValue(p, "packs"), at+".packs", out)
	})
}

// manifestFolderWarnings finds `folder:` in a pack manifest: on its
// workflows, triggers and bot templates.
func manifestFolderWarnings(doc []byte, pack string) []string {
	var n yaml.Node
	if yaml.Unmarshal(doc, &n) != nil || len(n.Content) == 0 {
		return nil
	}
	root := n.Content[0]
	var out []string
	for _, sec := range []string{"workflows", "bots"} {
		eachEntry(mapValue(root, sec), func(name string, e *yaml.Node) {
			hasFolder(e, "pack "+pack+": "+sec+"."+name, &out)
		})
	}
	eachItem(mapValue(root, "triggers"), func(label string, e *yaml.Node) {
		hasFolder(e, "pack "+pack+": triggers"+label, &out)
	})
	sort.Strings(out)
	return out
}

func hasFolder(e *yaml.Node, where string, out *[]string) {
	if mapValue(e, "folder") != nil {
		*out = append(*out, where+".folder: "+FolderRemoved)
	}
}

// mapValue is a mapping's value for key (nil when absent or not a mapping).
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func eachEntry(n *yaml.Node, fn func(string, *yaml.Node)) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		fn(n.Content[i].Value, n.Content[i+1])
	}
}

// eachItem walks a trigger list, labeling each by its name or index.
func eachItem(n *yaml.Node, fn func(string, *yaml.Node)) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.SequenceNode:
		for i, it := range n.Content {
			label := fmt.Sprintf("[%d]", i)
			if v := mapValue(it, "name"); v != nil && v.Value != "" {
				label = "." + v.Value
			}
			fn(label, it)
		}
	case yaml.MappingNode:
		eachEntry(n, func(k string, v *yaml.Node) {
			if v.Kind == yaml.SequenceNode { // the instance-array form
				for i, it := range v.Content {
					fn(fmt.Sprintf(".%s[%d]", k, i), it)
				}
				return
			}
			fn("."+k, v)
		})
	}
}

// MaxRuns is a scope's policy.concurrency.max_runs, 0 when unset.
func (p *Policy) MaxRuns() int {
	if p == nil || p.Concurrency == nil || p.Concurrency.MaxRuns == nil {
		return 0
	}
	return *p.Concurrency.MaxRuns
}

// scopedAgentCapWarnings names each connector or trigger that sets
// policy.concurrency.max_agents or max_agents_per_hour: those are read at
// global scope only. A warning, not a refusal, so a live config that already
// sets them keeps loading.
func (c *Config) scopedAgentCapWarnings() []string {
	ignored := func(p *Policy) []string {
		if p == nil || p.Concurrency == nil {
			return nil
		}
		var keys []string
		if p.Concurrency.MaxAgents != nil {
			keys = append(keys, "max_agents")
		}
		if p.Concurrency.MaxAgentsPerHour != nil {
			keys = append(keys, "max_agents_per_hour")
		}
		return keys
	}
	var out []string
	for name, ref := range c.ConnectorsMap {
		for _, k := range ignored(ref.Policy) {
			out = append(out, fmt.Sprintf("connectors.%s.policy.concurrency.%s: %s", name, k, scopedAgentCapIgnored))
		}
	}
	for i, t := range c.Triggers {
		label := t.Name
		if label == "" {
			label = fmt.Sprintf("[%d] (%s)", i, t.On)
		}
		for _, k := range ignored(t.Policy) {
			out = append(out, fmt.Sprintf("triggers.%s.policy.concurrency.%s: %s", label, k, scopedAgentCapIgnored))
		}
	}
	sort.Strings(out)
	return out
}

const scopedAgentCapIgnored = "ignored — read only in the global policy; use concurrency.max_runs to cap the runs of a connector or trigger"

// emptyNarrowingWarnings name each connector whose `network: []` or
// `allow_env: []` now grants none: before the plugin jail an empty list
// passed everything the plugin declares, and a config written then loads
// unchanged with the opposite meaning.
func (c *Config) emptyNarrowingWarnings() []string {
	var out []string
	for name, ref := range c.ConnectorsMap {
		if ref.Network != nil && len(ref.Network) == 0 {
			out = append(out, fmt.Sprintf("connectors.%s.network: [] grants no network at all (before the plugin jail it granted every destination the plugin declares) — remove the key to grant those", name))
		}
		if ref.AllowEnv != nil && len(ref.AllowEnv) == 0 {
			out = append(out, fmt.Sprintf("connectors.%s.allow_env: [] passes no variables at all (before the plugin jail it passed every one the plugin declares) — remove the key to pass those", name))
		}
	}
	sort.Strings(out)
	return out
}

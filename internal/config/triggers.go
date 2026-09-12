package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Named and qualified triggers (docs/design/runtimes-models-packs.md §5.4,
// §5.5).
//
// A bare event name is not an identity: two connectors can publish the same
// event, and a pack that ships several triggers needs each to be addressable
// so a consumer can override exactly one. So `triggers:` accepts a MAP as well
// as the list it has always been, and the map key IS the trigger's address:
//
//	triggers:
//	  github.pull_request:  { steps: [...] }   # key is source.event -> implies on:
//	  gitlab.merge_request: { steps: [...] }   # a distinct source is a distinct key
//	  review:    { on: github.pull_request, steps: [...] }   # free name + explicit on:
//	  autolabel: { on: github.pull_request, steps: [...] }   # ...so two can share an event
//
// A trigger's value is polymorphic, exactly as `runtimes:` and `model:` are:
// an OBJECT is one trigger, an ARRAY is several INSTANCES that each fire
// independently.
//
//	triggers:
//	  review:
//	    - { repos: [me/app], filters: { labels: [ready] } }
//	    - { repos: [me/api] }
//
// There is no `instances:` keyword, no `extends:`/`abstract:` ceremony, and no
// per-instance name. An instance's identity IS its content — its repos and
// filters are what make it distinct — so its internal handle is derived from
// that content and is stable across a reorder of the array.

// TriggerList is the `triggers:` section: an ordered list of triggers,
// decodable from either the list form or the named-map form above.
type TriggerList []TriggerSpec

// UnmarshalYAML accepts the sequence form (unchanged) and the map form.
func (l *TriggerList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*l = nil
			return nil
		}
		return fmt.Errorf("triggers: must be a list of triggers or a map of named triggers")
	case yaml.SequenceNode:
		var list []TriggerSpec
		if err := n.Decode(&list); err != nil {
			return err
		}
		*l = list
		return nil
	case yaml.MappingNode:
		out, err := decodeTriggerMap(n)
		if err != nil {
			return err
		}
		*l = out
		return nil
	}
	return fmt.Errorf("triggers: must be a list of triggers or a map of named triggers")
}

// decodeTriggerMap decodes the named-map form in document order, so the
// resulting list is deterministic and matches what the operator wrote.
func decodeTriggerMap(n *yaml.Node) (TriggerList, error) {
	var out TriggerList
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("triggers: empty trigger name")
		}
		if seen[key] {
			return nil, fmt.Errorf("triggers: duplicate trigger key %q", key)
		}
		seen[key] = true

		switch val.Kind {
		case yaml.MappingNode:
			t, err := decodeKeyedTrigger(key, val)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		case yaml.SequenceNode:
			insts, err := decodeTriggerInstances(key, val)
			if err != nil {
				return nil, err
			}
			out = append(out, insts...)
		case yaml.ScalarNode:
			if val.Tag == "!!null" {
				t, err := decodeKeyedTrigger(key, &yaml.Node{Kind: yaml.MappingNode})
				if err != nil {
					return nil, err
				}
				out = append(out, t)
				continue
			}
			return nil, fmt.Errorf("triggers.%s: a trigger is a block, or a list of instances", key)
		default:
			return nil, fmt.Errorf("triggers.%s: a trigger is a block, or a list of instances", key)
		}
	}
	return out, nil
}

// decodeKeyedTrigger decodes one map-form entry and applies the key rules:
// the key is the trigger's stable ADDRESS (its name), and a key that reads as
// `source.event` also implies `on:` when the body names none.
func decodeKeyedTrigger(key string, val *yaml.Node) (TriggerSpec, error) {
	var t TriggerSpec
	if err := val.Decode(&t); err != nil {
		return TriggerSpec{}, fmt.Errorf("triggers.%s: %w", key, err)
	}
	if t.Name == "" {
		t.Name = key
	}
	if t.On == "" && len(t.OnSources) == 0 {
		if !IsQualifiedEvent(key) {
			return TriggerSpec{}, fmt.Errorf("triggers.%s: needs `on:` — a trigger key only implies the event when it reads as <source>.<event> (e.g. github.pull_request)", key)
		}
		t.On = key
	}
	return t, nil
}

// decodeTriggerInstances decodes the array form: each element is an
// independent trigger sharing the key's address, distinguished by a handle
// derived from its own CONTENT.
func decodeTriggerInstances(key string, val *yaml.Node) (TriggerList, error) {
	if len(val.Content) == 0 {
		return nil, fmt.Errorf("triggers.%s: an instance list is empty — write the block form, or list at least one instance", key)
	}
	out := make(TriggerList, 0, len(val.Content))
	handles := map[string]bool{}
	for i, item := range val.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("triggers.%s[%d]: an instance is a block (its repos:/filters: are what make it distinct)", key, i)
		}
		t, err := decodeKeyedTrigger(key, item)
		if err != nil {
			return nil, err
		}
		h, err := instanceHandle(item)
		if err != nil {
			return nil, fmt.Errorf("triggers.%s[%d]: %w", key, i, err)
		}
		if handles[h] {
			return nil, fmt.Errorf("triggers.%s[%d]: identical to an earlier instance — an instance's identity is its content (repos/filters), so two identical entries are one trigger written twice", key, i)
		}
		handles[h] = true
		t.Name = InstanceName(t.Name, h)
		out = append(out, t)
	}
	return out, nil
}

// instanceSep separates a trigger's address from its content-derived instance
// handle. It is not a legal character in an author-written trigger name, so an
// instance handle can never collide with one.
const instanceSep = "#"

// InstanceName joins a trigger address and an instance handle.
func InstanceName(addr, handle string) string { return addr + instanceSep + handle }

// SplitInstanceName returns a trigger's address and its instance handle
// ("" when it is not an instance) — for display and for grouping a fanned-out
// trigger's runs back together.
func SplitInstanceName(name string) (addr, handle string) {
	if i := strings.Index(name, instanceSep); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// instanceHandle derives a stable, content-addressed handle for one trigger
// instance.
//
// Identity is CONTENT, not position: the node is decoded into a plain map and
// re-marshalled (yaml.v3 sorts a Go map's keys), so the handle survives a
// reorder of the array and a reordering of keys within an entry. Dedup and
// attempt state key off the trigger name, so this is what keeps a fanned-out
// trigger's state stable when the operator edits the list.
func instanceHandle(n *yaml.Node) (string, error) {
	var body map[string]any
	if err := n.Decode(&body); err != nil {
		return "", err
	}
	canon, err := yaml.Marshal(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])[:8], nil
}

// IsQualifiedEvent reports whether a string reads as `<source>.<event>` — the
// form a trigger key may imply `on:` from. The built-in `manual` source counts
// (it takes no event).
func IsQualifiedEvent(s string) bool {
	if s == ManualSource {
		return true
	}
	src, event, ok := strings.Cut(s, ".")
	if !ok || src == "" || event == "" {
		return false
	}
	// A second dot would be an event name containing one, which no connector
	// publishes — more likely a free name that happens to look qualified.
	return !strings.Contains(event, ".")
}

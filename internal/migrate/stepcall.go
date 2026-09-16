package migrate

// The step `use:` -> `call:` pass.
//
// A step-level `use:` named a WORKFLOW to call. That key now selects the
// code-step ENGINE (`use: cli`, `use: js`, `use: bash` — internal/config
// engines.go), and the workflow call is spelled `call:`. The two readings of
// one key cannot coexist, so every step-level `use:` is rewritten:
//
//	steps: [ { use: review-flow, with: {…} } ]
//	steps: [ { call: review-flow, with: {…} } ]
//
// The rewrite is UNCONDITIONAL — no lookup of the name against the config's
// workflows, no guessing. A step `use:` has only ever had one meaning, so
// there is nothing to disambiguate, and a conditional rewrite would leave
// exactly the configs whose workflow lives in another imported file (the
// common split) silently unmigrated and then broken at load.
//
// `workflow:` is left alone. It is the other, still-valid spelling of the
// same field and rewriting it would churn every config in the wild for a
// synonym.
//
// Like the other standalone passes this is a RAW-NODE rewrite, so comments,
// anchors and formatting survive, and it walks only the places a STEP can
// appear — triggers, workflows, checks, and the parallel/compensate bodies
// nested inside them. A connector's or runtime's `use:` is a different key
// in a different block and is never in scope.

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// applyStepCallPass rewrites every step-level `use:` to `call:`.
func applyStepCallPass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(masked, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, nil
	}

	n := 0
	// triggers:/workflows: are maps of entries that each carry a steps: list;
	// triggers: also takes the list shape (`triggers: [ {…}, {…} ]`).
	for _, block := range []string{"triggers", "workflows"} {
		b := childNode(root, block)
		if b == nil {
			continue
		}
		for _, entry := range entriesOf(b) {
			n += renameStepUse(childNode(entry, "steps"))
		}
	}
	// checks: maps a name to ONE step, with no steps: list around it.
	if b := childNode(root, "checks"); b != nil {
		for _, entry := range entriesOf(b) {
			n += renameOneStepUse(entry)
		}
	}
	if n == 0 {
		return nil, false, nil
	}
	*notes = append(*notes, fmt.Sprintf("rewrote %d step-level `use:` to `call:` (a step's `use:` now selects the code engine)", n))
	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// childNode returns the value under key in a mapping, whatever its kind.
// (The package's own mapValue answers only for MAPPING values, which would
// skip every `steps:` sequence this pass exists to walk.)
func childNode(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// entriesOf returns the entries of a block written either as a map (keyed by
// name) or as a list — both shapes `triggers:` accepts.
func entriesOf(block *yaml.Node) []*yaml.Node {
	switch block.Kind {
	case yaml.MappingNode:
		out := make([]*yaml.Node, 0, len(block.Content)/2)
		for i := 0; i+1 < len(block.Content); i += 2 {
			out = append(out, block.Content[i+1])
		}
		return out
	case yaml.SequenceNode:
		return block.Content
	}
	return nil
}

// renameStepUse rewrites every step in a steps: sequence, returning how many
// keys it renamed.
func renameStepUse(steps *yaml.Node) int {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return 0
	}
	n := 0
	for _, step := range steps.Content {
		n += renameOneStepUse(step)
	}
	return n
}

// renameOneStepUse rewrites one step mapping and the steps nested inside it.
func renameOneStepUse(step *yaml.Node) int {
	if step == nil || step.Kind != yaml.MappingNode {
		return 0
	}
	n := 0
	for i := 0; i+1 < len(step.Content); i += 2 {
		k, v := step.Content[i], step.Content[i+1]
		switch k.Value {
		case "use":
			// Only a scalar is a workflow name. Anything else was never a
			// valid step `use:`, so leave it for the loader to report.
			if v.Kind == yaml.ScalarNode {
				k.Value = "call"
				n++
			}
		case "parallel":
			// `parallel: [ [steps…], [steps…] ]` — a sequence of step lists.
			if v.Kind == yaml.SequenceNode {
				for _, branch := range v.Content {
					n += renameStepUse(branch)
				}
			}
		case "compensate":
			n += renameOneStepUse(v)
		}
	}
	return n
}

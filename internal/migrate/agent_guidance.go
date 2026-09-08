package migrate

// The agent_guidance pass folds the legacy top-level `agent_guidance:` into the
// canonical `policy.guidance` (the global scope of the guidance cascade). The
// runtime still accepts `agent_guidance:` as a soft alias, so this is a
// convergence rewrite, not a required one: it moves the value under policy: so
// real configs stop carrying the odd top-level field.
//
//	agent_guidance: "…"           -> policy: { guidance: "…" }
//	agent_guidance + policy.guidance already set -> agent_guidance dropped
//	                                                (policy.guidance wins)
//
// Runs standalone on a connectors-schema file (the legacy transform carries
// agent_guidance through unchanged; a later migrate run converges it).

import "gopkg.in/yaml.v3"

// applyAgentGuidancePass rewrites one (env-masked) YAML document, moving a
// top-level agent_guidance into policy.guidance. Returns rewritten bytes when
// anything changed.
func applyAgentGuidancePass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
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

	// Locate the top-level agent_guidance key/value.
	agIdx := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "agent_guidance" {
			agIdx = i
			break
		}
	}
	if agIdx < 0 {
		return nil, false, nil
	}
	agVal := root.Content[agIdx+1]

	policy := mapValue(root, "policy")
	policyHasGuidance := false
	if policy != nil {
		for i := 0; i+1 < len(policy.Content); i += 2 {
			if policy.Content[i].Value == "guidance" {
				policyHasGuidance = true
				break
			}
		}
	}

	// Remove the top-level agent_guidance entry.
	root.Content = append(root.Content[:agIdx], root.Content[agIdx+2:]...)

	switch {
	case policyHasGuidance:
		// policy.guidance already wins at runtime — agent_guidance was dead.
		*notes = append(*notes, "dropped redundant top-level agent_guidance (policy.guidance already set)")
	default:
		guidanceKey := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "guidance"}
		if policy == nil {
			policy = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			policy.Content = append(policy.Content, guidanceKey, agVal)
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "policy"}, policy)
		} else {
			policy.Content = append(policy.Content, guidanceKey, agVal)
		}
		*notes = append(*notes, "moved top-level agent_guidance -> policy.guidance (global scope)")
	}

	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

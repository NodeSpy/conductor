package flow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/connector"
)

// The capability card (docs/design/skill-capability-and-pack-interface.md §A).
//
// A workflow prompt used to hand-code conductor's own CLI mechanics — run
// `conductor discover`, then `conductor call gh.submit_review --comments
// '<json>'`. That duplicated the transport into every prompt and meant a verb
// signature change silently broke all of them. Conductor teaches the agent
// instead, and prompts drop to intent.
//
// Two layers, and BOTH RIDE THE GRANT — an agent with no skill grant is told
// nothing, because it can do nothing (deny-by-default, §B):
//
//	Layer 0  general mechanics: act through verbs, how to discover, the call form.
//	Layer 1  the capability card: the granted verbs, rendered from the registry.
//
// The card is rendered from GrantedVerbs — the same resolution that produces
// the MCP tool list, `conductor discover`, and (through the same matchAny
// over the same patterns) what RunSkillVerb enforces. One source, so the card
// an agent reads can never promise a verb the daemon would refuse.

// CapabilityPreamble is Layer 0: the general "you can act through conductor"
// guidance, injected whenever a step has a non-empty grant. It is GENERATED,
// never authored, so it stays correct as the CLI evolves.
//
// It is deliberately transport-specific in one respect only: an MCP runtime
// already has the verbs as native tools, so telling it to shell `conductor
// call` would be wrong.
func CapabilityPreamble(cli bool) string {
	var b strings.Builder
	b.WriteString("You can act on external systems through CONDUCTOR VERBS — do that rather than shelling out to git, gh, curl, or a vendor CLI for anything a verb covers. ")
	b.WriteString("A verb runs server-side with conductor's own credentials, so no secret ever enters this session, and every call is audited.")
	if cli {
		b.WriteString(" Run `conductor discover` to see the verbs available to you (`conductor discover <connector>` to narrow, `conductor discover <connector.verb>` for one verb's options), and call them as `conductor call <connector.verb> --<option> <value>`.")
	} else {
		b.WriteString(" They are attached to this session as tools — call them directly; their schemas list the options.")
	}
	return b.String()
}

// CapabilityCard is Layer 1: the granted verbs with their options and the
// exact call form, rendered from the verb registry and scoped to exactly the
// grant. Empty when the grant admits nothing — an agent is never handed a
// heading with no verbs under it.
//
// Only the CLI transport needs this: an MCP runtime receives the identical
// registry entries as native tool schemas (SkillVerbCatalog), so repeating
// them as prompt text would be redundant tokens.
func (r *Runner) CapabilityCard(patterns []string) string {
	granted := r.GrantedVerbs(patterns)
	if len(granted) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Conductor verbs available to you\n")
	b.WriteString("Act through these — do not shell out to git/gh/network for what they cover.\n")
	b.WriteString("Call as: conductor call <verb> --<option> <value>\n")
	for _, g := range granted {
		b.WriteString("\n• ")
		b.WriteString(g.Uses)
		if d := firstLine(g.Description()); d != "" {
			b.WriteString(" — ")
			b.WriteString(d)
		}
		b.WriteString("\n")
		if opts := renderVerbOptions(g.Decl); opts != "" {
			b.WriteString(opts)
		}
	}
	return b.String()
}

// renderVerbOptions renders one verb's options, required first then optional,
// each as the flag an agent would actually type.
func renderVerbOptions(vd connector.VerbDecl) string {
	if len(vd.Options) == 0 {
		if vd.Open {
			return "    (options are free-form for this verb)\n"
		}
		return ""
	}
	names := make([]string, 0, len(vd.Options))
	for n := range vd.Options {
		names = append(names, n)
	}
	sort.Strings(names)
	// Required options first: they are what an agent must supply to make the
	// call at all.
	sort.SliceStable(names, func(i, j int) bool {
		return vd.Options[names[i]].Required && !vd.Options[names[j]].Required
	})

	var b strings.Builder
	for _, n := range names {
		f := vd.Options[n]
		b.WriteString("    --")
		b.WriteString(n)
		b.WriteString(" ")
		b.WriteString(optionTypeHint(f))
		if f.Required {
			b.WriteString(" (required)")
		}
		if d := firstLine(f.Desc); d != "" {
			b.WriteString("  ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	if vd.Open {
		b.WriteString("    (additional free-form options accepted)\n")
	}
	return b.String()
}

// optionTypeHint renders an option's accepted shape: its enum when it has
// one (far more useful than "string"), else its type.
func optionTypeHint(f connector.Field) string {
	if len(f.Enum) > 0 {
		return strings.Join(f.Enum, "|")
	}
	switch f.Type {
	case connector.TInt:
		return "integer"
	case connector.TBool:
		return "true|false"
	case connector.TFloat:
		return "number"
	case connector.TList:
		return "list"
	case connector.TMap:
		return "json"
	default:
		return "string"
	}
}

// GrantSummary is a one-line human description of what a grant admits, for
// logs and `conductor validate` output.
func (r *Runner) GrantSummary(patterns []string) string {
	granted := r.GrantedVerbs(patterns)
	if len(granted) == 0 {
		return "no verbs"
	}
	conns := map[string]int{}
	for _, g := range granted {
		conns[g.Connector]++
	}
	names := make([]string, 0, len(conns))
	for c := range conns {
		names = append(names, c)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, c := range names {
		parts = append(parts, fmt.Sprintf("%s (%d)", c, conns[c]))
	}
	return fmt.Sprintf("%d verb(s) across %s", len(granted), strings.Join(parts, ", "))
}

// firstLine trims a description to its first line, so a multi-paragraph verb
// doc does not blow up the card.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

package connector

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// validateInstanceRefinement enforces the Q6 refinement rule
// (plugin-contract.md §1.4, §3.9 G13, Q6): a per-instance declaration
// (plugin.describe {instance, config}) may vary what the type-level
// declaration said about EVENTS — that is the entire point of Q6 (webhook
// materializes one concrete, statically-known target.assigned per
// configured source; rest/graphql materialize their user-declared events).
// Everything install-time review actually looked at — a verb's semantics,
// the connection-level semantics, and the capability manifest — must come
// back either absent (a verb/field the type decl never named) or
// byte-for-byte identical to what the type decl already said.
//
// Without this check, resolveInstanceDecl's instance decl wholly REPLACED
// the type decl: a type-level host_only mints_credential verb could be
// redeclared, per instance, as an ordinary verb with no semantics at all —
// then a flow step could call it directly and receive the minted credential
// as a step output. verbs present in both keep identical semantics; an
// instance may not introduce a verb semantic, a connection semantic, or a
// capability the type decl lacks or differs on.
func validateInstanceRefinement(typeDecl, instanceDecl *sdk.Decl) error {
	var problems []string

	typeVerbs := make(map[string]*sdk.VerbSemantics, len(typeDecl.Verbs))
	for _, v := range typeDecl.Verbs {
		typeVerbs[v.Name] = v.Semantics
	}
	for _, v := range instanceDecl.Verbs {
		ts, known := typeVerbs[v.Name]
		switch {
		case known && !reflect.DeepEqual(ts, v.Semantics):
			problems = append(problems, fmt.Sprintf(
				"verb %q: the instance declaration's semantics (%s) differ from the type-level declaration's (%s)",
				v.Name, describeVerbSemantics(v.Semantics), describeVerbSemantics(ts)))
		case !known && v.Semantics != nil:
			problems = append(problems, fmt.Sprintf(
				"verb %q: declares semantics (%s) the type-level declaration never granted to any verb",
				v.Name, describeVerbSemantics(v.Semantics)))
		}
	}

	// Connection-level semantics (credentials, listeners, poll, scope,
	// preflight, translate) are fixed at install, not per instance — an
	// instance decl is held to the type decl's word on the whole block.
	if !reflect.DeepEqual(typeDecl.Semantics, instanceDecl.Semantics) {
		problems = append(problems, "connection semantics (credentials/listeners/poll/scope/preflight) differ from the type-level declaration")
	}

	// Confinement (egress/commands/fs/spawns) is decided once, at install,
	// from the type-level describe — an instance must never be able to
	// claim a wider manifest than the plugin was reviewed and sandboxed for.
	if !reflect.DeepEqual(typeDecl.Capabilities, instanceDecl.Capabilities) {
		problems = append(problems, "capabilities manifest differs from the type-level declaration")
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("per-instance declaration is not a refinement of the type-level one (plugin-contract.md §1.4 Q6):\n  %s", strings.Join(problems, "\n  "))
}

// describeVerbSemantics renders a *sdk.VerbSemantics for an error message
// (nil-safe) — just the markers a reviewer cares about, not the full struct.
func describeVerbSemantics(s *sdk.VerbSemantics) string {
	if s == nil {
		return "none"
	}
	var parts []string
	if s.HostOnly {
		parts = append(parts, "host_only")
	}
	if s.MintsCredential != nil {
		parts = append(parts, fmt.Sprintf("mints_credential:%s", s.MintsCredential.Credential))
	}
	if s.ReadsRevision != nil {
		parts = append(parts, "reads_revision")
	}
	if s.OpensConversation != nil {
		parts = append(parts, "opens_conversation")
	}
	if s.ConversationPost {
		parts = append(parts, "conversation_post")
	}
	if s.Exposes != nil {
		parts = append(parts, "exposes")
	}
	if len(s.TargetArgs) > 0 {
		parts = append(parts, "target_args")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

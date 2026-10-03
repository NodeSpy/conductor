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
// Everything install-time review actually looked at — a verb's semantics
// AND its security-relevant shape (an option's scope, whether it is Open,
// its output schema), the connection-level semantics, and the capability
// manifest — must come back either absent (a verb/field the type decl never
// named) or byte-for-byte identical to what the type decl already said, or
// in the two cases called out below, no LOOSER. The allowlist is explicit
// and deliberately narrow (Desc may always differ, and Outputs may only
// gain strictness) so a new field added to the wire format later fails
// closed — refused as a mismatch — rather than silently passing through
// unchecked.
//
// Without this check, resolveInstanceDecl's instance decl wholly REPLACED
// the type decl: a type-level host_only mints_credential verb could be
// redeclared, per instance, as an ordinary verb with no semantics at all —
// then a flow step could call it directly and receive the minted credential
// as a step output (the original finding). Finding 7 extended it: the first
// version compared only a verb's Semantics block, so an instance decl could
// keep Semantics byte-identical while still stripping an option's Scope
// (which gates agent-authored access to that option via ScopedOptions) or
// flipping Open to true (which skips output validation entirely) — neither
// lives inside VerbSemantics, so neither was checked.
func validateInstanceRefinement(typeDecl, instanceDecl *sdk.Decl) error {
	var problems []string

	typeVerbs := make(map[string]*sdk.Verb, len(typeDecl.Verbs))
	for i := range typeDecl.Verbs {
		typeVerbs[typeDecl.Verbs[i].Name] = &typeDecl.Verbs[i]
	}
	for i := range instanceDecl.Verbs {
		v := &instanceDecl.Verbs[i]
		tv, known := typeVerbs[v.Name]
		if !known {
			// A brand-new verb the type decl never named at all: fine, as
			// long as it carries no semantics of its own (rest/graphql's
			// user-declared, plain verbs are exactly this).
			if v.Semantics != nil {
				problems = append(problems, fmt.Sprintf(
					"verb %q: declares semantics (%s) the type-level declaration never granted to any verb",
					v.Name, describeVerbSemantics(v.Semantics)))
			}
			continue
		}
		if !reflect.DeepEqual(tv.Semantics, v.Semantics) {
			problems = append(problems, fmt.Sprintf(
				"verb %q: the instance declaration's semantics (%s) differ from the type-level declaration's (%s)",
				v.Name, describeVerbSemantics(v.Semantics), describeVerbSemantics(tv.Semantics)))
		}
		if tv.Open != v.Open {
			problems = append(problems, fmt.Sprintf(
				"verb %q: open changed from %v to %v (Open skips output validation — this must match the type-level declaration exactly)",
				v.Name, tv.Open, v.Open))
		}
		// An option's Scope gates the VALUE an agent-authored call may put
		// there (ScopedOptions); dropping or changing it on a verb the type
		// decl already scoped would let an instance quietly hand an agent
		// free-form access to a resource dimension install-time review
		// believed was gated.
		for optName, tf := range tv.Options {
			if tf.Scope == "" {
				continue
			}
			vf, ok := v.Options[optName]
			if !ok || vf.Scope != tf.Scope {
				got := "absent"
				if ok {
					got = fmt.Sprintf("%q", vf.Scope)
				}
				problems = append(problems, fmt.Sprintf(
					"verb %q option %q: scope %q dropped or altered (instance: %s)", v.Name, optName, tf.Scope, got))
			}
		}
		// Outputs may only gain strictness: a type-declared output must
		// still be present, with the same type, and at least as required —
		// dropping or loosening one would let an instance's own validation
		// accept a shape the engine's output handling for this verb assumes
		// was checked.
		for outName, tf := range tv.Outputs {
			vf, ok := v.Outputs[outName]
			if !ok {
				problems = append(problems, fmt.Sprintf(
					"verb %q output %q: dropped (loosens output validation)", v.Name, outName))
				continue
			}
			if vf.Type != tf.Type || (tf.Required && !vf.Required) {
				problems = append(problems, fmt.Sprintf(
					"verb %q output %q: loosened (type %q -> %q, required %v -> %v)",
					v.Name, outName, tf.Type, vf.Type, tf.Required, vf.Required))
			}
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

	// Events are the one place an instance decl may say something NEW — Q6's
	// whole point (webhook's one concrete per-source target.assigned event,
	// rest/graphql's user-declared polled events). A brand-new event name
	// (absent from the type decl) is unrestricted. But a SAME-NAMED event —
	// one the type decl already declares — must not ESCALATE what the
	// engine does with it beyond what install-time review already saw:
	// adding (or changing) conversation_reply/closes_target, or widening the
	// target's scope dimensions, past the type-level declaration's own
	// same-named event.
	typeEvents := make(map[string]*sdk.Event, len(typeDecl.Events))
	for i := range typeDecl.Events {
		typeEvents[typeDecl.Events[i].Name] = &typeDecl.Events[i]
	}
	for i := range instanceDecl.Events {
		ev := &instanceDecl.Events[i]
		te, known := typeEvents[ev.Name]
		if !known {
			// A genuinely new event (the legitimate Q6 case) may not
			// declare option_hooks at all: an option_hooks entry invokes a
			// verb — potentially a host_only one — at a run phase, exactly
			// as an operator-authored `hooks:` entry would, so a per-
			// instance decl smuggling one in on an event install-time
			// review never saw is the same class of escalation the verb
			// checks above exist for; unlike a verb's OWN semantics (which
			// a brand-new verb may carry none of), there is no legitimate
			// reason for a brand-new EVENT to come with one.
			if ev.Semantics != nil && len(ev.Semantics.OptionHooks) > 0 {
				problems = append(problems, fmt.Sprintf(
					"event %q: a brand-new event (absent from the type-level declaration) declares option_hooks — not permitted", ev.Name))
			}
			continue
		}
		if msg, ok := eventSemanticsRefine(te.Semantics, ev.Semantics); !ok {
			problems = append(problems, fmt.Sprintf("event %q: %s", ev.Name, msg))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("per-instance declaration is not a refinement of the type-level one (plugin-contract.md §1.4 Q6):\n  %s", strings.Join(problems, "\n  "))
}

// eventSemanticsRefine reports whether a SAME-NAMED event's instance
// semantics (i) escalate beyond what the type-level declaration (t) already
// said for it — conversation_reply, closes_target, and the target's scope
// dimensions are the engine-honored effects a plugin could otherwise smuggle
// in on an event name install-time review already approved. Everything else
// about an event is exempt (Q6) even for a same-named one — only these
// three, specifically, are escalation paths an instance decl is not
// otherwise checked against at all.
func eventSemanticsRefine(t, i *sdk.EventSemantics) (string, bool) {
	var tCR, iCR *sdk.ConversationReply
	if t != nil {
		tCR = t.ConversationReply
	}
	if i != nil {
		iCR = i.ConversationReply
	}
	if !reflect.DeepEqual(tCR, iCR) {
		return "conversation_reply differs from the type-level declaration's same-named event", false
	}

	var tCT, iCT *sdk.ClosesTargetSemantics
	if t != nil {
		tCT = t.ClosesTarget
	}
	if i != nil {
		iCT = i.ClosesTarget
	}
	if !reflect.DeepEqual(tCT, iCT) {
		return "closes_target differs from the type-level declaration's same-named event", false
	}

	var tScope, iScope []sdk.ScopeFact
	if t != nil && t.Target != nil {
		tScope = t.Target.Scope
	}
	if i != nil && i.Target != nil {
		iScope = i.Target.Scope
	}
	if !scopeFactsEqual(tScope, iScope) {
		return "target scope dimensions differ from the type-level declaration's same-named event", false
	}

	// option_hooks (plugin-contract.md §2.2): a per-instance decl can add an
	// option hook invoking a host_only verb to an APPROVED event just as
	// easily as it could smuggle in a conversation_reply/closes_target —
	// same-named events must keep option_hooks IDENTICAL, not merely
	// "no widening": narrowing (silently dropping one the type decl
	// declared) is refused too, since that changes what the operator sees
	// documented for this event out from under them just as surely as
	// adding one does.
	var tOH, iOH []sdk.OptionHook
	if t != nil {
		tOH = t.OptionHooks
	}
	if i != nil {
		iOH = i.OptionHooks
	}
	if !optionHooksEqual(tOH, iOH) {
		return "option_hooks differ from the type-level declaration's same-named event", false
	}
	return "", true
}

// optionHooksEqual compares two OptionHook lists as SETS (the wire gives no
// ordering guarantee, matching scopeFactsEqual's treatment of ScopeFact) —
// byte-for-byte on every field, not just the option name, since a hook that
// renamed its own verb or at-phase while keeping the same Option would still
// be a materially different hook.
func optionHooksEqual(a, b []sdk.OptionHook) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(h sdk.OptionHook) string {
		args := make([]string, 0, len(h.Args))
		for k, v := range h.Args {
			args = append(args, k+"\x00"+v)
		}
		sort.Strings(args)
		return h.Option + "\x01" + h.At + "\x01" + h.Verb + "\x01" + strings.Join(args, "\x02")
	}
	ak := make([]string, len(a))
	for i, h := range a {
		ak[i] = key(h)
	}
	bk := make([]string, len(b))
	for i, h := range b {
		bk[i] = key(h)
	}
	sort.Strings(ak)
	sort.Strings(bk)
	for i := range ak {
		if ak[i] != bk[i] {
			return false
		}
	}
	return true
}

// scopeFactsEqual compares two ScopeFact lists as sets (order-independent —
// the wire gives no ordering guarantee), by Dimension+Fact pair.
func scopeFactsEqual(a, b []sdk.ScopeFact) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(s []sdk.ScopeFact) []string {
		out := make([]string, len(s))
		for i, f := range s {
			out[i] = f.Dimension + "\x00" + f.Fact
		}
		sort.Strings(out)
		return out
	}
	ak, bk := key(a), key(b)
	for i := range ak {
		if ak[i] != bk[i] {
			return false
		}
	}
	return true
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

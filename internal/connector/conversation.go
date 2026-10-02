package connector

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/handoff"
)

// A plugin connector whose verb declares opens_conversation is an ask
// channel like any bundled one (plugin-contract.md §2.3): presenting invokes
// that verb (it posts the question, in the plugin's own medium) and registers
// the conversation it returns in the engine's one inbox; the reply arrives as
// the plugin's conversation_reply event (pluginsource.go) and resolves it.

// conversationVerb is the decl's conversation-opening verb: an ask verb if
// one declares it, else the first that does.
func (d *TypeDecl) conversationVerb() (VerbDecl, bool) {
	var first VerbDecl
	found := false
	for _, v := range d.Verbs {
		if v.Semantics == nil || v.Semantics.OpensConversation == nil {
			continue
		}
		if v.Ask {
			return v, true
		}
		if !found {
			first, found = v, true
		}
	}
	return first, found
}

// AskChannel implements AskChanneler for a plugin declaring a conversation
// verb.
func (e *externalImpl) AskChannel(opts map[string]any) (handoff.Channel, error) {
	v, ok := e.decl.conversationVerb()
	if !ok {
		return nil, fmt.Errorf("connector %q (%s) declares no conversation verb — it cannot present a hand-off", e.instance, e.pluginType)
	}
	return &conversationChannel{e: e, verb: v, opts: opts}, nil
}

type conversationChannel struct {
	e    *externalImpl
	verb VerbDecl
	opts map[string]any
}

func (c *conversationChannel) Present(ctx context.Context, d handoff.Draft) (handoff.Presentation, error) {
	opts := map[string]any{}
	for k, v := range c.opts {
		opts[k] = v
	}
	if _, ok := opts["prompt"]; !ok {
		opts["prompt"] = d.Title
	}
	if _, ok := opts["draft"]; !ok && d.Body != "" {
		opts["draft"] = d.Body
	}
	if _, ok := opts["title"]; !ok && d.Title != "" {
		opts["title"] = d.Title
	}
	out, err := c.e.invokePlugin(ctx, c.verb.Name, opts)
	if err != nil {
		return nil, err
	}
	oc := c.verb.Semantics.OpensConversation
	id := fmt.Sprint(out[oc.ID])
	if out[oc.ID] == nil || id == "" {
		return nil, fmt.Errorf("%s.%s opened no conversation (no %q in its outputs)", c.e.instance, c.verb.Name, oc.ID)
	}
	ref, _ := out["ref"].(string)
	var approvers []string
	if oc.Approvers != "" {
		approvers = stringList(opts[oc.Approvers])
	}
	return handoff.Conversations.OpenConversation(c.e.instance, id, ref, approvers), nil
}

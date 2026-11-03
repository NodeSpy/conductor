package flow

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// WarnInteractiveBotSteps logs (never fails) a boot-time note for every
// `bot:` step wired to a conversational event (a chat/DM message, per its
// declared ConversationReply semantic) that omits `requester:` — rt2 #7: such
// a step is routed Conversational at runtime (internal/bot's classifier), so
// it already cannot inherit event-tier trust from an omitted requester; this
// warning is purely so an operator notices the omission rather than
// wondering why a chat message never auto-runs a promoted verb.
//
// This is a WARNING, not a validation error: an existing config with this
// shape has always loaded and must keep loading (failing it here would
// crash-loop a fleet on upgrade) — the runtime fix already makes it safe,
// this just helps an operator find the step and add `requester:` if they
// want operator-grade requesters tracked (chat: aliases, etc.) rather than
// every such message landing as a third-party `user`.
//
// Only a LITERAL empty/omitted `requester:` is flagged — a templated one
// (`requester: "{{.user}}"`) is the operator's own call and may render
// non-empty at runtime, so it is not second-guessed here.
func WarnInteractiveBotSteps(cfg *config.Config, reg *connector.Registry, warn func(string)) {
	if cfg == nil || reg == nil || warn == nil {
		return
	}
	check := func(where string, steps []config.Step, connName, eventName string) {
		in, ok := reg.Get(connName)
		if !ok || in.Decl == nil {
			return
		}
		ev, ok := in.Decl.Event(eventName)
		if !ok || ev.Semantics == nil || ev.Semantics.ConversationReply == nil {
			return
		}
		for i, st := range steps {
			if st.Bot == nil || st.Bot.Team != "" {
				continue // team routing re-classifies per assignee; same guard applies there too
			}
			if strings.TrimSpace(st.Bot.Requester) != "" {
				continue
			}
			warn(fmt.Sprintf("config: %s.steps[%d]: bot: %q has no requester: and is wired to a conversational event (%s.%s) — "+
				"a chat message with no requester is always treated as a third-party user (never auto-run tier), "+
				"set requester: (e.g. \"{{.user}}\") if you want it attributed",
				where, i, st.Bot.Name, connName, eventName))
		}
	}
	for i, t := range cfg.Triggers {
		if t.Manual() {
			continue
		}
		check(fmt.Sprintf("triggers[%d]", i), t.Steps, t.Connector(), t.Event())
	}
}

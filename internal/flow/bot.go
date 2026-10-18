package flow

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// BotRoute is one `bot:` helper step, rendered: what the bot
// service routes (internal/bot.Service).
type BotRoute struct {
	Bot, Action, Title, Text, Requester string
	Urgent                              bool
	// Team routes to a team instead of Bot (its routing: picks the
	// bot); Fields are the event's own fields, for its routing rules.
	Team   string
	Fields map[string]any
	// Source is the event the run is for ("<instance>.<kind>"), stamped by
	// the runner from the trigger — never from the step's options.
	Source string
	// Files fetches the files the event carries (its connector's declared
	// `attachments`), within the receiving bot's caps; the bot service calls
	// it once it knows which bot that is. nil: the event carries none.
	Files func(ctx context.Context, maxFiles int, maxFile int64) ([]connector.File, error)
}

// BotRouter is the daemon's bot service as the flow runner sees it.
type BotRouter interface {
	RouteBot(ctx context.Context, r BotRoute) (map[string]any, error)
}

// execBot runs the `bot:` helper. An agent-authored plan cannot route
// into a bot: bots are isolated from one another, and an agent's plan
// is exactly how one bot's session could push work into another's.
func (r *Runner) execBot(ctx context.Context, t core.Trigger, step config.Step, data map[string]any, shadow bool) (map[string]any, string, error) {
	if agentAuthored(ctx) {
		return nil, "", fmt.Errorf("bot: an agent-authored step cannot route into a bot")
	}
	ms := step.Bot
	render1 := func(field, s string) (string, error) {
		out, err := render(s, data)
		if err != nil {
			return "", fmt.Errorf("bot.%s: %w", field, err)
		}
		return out, nil
	}
	rt := BotRoute{Action: ms.Action, Urgent: ms.Urgent, Source: t.Instance + "." + t.Kind}
	if rt.Action == "" {
		rt.Action = "deliver"
	}
	var err error
	if rt.Bot, err = render1("name", ms.Name); err != nil {
		return nil, "", err
	}
	if rt.Team, err = render1("team", ms.Team); err != nil {
		return nil, "", err
	}
	if rt.Team != "" {
		rt.Fields = data
	}
	if rt.Title, err = render1("title", ms.Title); err != nil {
		return nil, "", err
	}
	if rt.Text, err = render1("text", ms.Text); err != nil {
		return nil, "", err
	}
	if rt.Requester, err = render1("requester", ms.Requester); err != nil {
		return nil, "", err
	}
	if t.Source == config.ManualSource {
		rt.Source = "manual." + t.Variant
	}
	if connector.EventFileCount(t) > 0 && r.Conns != nil {
		if in, ok := r.Conns.Get(t.Instance); ok {
			rt.Files = func(ctx context.Context, maxFiles int, maxFile int64) ([]connector.File, error) {
				return in.FetchAttachments(ctx, t, maxFiles, maxFile)
			}
		}
	}
	if shadow {
		if rt.Team != "" {
			r.Log("%s [dry-run] would route to team %s", flowTag(t), rt.Team)
			return map[string]any{"stubbed": true}, "", nil
		}
		r.Log("%s [dry-run] would %s bot %s", flowTag(t), rt.Action, rt.Bot)
		return map[string]any{"stubbed": true}, "", nil
	}
	if r.BotRouter == nil {
		return nil, "", fmt.Errorf("bot: no bot service on this daemon")
	}
	out, err := r.BotRouter.RouteBot(ctx, rt)
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	r.audit(map[string]any{"event": "bot_route", "bot": rt.Bot, "team": rt.Team, "action": rt.Action, "source": rt.Source, "urgent": rt.Urgent, "outcome": outcome})
	if err != nil {
		return nil, "", fmt.Errorf("bot: %w", err)
	}
	return out, "", nil
}

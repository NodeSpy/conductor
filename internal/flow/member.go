package flow

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// MemberRoute is one `member:` helper step, rendered: what the member
// service routes (internal/member.Service).
type MemberRoute struct {
	Member, Action, Title, Text, Requester string
	Urgent                                 bool
	// Team routes to a team instead of Member (its routing: picks the
	// member); Fields are the event's own fields, for its routing rules.
	Team   string
	Fields map[string]any
	// Source is the event the run is for ("<instance>.<kind>"), stamped by
	// the runner from the trigger — never from the step's options.
	Source string
}

// MemberRouter is the daemon's member service as the flow runner sees it.
type MemberRouter interface {
	RouteMember(ctx context.Context, r MemberRoute) (map[string]any, error)
}

// execMember runs the `member:` helper. An agent-authored plan cannot route
// into a member: members are isolated from one another, and an agent's plan
// is exactly how one member's session could push work into another's.
func (r *Runner) execMember(ctx context.Context, t core.Trigger, step config.Step, data map[string]any, shadow bool) (map[string]any, string, error) {
	if agentAuthored(ctx) {
		return nil, "", fmt.Errorf("member: an agent-authored step cannot route into a member")
	}
	ms := step.Member
	render1 := func(field, s string) (string, error) {
		out, err := render(s, data)
		if err != nil {
			return "", fmt.Errorf("member.%s: %w", field, err)
		}
		return out, nil
	}
	rt := MemberRoute{Action: ms.Action, Urgent: ms.Urgent, Source: t.Instance + "." + t.Kind}
	if rt.Action == "" {
		rt.Action = "deliver"
	}
	var err error
	if rt.Member, err = render1("name", ms.Name); err != nil {
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
	if shadow {
		if rt.Team != "" {
			r.Log("%s [dry-run] would route to team %s", flowTag(t), rt.Team)
			return map[string]any{"stubbed": true}, "", nil
		}
		r.Log("%s [dry-run] would %s member %s", flowTag(t), rt.Action, rt.Member)
		return map[string]any{"stubbed": true}, "", nil
	}
	if r.MemberRouter == nil {
		return nil, "", fmt.Errorf("member: no member service on this daemon")
	}
	out, err := r.MemberRouter.RouteMember(ctx, rt)
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	r.audit(map[string]any{"event": "member_route", "member": rt.Member, "team": rt.Team, "action": rt.Action, "source": rt.Source, "urgent": rt.Urgent, "outcome": outcome})
	if err != nil {
		return nil, "", fmt.Errorf("member: %w", err)
	}
	return out, "", nil
}

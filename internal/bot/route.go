package bot

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/flow"
)

var _ flow.BotRouter = (*Service)(nil)

// RouteBot implements flow.BotRouter: the `bot:` helper step.
func (s *Service) RouteBot(ctx context.Context, r flow.BotRoute) (map[string]any, error) {
	if r.Team != "" {
		if r.Action != "" && r.Action != "deliver" {
			return nil, fmt.Errorf("a team is routed deliveries, not %s", r.Action)
		}
		return s.RouteTeam(ctx, TeamEvent{Team: r.Team, Source: r.Source, Requester: r.Requester, Title: r.Title, Text: r.Text, Urgent: r.Urgent, Fields: r.Fields,
			files: r.Files})
	}
	switch r.Action {
	case "", "deliver":
		id, err := s.Deliver(ctx, Event{Bot: r.Bot, Source: r.Source, Requester: r.Requester, Title: r.Title, Text: r.Text, Urgent: r.Urgent, files: r.Files})
		if err != nil {
			return nil, err
		}
		return map[string]any{"case": id}, nil
	case "rotate":
		if err := s.Rotate(ctx, r.Bot, "requested by "+r.Source); err != nil {
			return nil, err
		}
		return map[string]any{"rotated": true}, nil
	case "consolidate":
		w, err := s.Consolidate(ctx, r.Bot)
		if err != nil {
			return nil, err
		}
		if w.ID == "" {
			return map[string]any{"skipped": "no new episodes since the last consolidation"}, nil
		}
		return map[string]any{"worker": w.ID, "case": w.CaseID}, nil
	}
	return nil, fmt.Errorf("unknown bot action %q", r.Action)
}

package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

var _ flow.BotRouter = (*Service)(nil)

// RouteBot implements flow.BotRouter: the `bot:` helper step.
func (s *Service) RouteBot(ctx context.Context, r flow.BotRoute) (map[string]any, error) {
	if r.Team != "" {
		if r.Action != "" && r.Action != "deliver" {
			return nil, fmt.Errorf("a team is routed deliveries, not %s", r.Action)
		}
		return s.RouteTeam(ctx, TeamEvent{Team: r.Team, Source: r.Source, Requester: r.Requester, Title: r.Title, Text: r.Text, Urgent: r.Urgent, Fields: r.Fields})
	}
	switch r.Action {
	case "", "deliver":
		id, err := s.Deliver(ctx, Event{Bot: r.Bot, Source: r.Source, Requester: r.Requester, Title: r.Title, Text: r.Text, Urgent: r.Urgent})
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

// Consolidate distils recent episodes into knowledge. It is judgment work,
// so it is a worker with a written brief (the same path as any other); the
// commits it makes through `conductor bot remember` are the reviewable
// result, and the ledger records when it last ran.
func (s *Service) Consolidate(ctx context.Context, name string) (store.Worker, error) {
	if _, err := s.bot(name); err != nil {
		return store.Worker{}, err
	}
	rec := s.Ledger.Bot(name)
	eps := s.Repo(name).EpisodesSince(ctx, rec.Consolidated)
	if len(eps) == 0 {
		// A scheduled consolidation with nothing new is a no-op, not a
		// failed run: the zero Worker says nothing was started.
		return store.Worker{}, nil
	}
	if len(eps) > 60 {
		eps = eps[len(eps)-60:]
	}
	brief := "CONSOLIDATION. Distil these episodes into durable knowledge: read each, then update knowledge/ (and runbooks/ where a procedure emerged) with what will matter next time — facts about the systems, recurring causes, what worked, what did not. Prefer editing an existing knowledge file over adding a new one; keep files focused; never copy secrets. Write with `conductor bot remember` (one call per file). Do not rewrite the episodes themselves. Finish with step.done summarizing which knowledge files changed and why.\n\nEpisodes:\n- " + strings.Join(eps, "\n- ")
	w, err := s.Spawn(ctx, Actor{Bot: name, Role: config.BotRoleHome}, SpawnRequest{Title: "memory consolidation", Brief: brief, byConductor: true})
	if err != nil {
		return store.Worker{}, err
	}
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error { r.Consolidated = s.now(); return nil })
	return w, nil
}

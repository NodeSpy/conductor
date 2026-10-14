package member

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

var _ flow.MemberRouter = (*Service)(nil)

// RouteMember implements flow.MemberRouter: the `member:` helper step.
func (s *Service) RouteMember(ctx context.Context, r flow.MemberRoute) (map[string]any, error) {
	switch r.Action {
	case "", "deliver":
		id, err := s.Deliver(ctx, Event{Member: r.Member, Source: r.Source, Requester: r.Requester, Title: r.Title, Text: r.Text, Urgent: r.Urgent})
		if err != nil {
			return nil, err
		}
		return map[string]any{"case": id}, nil
	case "rotate":
		if err := s.Rotate(ctx, r.Member, "requested by "+r.Source); err != nil {
			return nil, err
		}
		return map[string]any{"rotated": true}, nil
	case "consolidate":
		w, err := s.Consolidate(ctx, r.Member)
		if err != nil {
			return nil, err
		}
		return map[string]any{"worker": w.ID, "case": w.CaseID}, nil
	}
	return nil, fmt.Errorf("unknown member action %q", r.Action)
}

// Consolidate distils recent episodes into knowledge. It is judgment work,
// so it is a worker with a written brief (the same path as any other); the
// commits it makes through `conductor member remember` are the reviewable
// result, and the ledger records when it last ran.
func (s *Service) Consolidate(ctx context.Context, name string) (store.Worker, error) {
	if _, err := s.member(name); err != nil {
		return store.Worker{}, err
	}
	rec := s.Ledger.Member(name)
	eps := s.Repo(name).EpisodesSince(ctx, rec.Consolidated)
	if len(eps) == 0 {
		return store.Worker{}, fmt.Errorf("nothing to consolidate: no episodes since %s", rec.Consolidated.Format(time.RFC3339))
	}
	if len(eps) > 60 {
		eps = eps[len(eps)-60:]
	}
	brief := "CONSOLIDATION. Distil these episodes into durable knowledge: read each, then update knowledge/ (and runbooks/ where a procedure emerged) with what will matter next time — facts about the systems, recurring causes, what worked, what did not. Prefer editing an existing knowledge file over adding a new one; keep files focused; never copy secrets. Write with `conductor member remember` (one call per file). Do not rewrite the episodes themselves. Finish with step.done summarizing which knowledge files changed and why.\n\nEpisodes:\n- " + strings.Join(eps, "\n- ")
	w, err := s.Spawn(ctx, Actor{Member: name, Role: config.MemberRoleHome}, SpawnRequest{Title: "memory consolidation", Brief: brief})
	if err != nil {
		return store.Worker{}, err
	}
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error { r.Consolidated = s.now(); return nil })
	return w, nil
}

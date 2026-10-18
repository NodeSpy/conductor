package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

type stubRouter struct{ got []BotRoute }

func (s *stubRouter) RouteBot(_ context.Context, r BotRoute) (map[string]any, error) {
	s.got = append(s.got, r)
	return map[string]any{"case": "c1"}, nil
}

// `bot: { team: … }` routes to the team with the event's own fields for
// its rules; the source is the trigger's, never the step's.
func TestBotStepRoutesToATeam(t *testing.T) {
	r := capRunner(t)
	sr := &stubRouter{}
	r.BotRouter = sr
	tr := core.Trigger{Source: "pagerduty", Instance: "pd", Kind: "incident"}
	step := config.Step{Bot: &config.BotStep{Team: "{{.team}}", Title: "{{.title}}", Requester: "{{.user}}", Urgent: true}}
	data := map[string]any{"team": "platform", "title": "db down", "user": "chat:U9", "service": "db-main"}
	if _, _, err := r.execBot(context.Background(), tr, step, data, false); err != nil {
		t.Fatal(err)
	}
	got := sr.got[0]
	if got.Team != "platform" || got.Bot != "" || got.Title != "db down" || got.Requester != "chat:U9" || !got.Urgent || got.Source != "pd.incident" {
		t.Fatalf("route: %+v", got)
	}
	if got.Fields["service"] != "db-main" {
		t.Fatalf("the rules do not see the event's fields: %v", got.Fields)
	}
	// A bot route carries no fields.
	step = config.Step{Bot: &config.BotStep{Name: "sre", Title: "x"}}
	if _, _, err := r.execBot(context.Background(), tr, step, data, false); err != nil {
		t.Fatal(err)
	}
	if got := sr.got[1]; got.Team != "" || got.Bot != "sre" || got.Fields != nil {
		t.Fatalf("bot route: %+v", got)
	}
	// Dry run routes nothing.
	step = config.Step{Bot: &config.BotStep{Team: "platform"}}
	if out, _, err := r.execBot(context.Background(), tr, step, data, true); err != nil || out["stubbed"] != true || len(sr.got) != 2 {
		t.Fatalf("dry run routed: %v %v", out, err)
	}
}
